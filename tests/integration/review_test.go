package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"instadeck/internal/account"
	"instadeck/internal/httpapi"
	"instadeck/internal/instabox"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func expectErrorCode(t *testing.T, response *httptest.ResponseRecorder, expected string) {
	t.Helper()
	var value struct {
		Error struct{ Code, Message string }
	}
	if json.Unmarshal(response.Body.Bytes(), &value) != nil || value.Error.Code != expected {
		t.Errorf("got %d %s, want %s", response.Code, response.Body, expected)
	}
	if value.Error.Message == "Request rejected." {
		t.Errorf("missing specific fixed message for %s", expected)
	}
}

func TestHTTPRetryIgnoresServerRequestID(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	var submitted atomic.Int32
	original := fixture.upstream.Config.Handler
	fixture.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/machines/machine/start" {
			submitted.Add(1)
		}
		original.ServeHTTP(w, r)
	})
	if err := fixture.dependencies.Operations.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"same-client-intent","kind":"start","body":{}}`, credential.CredentialID)
	first := fixture.request("POST", "/api/v1/machines/machine/actions", body, cookie, csrf, fixture.dependencies.Config.Origin)
	second := fixture.request("POST", "/api/v1/machines/machine/actions", body, cookie, csrf, fixture.dependencies.Config.Origin)
	var firstAction, secondAction struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &firstAction)
	_ = json.Unmarshal(second.Body.Bytes(), &secondAction)
	if first.Code != 202 || second.Code != 202 || firstAction.ID == "" || firstAction.ID != secondAction.ID {
		t.Fatalf("HTTP retry created another action: %d %d", first.Code, second.Code)
	}
	if first.Header().Get("X-Request-ID") == second.Header().Get("X-Request-ID") {
		t.Fatal("test did not exercise distinct server request IDs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := fixture.dependencies.Operations.Get(ctx, firstAction.ID)
		if err != nil {
			t.Fatal(err)
		}
		if value.State == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("operation did not complete")
		case <-ticker.C:
		}
	}
	fixture.dependencies.Operations.Stop()
	if submitted.Load() != 1 {
		t.Fatalf("upstream submissions = %d, want 1", submitted.Load())
	}
}

func TestUnusedTicketSurvivesSessionRotation(t *testing.T) {
	fixture := newFixture(t, false)
	fakeSSH(t, fixture)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
	fixture.clock.Add(61)
	response := fixture.request("POST", "/api/v1/auth/reauthenticate", fmt.Sprintf(`{"password":%q,"code":%q}`, testPassword, totpCode(fixture.clock.Load()/30)), cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 200 {
		t.Fatal("reauthentication failed", response.Body)
	}
	newCookie := response.Result().Cookies()[0]
	server := httptest.NewServer(fixture.api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := dialTerminal(ctx, server, newCookie, fixture.dependencies.Config.Origin, ticket)
	if err != nil {
		t.Fatal("unused ticket lost during rotation", err)
	}
	defer connection.CloseNow()
	if _, _, err := connection.Read(ctx); err != nil {
		t.Fatal("rebound ticket did not start terminal", err)
	}
}

func TestFailedKeyReplacementKeepsTerminalOpen(t *testing.T) {
	fixture := newFixture(t, false)
	fakeSSH(t, fixture)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	original := fixture.upstream.Config.Handler
	fixture.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/me" && r.Header.Get("X-API-Key") == "rejected-key" {
			respond(w, 401, map[string]any{"error": map[string]string{"message": "rejected"}})
			return
		}
		original.ServeHTTP(w, r)
	})
	server := httptest.NewServer(fixture.api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
	connection, _, err := dialTerminal(ctx, server, cookie, fixture.dependencies.Config.Origin, ticket)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if _, _, err = connection.Read(ctx); err != nil {
		t.Fatal("terminal not ready", err)
	}
	response := fixture.request("PATCH", "/api/v1/accounts/"+credential.CredentialID, `{"key":"rejected-key"}`, cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 400 {
		t.Fatal("bad replacement accepted")
	}
	if err = connection.Write(ctx, websocket.MessageBinary, []byte("still-open")); err != nil {
		t.Fatal("failed replacement closed terminal", err)
	}
	_, echoed, err := connection.Read(ctx)
	if err != nil || string(echoed) != "still-open" {
		t.Fatal("failed replacement closed terminal", err)
	}
}

func TestSlowUpstreamDoesNotBlockSessionChanges(t *testing.T) {
	for _, operation := range []string{"ticket", "add", "replace", "validate"} {
		for _, sessionChange := range []string{"logout", "reauthenticate"} {
			t.Run(operation+"/"+sessionChange, func(t *testing.T) {
				fixture := newFixture(t, false)
				cookie, csrf := fixture.login(t)
				credential := fixture.addAccount(t, cookie, csrf)
				entered, release := make(chan struct{}), make(chan struct{})
				original := fixture.upstream.Config.Handler
				fixture.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if (operation == "ticket" && r.URL.Path == "/v1/machines/machine") || (operation != "ticket" && r.URL.Path == "/v1/me") {
						close(entered)
						<-release
					}
					original.ServeHTTP(w, r)
				})
				path, method, body := "/api/v1/machines/machine/terminal-tickets", "POST", fmt.Sprintf(`{"credentialId":%q}`, credential.CredentialID)
				switch operation {
				case "add":
					path, body = "/api/v1/accounts", `{"name":"slow","key":"slow-key"}`
				case "replace":
					path, method, body = "/api/v1/accounts/"+credential.CredentialID, "PATCH", `{"key":"replacement-key"}`
				case "validate":
					path, body = "/api/v1/accounts/"+credential.CredentialID+"/validate", `{}`
				}
				slowDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					slowDone <- fixture.request(method, path, body, cookie, csrf, fixture.dependencies.Config.Origin)
				}()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					close(release)
					<-slowDone
					t.Fatal("upstream not reached")
				}
				fixture.clock.Add(61)
				controlBody := fmt.Sprintf(`{"password":%q,"code":%q}`, testPassword, totpCode(fixture.clock.Load()/30))
				controlDone := make(chan *httptest.ResponseRecorder, 1)
				go func() {
					controlDone <- fixture.request("POST", "/api/v1/auth/"+sessionChange, controlBody, cookie, csrf, fixture.dependencies.Config.Origin)
				}()
				var control *httptest.ResponseRecorder
				select {
				case control = <-controlDone:
				case <-time.After(time.Second):
					t.Error("session change blocked by upstream")
				}
				close(release)
				slowResponse := <-slowDone
				if control == nil {
					control = <-controlDone
				}
				if control.Code != 200 {
					t.Fatal("session change failed", control.Body)
				}
				if operation == "ticket" && slowResponse.Code != 401 {
					t.Errorf("ticket bound to invalidated session: %d", slowResponse.Code)
				}
			})
		}
	}
}

func TestOperationFailureCodesReachHTTP(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	for index, test := range []struct{ machine, kind, body, confirmation, code string }{
		{machine: "machine", kind: "delete", confirmation: "wrong", code: "confirmation_mismatch"},
		{machine: "missing", kind: "start", body: `{}`, code: "machine_not_visible"},
		{machine: "machine", kind: "start", body: `{"unexpected":true}`, code: "invalid_body"},
	} {
		body := test.body
		if body == "" {
			body = "null"
		}
		payload := fmt.Sprintf(`{"credentialId":%q,"clientRequestId":%q,"kind":%q,"confirmName":%q,"body":%s}`, credential.CredentialID, fmt.Sprintf("failure-%d", index), test.kind, test.confirmation, body)
		if test.kind == "delete" {
			payload = fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"delete","kind":"delete","confirmName":"wrong"}`, credential.CredentialID)
		}
		expectErrorCode(t, fixture.request("POST", "/api/v1/machines/"+test.machine+"/actions", payload, cookie, csrf, fixture.dependencies.Config.Origin), test.code)
	}
	payload := fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"conflict","kind":"start","body":{}}`, credential.CredentialID)
	if fixture.request("POST", "/api/v1/machines/machine/actions", payload, cookie, csrf, fixture.dependencies.Config.Origin).Code != 202 {
		t.Fatal("initial action failed")
	}
	payload = fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"conflict","kind":"stop","body":{}}`, credential.CredentialID)
	expectErrorCode(t, fixture.request("POST", "/api/v1/machines/machine/actions", payload, cookie, csrf, fixture.dependencies.Config.Origin), "request_conflict")
	if _, err := fixture.dependencies.Accounts.SetEnabled(context.Background(), credential.CredentialID, false); err != nil {
		t.Fatal(err)
	}
	payload = fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"disabled","kind":"start","body":{}}`, credential.CredentialID)
	expectErrorCode(t, fixture.request("POST", "/api/v1/machines/machine/actions", payload, cookie, csrf, fixture.dependencies.Config.Origin), "credential_unavailable")
	payload = fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"disabled-create","body":{"name":"test"}}`, credential.CredentialID)
	expectErrorCode(t, fixture.request("POST", "/api/v1/machines", payload, cookie, csrf, fixture.dependencies.Config.Origin), "credential_unavailable")
}

func TestAccountFailureCodesReachHTTP(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{{status: 401, code: "unauthorized"}, {status: 403, code: "forbidden"}, {status: 429, code: "rate_limited"}, {status: 200, code: "invalid_identity"}} {
		t.Run(test.code, func(t *testing.T) {
			fixture := newFixture(t, false)
			cookie, csrf := fixture.login(t)
			credential := fixture.addAccount(t, cookie, csrf)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respond(w, test.status, map[string]any{"error": map[string]string{"message": "private upstream text"}})
			}))
			defer server.Close()
			fixture.dependencies.Accounts = account.New(fixture.store, fixture.ring, func(key string) (account.Client, error) {
				return instabox.NewClient(key, instabox.Options{BaseURL: server.URL})
			}, nil)
			var err error
			fixture.api, err = httpapi.New(fixture.dependencies)
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/api/v1/accounts", "/api/v1/accounts/" + credential.CredentialID + "/validate"} {
				expectErrorCode(t, fixture.request("POST", path, `{"name":"test","key":"fake-key"}`, cookie, csrf, fixture.dependencies.Config.Origin), test.code)
			}
		})
	}
}
