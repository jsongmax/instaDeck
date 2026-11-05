package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"instadeck/internal/account"
	"instadeck/internal/httpapi"
	"instadeck/internal/instabox"
	"instadeck/internal/terminal"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDesktopRequests(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	origin := f.dependencies.Config.Origin
	base := "/api/v1/machines/machine/desktop"
	body := fmt.Sprintf(`{"credentialId":%q,"action":"status"}`, a.CredentialID)
	var calls atomic.Int32
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/desktop") {
			return false
		}
		calls.Add(1)
		respond(w, 200, map[string]any{"installed": true, "running": true, "installing": false, "failed": false, "log": testKey, "hint": testKey, "actions": []string{testKey}})
		return true
	}
	for _, route := range []string{base, base + "/tickets"} {
		input := body
		if strings.HasSuffix(route, "tickets") {
			input = fmt.Sprintf(`{"credentialId":%q}`, a.CredentialID)
		}
		if r := f.request("POST", route, input, nil, csrf, origin); r.Code != 401 {
			t.Fatal(r.Code, r.Body)
		}
		if r := f.request("POST", route, input, cookie, "", origin); r.Code != 403 {
			t.Fatal(r.Code, r.Body)
		}
		if r := f.request("POST", route, input, cookie, csrf, "http://wrong.invalid"); r.Code != 403 {
			t.Fatal(r.Code, r.Body)
		}
	}
	for _, extra := range []string{`"action":"oops"`, `"action":"start","width":0`, `"action":"start","height":1601`, `"action":"start","width":2561`, `"action":"start","height":599`} {
		r := f.request("POST", base, fmt.Sprintf(`{"credentialId":%q,%s}`, a.CredentialID, extra), cookie, csrf, origin)
		if r.Code != 400 || !strings.Contains(r.Body.String(), "invalid_request") {
			t.Fatal(r.Code, r.Body)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached desktop upstream")
	}
	for _, action := range []string{"status", "install", "start"} {
		r := f.request("POST", base, fmt.Sprintf(`{"credentialId":%q,"action":%q}`, a.CredentialID, action), cookie, csrf, origin)
		if r.Code != 200 || strings.Contains(r.Body.String(), testKey) || strings.Contains(r.Body.String(), "log") {
			t.Fatal(r.Code, r.Body)
		}
		var dto map[string]any
		_ = json.Unmarshal(r.Body.Bytes(), &dto)
		if len(dto) != 4 {
			t.Fatal(dto)
		}
	}
	records, e := f.store.ReadAuditEvents(context.Background(), 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(records)
	if !bytes.Contains(encoded, []byte("desktop.install")) || !bytes.Contains(encoded, []byte("desktop.start")) || bytes.Contains(encoded, []byte("desktop.status")) {
		t.Fatal(string(encoded))
	}
	if r := f.request("POST", base, `{"credentialId":"unknown","action":"status"}`, cookie, csrf, origin); r.Code != 404 {
		t.Fatal(r.Code, r.Body)
	}
	if e := f.dependencies.Accounts.Observe(context.Background(), a.CredentialID, a.Version, account.Write, &instabox.APIError{StatusCode: 403, Code: "missing_scope"}); e != nil {
		t.Fatal(e)
	}
	for _, route := range []string{base, base + "/tickets"} {
		input := body
		if strings.HasSuffix(route, "tickets") {
			input = fmt.Sprintf(`{"credentialId":%q}`, a.CredentialID)
		}
		r := f.request("POST", route, input, cookie, csrf, origin)
		if r.Code != 403 || !strings.Contains(r.Body.String(), "capability_denied") {
			t.Fatal(r.Code, r.Body)
		}
	}
}

func TestDesktopWebsocket(t *testing.T) {
	for _, end := range []string{"upstream", "browser", "logout", "revocation"} {
		t.Run(end, func(t *testing.T) {
			f := newFixture(t, false)
			cookie, csrf := f.login(t)
			a := f.addAccount(t, cookie, csrf)
			upstreamClosed := make(chan struct{})
			closeUpstream := make(chan struct{})
			payload := []byte{0, 1, 2, 255, 128, 0, 42}
			f.resource = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/vnc") {
					return false
				}
				defer close(upstreamClosed)
				if r.Header.Get("X-API-Key") != testKey || r.URL.RawQuery != "" || strings.Contains(r.URL.String(), testKey) {
					t.Error("unsafe upstream authentication")
				}
				conn, e := websocket.Accept(w, r, nil)
				if e != nil {
					t.Error(e)
					return true
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				kind, b, e := conn.Read(ctx)
				if e != nil || kind != websocket.MessageBinary || !bytes.Equal(b, payload) {
					t.Error("upstream payload", e)
					return true
				}
				if e = conn.Write(ctx, websocket.MessageBinary, append([]byte{9}, b...)); e != nil {
					t.Error(e)
					return true
				}
				if end == "upstream" {
					<-closeUpstream
					return true
				}
				_, _, _ = conn.Read(ctx)
				return true
			}
			server := httptest.NewServer(f.api)
			defer server.Close()
			ticketResponse := f.request("POST", "/api/v1/machines/machine/desktop/tickets", fmt.Sprintf(`{"credentialId":%q}`, a.CredentialID), cookie, csrf, f.dependencies.Config.Origin)
			var ticket struct {
				Ticket string `json:"ticket"`
			}
			if json.Unmarshal(ticketResponse.Body.Bytes(), &ticket) != nil || ticket.Ticket == "" {
				t.Fatal(ticketResponse.Code, ticketResponse.Body)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			options := &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie.String()}, "Origin": {f.dependencies.Config.Origin}}, Subprotocols: []string{"instadeck.desktop.v1", "instadeck.ticket." + ticket.Ticket}}
			conn, resp, e := websocket.Dial(ctx, server.URL+"/api/v1/desktop/ws", options)
			if e != nil {
				t.Fatal(e, resp)
			}
			defer conn.CloseNow()
			if conn.Subprotocol() != "instadeck.desktop.v1" {
				t.Fatal(conn.Subprotocol())
			}
			if e = conn.Write(ctx, websocket.MessageBinary, payload); e != nil {
				t.Fatal(e)
			}
			kind, b, e := conn.Read(ctx)
			if e != nil || kind != websocket.MessageBinary || !bytes.Equal(b, append([]byte{9}, payload...)) {
				t.Fatal("browser payload", b, e)
			}
			reused, reject, e := websocket.Dial(ctx, server.URL+"/api/v1/desktop/ws", options)
			if reused != nil {
				reused.CloseNow()
			}
			if e == nil || reject.StatusCode != 403 {
				t.Fatal("reused ticket accepted")
			}
			switch end {
			case "upstream":
				close(closeUpstream)
			case "browser":
				_ = conn.CloseNow()
			case "logout":
				r := f.request("POST", "/api/v1/auth/logout", `{}`, cookie, csrf, f.dependencies.Config.Origin)
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
			case "revocation":
				f.clock.Add(30)
				other, otherCSRF := f.login(t)
				r := f.request("POST", "/api/v1/sessions/revoke-others", `{}`, other, otherCSRF, f.dependencies.Config.Origin)
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body)
				}
			}
			if end != "browser" {
				if _, _, e = conn.Read(ctx); e == nil {
					t.Fatal("browser stayed open")
				}
			}
			select {
			case <-upstreamClosed:
			case <-ctx.Done():
				t.Fatal("upstream stayed open")
			}
			if strings.Contains(f.logs.String(), testKey) || strings.Contains(f.logs.String(), ticket.Ticket) {
				t.Fatal("secret in logs")
			}
		})
	}
}
func TestDesktopWebsocketRejections(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	var upstreamCalls atomic.Int32
	var code atomic.Value
	code.Store("desktop_not_running")
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/vnc") {
			return false
		}
		upstreamCalls.Add(1)
		c := code.Load().(string)
		status := 409
		if c == "missing_scope" {
			status = 403
		}
		if c == "quota_exceeded" || c == "rate_limited" {
			status = 429
		}
		respond(w, status, map[string]any{"error": map[string]string{"code": c, "message": testKey}})
		return true
	}
	server := httptest.NewServer(f.api)
	defer server.Close()
	issue := func() string {
		r := f.request("POST", "/api/v1/machines/machine/desktop/tickets", fmt.Sprintf(`{"credentialId":%q}`, a.CredentialID), cookie, csrf, f.dependencies.Config.Origin)
		var v struct{ Ticket string }
		_ = json.Unmarshal(r.Body.Bytes(), &v)
		if v.Ticket == "" {
			t.Fatal(r.Code, r.Body)
		}
		return v.Ticket
	}
	dial := func(ticket, origin string, c *http.Cookie, status int, want string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, r, e := websocket.Dial(ctx, server.URL+"/api/v1/desktop/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {c.String()}, "Origin": {origin}}, Subprotocols: []string{"instadeck.desktop.v1", "instadeck.ticket." + ticket}})
		if conn != nil {
			conn.CloseNow()
		}
		if e == nil || r == nil || r.StatusCode != status {
			t.Fatalf("want %d: %v %v", status, r, e)
		}
		b := bodyString(t, r)
		if !strings.Contains(b, want) || strings.Contains(b, testKey) {
			t.Fatal(b)
		}
	}
	dial("", f.dependencies.Config.Origin, cookie, 403, "invalid_ticket")
	dial(issue(), "http://wrong.invalid", cookie, 403, "origin_rejected")
	f.clock.Add(30)
	other, _ := f.login(t)
	dial(issue(), f.dependencies.Config.Origin, other, 403, "invalid_ticket")
	tr := f.request("POST", "/api/v1/machines/machine/terminal-tickets", fmt.Sprintf(`{"credentialId":%q}`, a.CredentialID), cookie, csrf, f.dependencies.Config.Origin)
	var v struct{ Ticket string }
	_ = json.Unmarshal(tr.Body.Bytes(), &v)
	dial(v.Ticket, f.dependencies.Config.Origin, cookie, 403, "invalid_ticket")
	if upstreamCalls.Load() != 0 {
		t.Fatal("invalid tickets reached upstream")
	}
	for _, c := range []string{"desktop_not_running", "machine_not_running", "missing_scope", "quota_exceeded", "rate_limited"} {
		code.Store(c)
		status := 409
		if c == "missing_scope" {
			status = 403
		}
		if c == "quota_exceeded" || c == "rate_limited" {
			status = 429
		}
		dial(issue(), f.dependencies.Config.Origin, cookie, status, c)
	}
	oldTicket := issue()
	changed := f.request("PATCH", "/api/v1/accounts/"+a.CredentialID, `{"key":"replacement-key","name":"test","declarations":{"write":true,"delete":true}}`, cookie, csrf, f.dependencies.Config.Origin)
	if changed.Code != 200 {
		t.Fatal(changed.Code, changed.Body)
	}
	before := upstreamCalls.Load()
	dial(oldTicket, f.dependencies.Config.Origin, cookie, 403, "invalid_ticket")
	if upstreamCalls.Load() != before {
		t.Fatal("replaced credential reached upstream")
	}

}

