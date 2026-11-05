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

func desktopFailure(err error) (int, string) {
	var upstream *instabox.APIError
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case "desktop_not_running", "machine_not_running":
			return 409, upstream.Code
		case "missing_scope", "capability_denied":
			return 403, upstream.Code
		case "quota_exceeded":
			return 429, "quota_exceeded"
		}
		if upstream.StatusCode == 429 {
			return 429, "rate_limited"
		}
	}
	if errors.Is(err, terminal.InvalidTicket) {
		return 403, "invalid_ticket"
	}
	if errors.Is(err, terminal.QuotaExceeded) {
		return 429, "quota_exceeded"
	}
	return 503, "desktop_unavailable"
}
func (api *API) desktop(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CredentialID string `json:"credentialId"`
		Action       string `json:"action"`
		Width        *int   `json:"width,omitempty"`
		Height       *int   `json:"height,omitempty"`
	}
	if !decode(w, r, &input, 1024) {
		return
	}
	metadata, machine, err := api.visibleMachine(r, input.CredentialID)
	if err != nil {
		fail(w, r, 404, "machine_not_visible")
		return
	}
	if metadata.Capabilities.Write == account.Denied {
		fail(w, r, 403, "capability_denied")
		return
	}
	if input.Action != "status" && input.Action != "install" && input.Action != "start" || input.Width != nil && (*input.Width < 800 || *input.Width > 2560) || input.Height != nil && (*input.Height < 600 || *input.Height > 1600) {
		fail(w, r, 400, "invalid_request")
		return
	}
	event := ""
	if input.Action != "status" {
		event = "desktop." + input.Action
		if api.event(r, event, "intent", metadata.CredentialID, machine.ID) != nil {
			fail(w, r, 503, "unavailable")
			return
		}
	}
	// start waits for the VNC server to answer. This stays under Cloudflare's 100 seconds.
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	_, client, err := api.dependencies.Accounts.DesktopClientForCredential(ctx, metadata.CredentialID, metadata.Version)
	var result instabox.Desktop
	if err == nil {
		result, err = client.Desktop(ctx, machine.ID, instabox.DesktopRequest{Action: input.Action, Width: input.Width, Height: input.Height})
	}
	status, code := 200, ""
	if err != nil {
		status, code = desktopFailure(err)
	}
	if event != "" {
		outcome := "success"
		if err != nil {
			outcome = "failure"
		}
		_ = api.eventDetail(r, event, outcome, metadata.CredentialID, machine.ID, code)
	}
	if err != nil {
		fail(w, r, status, code)
		return
	}
	writeJSON(w, 200, result)
}
func (api *API) desktopTicket(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CredentialID string `json:"credentialId"`
	}
	if !decode(w, r, &input, 1024) {
		return
	}
	metadata, machine, err := api.visibleMachine(r, input.CredentialID)
	if err != nil {
		fail(w, r, 404, "machine_not_visible")
		return
	}
	if metadata.Capabilities.Write == account.Denied {
		fail(w, r, 403, "capability_denied")
		return
	}
	if machine.State != "running" {
		fail(w, r, 409, "machine_not_running")
		return
	}
	api.authMutex.Lock()
	defer api.authMutex.Unlock()
	session, err := api.dependencies.Identity.Session(r.Context(), api.token(r, api.policy.Load().sessionCookie))
	if err != nil {
		fail(w, r, 401, "unauthorized")
		return
	}
	ticket, err := api.dependencies.Terminal.IssueDesktopTicket(terminal.Binding{SessionHash: session.Hash, CredentialID: metadata.CredentialID, CredentialVersion: int64(metadata.Version), MachineID: machine.ID, OrgID: metadata.OrgID})
	if err != nil {
		fail(w, r, 503, "unavailable")
		return
	}
	writeJSON(w, 200, map[string]string{"ticket": ticket.Value()})
}
func (api *API) desktopWebsocket(w http.ResponseWriter, r *http.Request) {
	protocol, ticket := false, ""
	for _, raw := range strings.Split(strings.Join(r.Header.Values("Sec-WebSocket-Protocol"), ","), ",") {
		candidate := strings.TrimSpace(raw)
		if candidate == "instadeck.desktop.v1" {
			protocol = true
		}
		if strings.HasPrefix(candidate, "instadeck.ticket.") {
			if ticket != "" {
				fail(w, r, 403, "invalid_ticket")
				return
			}
			ticket = strings.TrimPrefix(candidate, "instadeck.ticket.")
		}
	}
	if !protocol || len(ticket) != 43 {
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
	ctx, cancel := context.WithDeadline(r.Context(), session.CreatedAt.Add(api.dependencies.AbsoluteSessionTTL))
	defer cancel()
	upgraded := false
	response := &handshakeResponse{ResponseWriter: w}
	err = api.dependencies.Terminal.ServeDesktop(ctx, terminal.Ticket(ticket), session.Hash, func(ctx context.Context, binding terminal.Binding) error {
		// The desktop is registered now, so a logout cancels ctx. The dial below can
		// take seconds and must not hold up every other login and logout.
		api.authMutex.Unlock()
		locked = false
		dialCtx, stop := context.WithTimeout(ctx, 10*time.Second)
		metadata, client, e := api.dependencies.Accounts.DesktopClientForCredential(dialCtx, binding.CredentialID, int(binding.CredentialVersion))
		if e != nil || metadata.OrgID != binding.OrgID {
			stop()
			return terminal.InvalidTicket
		}
		if metadata.Capabilities.Write == account.Denied {
			stop()
			return &instabox.APIError{StatusCode: 403, Code: "capability_denied"}
		}
		upstream, e := client.DialDesktop(dialCtx, binding.MachineID)
		stop()
		if e != nil {
			_, code := desktopFailure(e)
			_ = api.eventDetail(r, "desktop.open", "failure", binding.CredentialID, binding.MachineID, code)
			return e
		}
		defer upstream.CloseNow()
		if api.event(r, "desktop.open", "intent", binding.CredentialID, binding.MachineID) != nil {
			return errors.New("audit unavailable")
		}
		browser, e := websocket.Accept(response, r, &websocket.AcceptOptions{Subprotocols: []string{"instadeck.desktop.v1"}, OriginPatterns: []string{strings.NewReplacer("[", "\\[", "]", "\\]").Replace(api.policy.Load().origin)}, CompressionMode: websocket.CompressionDisabled})
		if e != nil {
			return terminal.InvalidTicket
		}
		upgraded = true
		_ = api.event(r, "desktop.open", "success", binding.CredentialID, binding.MachineID)
		defer browser.CloseNow()
		browser.SetReadLimit(1 << 20)
		upstream.SetReadLimit(64 << 20)
		proxyCtx, finish := context.WithCancel(ctx)
		defer finish()
		var workers sync.WaitGroup
		done := make(chan struct{}, 2)
		pump := func(src, dst *websocket.Conn) {
			defer workers.Done()
			defer func() { done <- struct{}{} }()
			for {
				kind, payload, e := src.Read(proxyCtx)
				if e != nil || kind != websocket.MessageBinary {
					return
				}
				if dst.Write(proxyCtx, kind, payload) != nil {
					return
				}
			}
		}
		workers.Add(3)
		go pump(browser, upstream)
		go pump(upstream, browser)
		go func() {
			defer workers.Done()
			websocketHeartbeat(proxyCtx, 30*time.Second, 15*time.Second, browser.Ping, func() { _ = browser.CloseNow() })
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
		finish()
		_ = browser.CloseNow()
		_ = upstream.CloseNow()
		workers.Wait()
		return nil
	})
	if err != nil && !upgraded && !response.switched {
		status, code := desktopFailure(err)
		fail(w, r, status, code)
	}
}
