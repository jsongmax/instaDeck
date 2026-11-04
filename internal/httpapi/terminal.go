package httpapi

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
	"instadeck/internal/terminal"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TerminalUpstream binds every connection request to the server's credential version.
type TerminalUpstream struct{ Accounts *account.Service }

func (upstream TerminalUpstream) client(ctx context.Context, binding terminal.Binding) (account.ConnectionClient, error) {
	metadata, client, err := upstream.Accounts.ConnectionClientForCredential(ctx, binding.CredentialID, int(binding.CredentialVersion))
	if err != nil || metadata.OrgID != binding.OrgID {
		return nil, terminal.Unavailable
	}
	return client, nil
}

// GetConnectionInfo obtains PTY capabilities using the exact bound credential.
func (upstream TerminalUpstream) GetConnectionInfo(ctx context.Context, binding terminal.Binding) (instabox.ConnectionInfo, error) {
	client, err := upstream.client(ctx, binding)
	if err != nil {
		return instabox.ConnectionInfo{}, err
	}
	return client.GetConnectionInfo(ctx, binding.MachineID)
}

// CreateConnection requests a short-lived certificate with a stable retry key.
func (upstream TerminalUpstream) CreateConnection(ctx context.Context, binding terminal.Binding, body instabox.CreateConnectionRequest, key string) (instabox.Connection, error) {
	client, err := upstream.client(ctx, binding)
	if err != nil {
		return instabox.Connection{}, err
	}
	return client.CreateConnection(ctx, binding.MachineID, body, key)
}

func (api *API) ticket(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CredentialID string `json:"credentialId"`
	}
	if !decode(w, r, &input, 1024) {
		return
	}
	metadata, machine, err := api.visibleMachine(r, input.CredentialID)
	if err != nil || machine.State != "running" {
		fail(w, r, 400, "terminal_rejected")
		return
	}
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	session, err := api.dependencies.Identity.Session(r.Context(), api.token(r, api.policy.Load().sessionCookie))
	if err != nil {
		fail(w, r, 401, "unauthorized")
		return
	}
	ticket, err := api.dependencies.Terminal.IssueTicket(terminal.Binding{SessionHash: session.Hash, CredentialID: metadata.CredentialID, CredentialVersion: int64(metadata.Version), MachineID: machine.ID, OrgID: metadata.OrgID})
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"ticket": ticket.Value(), "protocol": "instadeck.terminal.v1"})
}