func TestDesktopTicketExpiryAndIsolation(t *testing.T) {
	f := newFixture(t, false)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m, e := terminal.New(nilTerminalUpstream{}, terminal.Options{Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	if e != nil {
		t.Fatal(e)
	}
	defer m.Shutdown()
	binding := terminal.Binding{SessionHash: [32]byte{1}, CredentialID: "credential", CredentialVersion: 1, MachineID: "machine", OrgID: "org"}
	expired, e := m.IssueDesktopTicket(binding)
	if e != nil {
		t.Fatal(e)
	}
	clock.Add(int64(30 * time.Second))
	if e = m.ServeDesktop(context.Background(), expired, binding.SessionHash, func(context.Context, terminal.Binding) error { t.Fatal("expired callback"); return nil }); e != terminal.InvalidTicket {
		t.Fatal(e)
	}
	desktop, e := f.dependencies.Terminal.IssueDesktopTicket(binding)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.dependencies.Terminal.ServeUpgrade(context.Background(), desktop, binding.SessionHash, func(terminal.Binding) (terminal.FrameConn, error) {
		t.Fatal("desktop ticket opened terminal")
		return nil, nil
	}); e != terminal.InvalidTicket {
		t.Fatal(e)
	}
}

type nilTerminalUpstream struct{}

func (nilTerminalUpstream) GetConnectionInfo(context.Context, terminal.Binding) (instabox.ConnectionInfo, error) {
	return instabox.ConnectionInfo{}, terminal.Unavailable
}
func (nilTerminalUpstream) CreateConnection(context.Context, terminal.Binding, instabox.CreateConnectionRequest, string) (instabox.Connection, error) {
	return instabox.Connection{}, terminal.Unavailable
}

func TestDesktopExpiredTicketHTTP(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	manager, e := terminal.New(nilTerminalUpstream{}, terminal.Options{Now: func() time.Time { return time.Unix(0, clock.Load()) }})
	if e != nil {
		t.Fatal(e)
	}
	defer manager.Shutdown()
	dependencies := f.dependencies
	dependencies.Terminal = manager
	api, e := httpapi.New(dependencies)
	if e != nil {
		t.Fatal(e)
	}
	f.api = api
	r := f.request("POST", "/api/v1/machines/machine/desktop/tickets", fmt.Sprintf(`{"credentialId":%q}`, a.CredentialID), cookie, csrf, f.dependencies.Config.Origin)
	var v struct{ Ticket string }
	_ = json.Unmarshal(r.Body.Bytes(), &v)
	if v.Ticket == "" {
		t.Fatal(r.Code, r.Body)
	}
	clock.Add(int64(30 * time.Second))
	server := httptest.NewServer(f.api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, response, e := websocket.Dial(ctx, server.URL+"/api/v1/desktop/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {cookie.String()}, "Origin": {f.dependencies.Config.Origin}}, Subprotocols: []string{"instadeck.desktop.v1", "instadeck.ticket." + v.Ticket}})
	if conn != nil {
		conn.CloseNow()
	}
	if e == nil || response == nil || response.StatusCode != 403 {
		t.Fatal(e, response)
	}
	if b := bodyString(t, response); !strings.Contains(b, "invalid_ticket") {
		t.Fatal(b)
	}
}

// noVNC draws JPEG frames from data: URLs, and real VNC servers send JPEG.
func TestContentSecurityPolicyAllowsDesktopFrames(t *testing.T) {
	f := newFixture(t, false)
	r := f.request("GET", "/api/v1/healthz", "", nil, "", "")
	policy := r.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "img-src 'self' data:") {
		t.Fatalf("policy: %s", policy)
	}
	for _, unsafe := range []string{"script-src", "unsafe-eval", "default-src *"} {
		if strings.Contains(policy, unsafe) {
			t.Fatalf("policy widened with %s: %s", unsafe, policy)
		}
	}
}
