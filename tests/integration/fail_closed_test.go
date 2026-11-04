package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"instadeck/internal/audit"
	"instadeck/internal/httpapi"
	"instadeck/internal/store/sqlite"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingAudit struct{ store *sqlite.Store }

func (auditStore failingAudit) WriteEvent(context.Context, audit.Event) error {
	return errors.New("private audit failure")
}

func (auditStore failingAudit) ReadAuditEvents(ctx context.Context, after int64, limit int) ([]sqlite.AuditRecord, error) {
	return auditStore.store.ReadAuditEvents(ctx, after, limit)
}

func TestHighRiskChangesFailClosedWithoutAudit(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	fixture.dependencies.Audit = failingAudit{store: fixture.store}
	var err error
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	response := fixture.request("DELETE", "/api/v1/accounts/"+credential.CredentialID, "", cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "audit_unavailable") || strings.Contains(response.Body.String(), "private audit failure") {
		t.Fatal("audit failure exposed or ignored", response.Code, response.Body)
	}
	metadata, err := fixture.dependencies.Accounts.List(context.Background())
	if err != nil || len(metadata) != 1 {
		t.Fatal("credential changed without audit")
	}
	path, _ := filepath.Abs(filepath.Join(fixture.directory, "panel.db"))
	location := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	database, err := sql.Open("sqlite", location.String())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.Exec(`CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	response = fixture.request("POST", "/api/v1/machines/machine/actions", `{"clientRequestId":"reject-without-audit","credentialId":"`+credential.CredentialID+`","kind":"stop","body":{}}`, cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 503 {
		t.Fatal("operation accepted without audit", response.Code, response.Body)
	}
	if _, found, err := fixture.store.FindAction(context.Background(), "reject-without-audit"); err != nil || found {
		t.Fatal("unaudited action persisted")
	}
}

func TestRecoveryCodeIsSingleUseAndBodiesAreBounded(t *testing.T) {
	fixture := newFixture(t, false)
	origin := fixture.dependencies.Config.Origin
	passwordBody := `{"username":"admin","password":"` + testPassword + `"}`
	challengeResponse := fixture.request("POST", "/api/v1/auth/password", passwordBody, nil, "", origin)
	challenge := challengeResponse.Result().Cookies()[0]
	response := fixture.request("POST", "/api/v1/auth/recovery", `{"code":"`+fixture.recovery[0]+`"}`, challenge, "", origin)
	if response.Code != 200 {
		t.Fatal("recovery failed", response.Body)
	}
	challengeResponse = fixture.request("POST", "/api/v1/auth/password", passwordBody, nil, "", origin)
	challenge = challengeResponse.Result().Cookies()[0]
	response = fixture.request("POST", "/api/v1/auth/recovery", `{"code":"`+fixture.recovery[0]+`"}`, challenge, "", origin)
	if response.Code != 401 {
		t.Fatal("recovery replay accepted")
	}
	for _, payload := range []string{`{"username":"admin","password":"` + strings.Repeat("x", 1025) + `"}`, `{"code":"` + strings.Repeat("x", 65) + `"}`, strings.Repeat("x", 4097)} {
		response = fixture.request("POST", "/api/v1/auth/password", payload, nil, "", origin)
		if response.Code != 400 {
			t.Fatal("oversized authentication body accepted", response.Code)
		}
	}
}

func TestSessionAbsoluteDeadlinePreventsTerminalUpgrade(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
	fixture.dependencies.AbsoluteSessionTTL = time.Nanosecond
	var err error
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fixture.api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := dialTerminal(ctx, server, cookie, fixture.dependencies.Config.Origin, ticket)
	if connection != nil {
		_ = connection.CloseNow()
		t.Fatal("expired session deadline upgraded")
	}
	if err == nil {
		t.Fatal("expired session deadline accepted")
	}
}

func TestCredentialChangesRevokeTickets(t *testing.T) {
	for _, change := range []string{"replace", "disable", "delete"} {
		t.Run(change, func(t *testing.T) {
			fixture := newFixture(t, false)
			cookie, csrf := fixture.login(t)
			credential := fixture.addAccount(t, cookie, csrf)
			ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
			method, body := "PATCH", `{"key":"replacement-private-key"}`
			if change == "disable" {
				body = `{"enabled":false}`
			}
			if change == "delete" {
				method, body = "DELETE", ""
			}
			response := fixture.request(method, "/api/v1/accounts/"+credential.CredentialID, body, cookie, csrf, fixture.dependencies.Config.Origin)
			if response.Code != 200 {
				t.Fatal("credential change failed", response.Body)
			}
			server := httptest.NewServer(fixture.api)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			connection, responseHTTP, err := dialTerminal(ctx, server, cookie, fixture.dependencies.Config.Origin, ticket)
			if connection != nil {
				_ = connection.CloseNow()
			}
			if err == nil || responseHTTP.StatusCode != 403 {
				t.Fatal("revoked credential ticket accepted")
			}
			_ = responseHTTP.Body.Close()
		})
	}
}

func TestMalformedHandshakeErrorIsJSONAndContainsNoHeaders(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
	request := httptest.NewRequest("GET", "/api/v1/terminal/ws", nil)
	request.AddCookie(cookie)
	request.Header.Set("Origin", fixture.dependencies.Config.Origin)
	request.Header.Set("Sec-WebSocket-Protocol", "instadeck.terminal.v1, instadeck.ticket."+ticket)
	request.Header.Set("Connection", "private-header-value")
	response := httptest.NewRecorder()
	fixture.api.ServeHTTP(response, request)
	if response.Code != 403 || response.Header().Get("Content-Type") != "application/json" || strings.Contains(response.Body.String(), "private-header-value") || !strings.Contains(response.Body.String(), `"requestId":`) {
		t.Fatal("unsafe handshake error", response.Code, response.Body)
	}
}