func (api *API) websocket(w http.ResponseWriter, r *http.Request) {
	protocol, ticketValue := false, ""
	for _, raw := range strings.Split(strings.Join(r.Header.Values("Sec-WebSocket-Protocol"), ","), ",") {
		candidate := strings.TrimSpace(raw)
		if candidate == "instadeck.terminal.v1" {
			protocol = true
		}
		if strings.HasPrefix(candidate, "instadeck.ticket.") {
			if ticketValue != "" {
				fail(w, r, 403, "invalid_ticket")
				return
			}
			ticketValue = strings.TrimPrefix(candidate, "instadeck.ticket.")
		}
	}
	if !protocol || len(ticketValue) != 43 {
		fail(w, r, 403, "invalid_ticket")
		return
	}
	api.authMutex.Lock()
	locked := true
	defer func() {
		if locked {
			api.authMutex.Unlock()
		}
	}()
	session, err := api.dependencies.Identity.Session(r.Context(), api.token(r, api.policy.Load().sessionCookie))
	if err != nil {
		fail(w, r, 401, "unauthorized")
		return
	}
	ctx, cancel := context.WithDeadline(context.Background(), session.CreatedAt.Add(api.dependencies.AbsoluteSessionTTL))
	defer cancel()
	upgraded := false
	upgradeResponse := &handshakeResponse{ResponseWriter: w}
	rejectionStatus, rejectionCode := 403, "invalid_ticket"
	var openedBinding terminal.Binding
	err = api.dependencies.Terminal.ServeUpgrade(ctx, terminal.Ticket(ticketValue), session.Hash, func(binding terminal.Binding) (terminal.FrameConn, error) {
		if ctx.Err() != nil {
			return nil, terminal.TimedOut
		}
		metadata, _, err := api.dependencies.Accounts.ConnectionClientForCredential(r.Context(), binding.CredentialID, int(binding.CredentialVersion))
		if err != nil || metadata.OrgID != binding.OrgID {
			return nil, terminal.InvalidTicket
		}
		if api.event(r, "terminal.open", "intent", binding.CredentialID, binding.MachineID) != nil {
			rejectionStatus, rejectionCode = 503, "audit_unavailable"
			return nil, errors.New("audit unavailable")
		}
		pattern := strings.NewReplacer("[", "\\[", "]", "\\]").Replace(api.policy.Load().origin)
		if ctx.Err() != nil {
			return nil, terminal.TimedOut
		}
		connection, err := websocket.Accept(upgradeResponse, r, &websocket.AcceptOptions{Subprotocols: []string{"instadeck.terminal.v1"}, OriginPatterns: []string{pattern}, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return nil, err
		}
		upgraded = true
		openedBinding = binding
		api.authMutex.Unlock()
		locked = false
		frames := &websocketFrames{connection: connection}
		heartbeatContext, stop := context.WithCancel(ctx)
		frames.stop = stop
		frames.done = make(chan struct{})
		go func() {
			defer close(frames.done)
			websocketHeartbeat(heartbeatContext, 30*time.Second, 15*time.Second, connection.Ping, func() { _ = connection.CloseNow() })
		}()
		return frames, nil
	})
	if upgraded {
		closeContext, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		closeContext = context.WithValue(closeContext, sessionKey, session)
		closeContext = context.WithValue(closeContext, requestIDKey, r.Context().Value(requestIDKey))
		_ = api.event(r.WithContext(closeContext), "terminal.close", "closed", openedBinding.CredentialID, openedBinding.MachineID)
		return
	}
	if err != nil && !upgradeResponse.switched {
		for _, header := range []string{"Upgrade", "Connection", "Sec-WebSocket-Accept", "Sec-WebSocket-Protocol", "Sec-WebSocket-Version"} {
			w.Header().Del(header)
		}
		fail(w, r, rejectionStatus, rejectionCode)
	}
}

type handshakeResponse struct {
	http.ResponseWriter
	rejected, switched bool
}

func (response *handshakeResponse) WriteHeader(status int) {
	if status >= 400 {
		response.rejected = true
		return
	}
	response.switched = status == http.StatusSwitchingProtocols
	response.ResponseWriter.WriteHeader(status)
}

func (response *handshakeResponse) Write(payload []byte) (int, error) {
	if response.rejected {
		return len(payload), nil
	}
	return response.ResponseWriter.Write(payload)
}

func (response *handshakeResponse) Unwrap() http.ResponseWriter { return response.ResponseWriter }

type websocketFrames struct {
	connection    *websocket.Conn
	stop          context.CancelFunc
	done          chan struct{}
	closeOnce     sync.Once
	closeErr      error
	writeDeadline time.Time
}

func (frames *websocketFrames) ReadFrame(ctx context.Context, limit int) (bool, []byte, error) {
	frames.connection.SetReadLimit(int64(limit))
	kind, payload, err := frames.connection.Read(ctx)
	return kind == websocket.MessageBinary, payload, err
}

func (frames *websocketFrames) WriteFrame(ctx context.Context, binary bool, payload []byte) error {
	if !frames.writeDeadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, frames.writeDeadline)
		defer cancel()
	}
	kind := websocket.MessageText
	if binary {
		kind = websocket.MessageBinary
	}
	return frames.connection.Write(ctx, kind, payload)
}

func (frames *websocketFrames) SetWriteDeadline(deadline time.Time) error {
	frames.writeDeadline = deadline
	return nil
}

func (frames *websocketFrames) Close() error {
	frames.closeOnce.Do(func() {
		if frames.stop != nil {
			frames.stop()
		}
		// An abrupt close with unread client frames resets the connection, and the
		// browser then discards an error frame it has not delivered yet.
		frames.closeErr = frames.connection.Close(websocket.StatusNormalClosure, "")
		_ = frames.connection.CloseNow()
		if frames.done != nil {
			<-frames.done
		}
	})
	return frames.closeErr
}

// Control frames never enter the terminal activity path.
func websocketHeartbeat(ctx context.Context, interval, timeout time.Duration, ping func(context.Context) error, closeConnection func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			pingContext, cancel := context.WithTimeout(ctx, timeout)
			err := ping(pingContext)
			cancel()
			if err != nil {
				closeConnection()
				return
			}
		}
	}
}
