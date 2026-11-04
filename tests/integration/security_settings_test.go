package integration_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"instadeck/internal/httpapi"
	"instadeck/internal/identity"
	"instadeck/internal/terminal"
)

type securitySession struct {
	CSRF          string   `json:"csrfToken"`
	RecoveryCodes []string `json:"recoveryCodes"`
}

func rotatedSecuritySession(t *testing.T, response *httptest.ResponseRecorder) (*http.Cookie, securitySession) {
	t.Helper()
	var session securitySession
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil || session.CSRF == "" {
		t.Fatalf("rotation failed: %d %s", response.Code, response.Body)
	}
	for _, cookie := range response.Result().Cookies() {
		if strings.Contains(cookie.Name, "session") {
			return cookie, session
		}
	}
	t.Fatal("replacement cookie missing")
	return nil, session
}

func securityRequest(fixture *fixture, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	return fixture.request("POST", "/api/v1"+path, string(encoded), cookie, csrf, fixture.dependencies.Config.Origin)
}

func loginWithCredentials(fixture *fixture, password, code string, recovery bool) *httptest.ResponseRecorder {
	fixture.clock.Add(61)
	challenge := securityRequest(fixture, "/auth/password", map[string]string{"username": "admin", "password": password}, nil, "")
	if challenge.Code != 200 {
		return challenge
	}
	path := "/auth/totp"
	if recovery {
		path = "/auth/recovery"
	}
	return securityRequest(fixture, path, map[string]string{"code": code}, challenge.Result().Cookies()[0], "")
}

func codeForSecret(t *testing.T, secret string, step int64) string {
	t.Helper()
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	digest := hmac.New(sha1.New, decoded)
	_, _ = digest.Write(counter[:])
	sum := digest.Sum(nil)
	offset := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}

func startAuthenticator(t *testing.T, fixture *fixture, cookie *http.Cookie, csrf string) string {
	t.Helper()
	fixture.clock.Add(61)
	response := securityRequest(fixture, "/security/totp/start", map[string]string{"password": testPassword, "code": totpCode(fixture.clock.Load() / 30)}, cookie, csrf)
	var pending struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauthUri"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &pending) != nil || pending.Secret == "" || !strings.Contains(pending.URI, pending.Secret) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start failed: %d %s", response.Code, response.Body)
	}
	return pending.Secret
}

func assertNoStoredSecrets(t *testing.T, fixture *fixture, secrets ...string) {
	t.Helper()
	for _, filename := range []string{"panel.db", "panel.db-wal", "panel.db-shm"} {
		raw, err := os.ReadFile(filepath.Join(fixture.directory, filename))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, secret := range secrets {
			if secret != "" && (strings.Contains(string(raw), secret) || strings.Contains(fixture.logs.String(), secret)) {
				t.Fatal("secret persisted or logged", filename)
			}
		}
	}
}

func TestSecurityPasswordValidationAndLimiter(t *testing.T) {
	for _, test := range []struct{ name, password, replacement, code string }{
		{name: "weak", password: testPassword, replacement: "short", code: "weak_password"},
		{name: "too-long", password: testPassword, replacement: strings.Repeat("x", 1025), code: "weak_password"},
		{name: "same", password: testPassword, replacement: testPassword, code: "password_unchanged"},
		{name: "wrong", password: "incorrect-password", replacement: "new-password-123456", code: "authentication_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixture(t, false)
			cookie, csrf := fixture.login(t)
			fixture.clock.Add(61)
			response := securityRequest(fixture, "/security/password", map[string]string{"currentPassword": test.password, "newPassword": test.replacement}, cookie, csrf)
			if !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatal(response.Code, response.Body)
			}
			if fixture.request("GET", "/api/v1/session", "", cookie, "", "").Code != 200 {
				t.Fatal("failed update revoked session")
			}
			if test.name == "wrong" {
				response = securityRequest(fixture, "/security/password", map[string]string{"currentPassword": "wrong-again", "newPassword": "new-password-123456"}, cookie, csrf)
				response = securityRequest(fixture, "/auth/password", map[string]string{"username": "admin", "password": testPassword}, nil, "")
				if response.Code != 401 {
					t.Fatal("password errors did not consume login limiter")
				}
			}
		})
	}
}

func TestSecurityPasswordRotationAndTerminalLifecycle(t *testing.T) {
	fixture := newFixture(t, false)
	fakeSSH(t, fixture)
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	fixture.clock.Add(61)
	otherCookie, otherCSRF := fixture.login(t)
	server := httptest.NewServer(fixture.api)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	openTerminal := func(cookie *http.Cookie, csrf string) *websocket.Conn {
		ticket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
		connection, _, err := dialTerminal(ctx, server, cookie, fixture.dependencies.Config.Origin, ticket)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.CloseNow() })
		_, ready, err := connection.Read(ctx)
		if err != nil || !strings.Contains(string(ready), `"type":"ready"`) {
			t.Fatal("terminal not ready", err)
		}
		return connection
	}
	currentTerminal := openTerminal(cookie, csrf)
	otherTerminal := openTerminal(otherCookie, otherCSRF)
	currentTicket := terminalTicket(t, fixture, cookie, csrf, credential.CredentialID)
	otherTicket := terminalTicket(t, fixture, otherCookie, otherCSRF, credential.CredentialID)
	fixture.clock.Add(61)
	response := securityRequest(fixture, "/security/password", map[string]string{"currentPassword": testPassword, "newPassword": "replacement-password-123"}, cookie, csrf)
	replacement, session := rotatedSecuritySession(t, response)
	for _, previous := range []*http.Cookie{cookie, otherCookie} {
		if fixture.request("GET", "/api/v1/session", "", previous, "", "").Code != 401 {
			t.Fatal("old session still valid")
		}
	}
	if _, _, err := otherTerminal.Read(ctx); err == nil {
		t.Fatal("revoked terminal survived")
	}
	if err := currentTerminal.Write(ctx, websocket.MessageBinary, []byte("still-live")); err != nil {
		t.Fatal(err)
	}
	_, echo, err := currentTerminal.Read(ctx)
	if err != nil || string(echo) != "still-live" {
		t.Fatal("current terminal was not migrated", err)
	}
	migrated, _, err := dialTerminal(ctx, server, replacement, fixture.dependencies.Config.Origin, currentTicket)
	if err != nil {
		t.Fatal("current ticket not migrated", err)
	}
	_ = migrated.CloseNow()
	if err := fixture.dependencies.Terminal.ServeUpgrade(ctx, terminal.Ticket(otherTicket), identity.TokenHash(otherCookie.Value), func(terminal.Binding) (terminal.FrameConn, error) {
		t.Error("revoked ticket reached upgrade")
		return nil, terminal.InvalidTicket
	}); err != terminal.InvalidTicket {
		t.Fatal("other ticket survived")
	}
	if fixture.request("POST", "/api/v1/auth/logout", "{}", replacement, session.CSRF, fixture.dependencies.Config.Origin).Code != 200 {
		t.Fatal("replacement session unusable")
	}
	if _, _, err := currentTerminal.Read(ctx); err == nil {
		t.Fatal("migrated terminal survived logout")
	}
	if response = loginWithCredentials(fixture, testPassword, fixture.recovery[0], true); response.Code != 401 {
		t.Fatal("old password accepted")
	}
	if response = loginWithCredentials(fixture, "replacement-password-123", fixture.recovery[0], true); response.Code != 200 {
		t.Fatal("new password rejected", response.Body)
	}
	assertNoStoredSecrets(t, fixture, replacement.Value)
}

func TestSecurityAuthenticatorReplacement(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	secret := startAuthenticator(t, fixture, cookie, csrf)
	assertNoStoredSecrets(t, fixture, secret)
	decoded, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	assertNoStoredSecrets(t, fixture, string(decoded))
	newCode := codeForSecret(t, secret, (fixture.clock.Load()+61)/30)
	if response := loginWithCredentials(fixture, testPassword, newCode, false); response.Code != 401 {
		t.Fatal("pending authenticator logged in")
	}
	oldCode := totpCode((fixture.clock.Load() + 61) / 30)
	if response := loginWithCredentials(fixture, testPassword, oldCode, false); response.Code != 200 {
		t.Fatal("old authenticator stopped before confirm", response.Body)
	}
	confirmCode := codeForSecret(t, secret, fixture.clock.Load()/30)
	response := securityRequest(fixture, "/security/totp/confirm", map[string]string{"code": confirmCode}, cookie, csrf)
	replacement, session := rotatedSecuritySession(t, response)
	if len(session.RecoveryCodes) != 10 {
		t.Fatal("new recovery codes missing")
	}
	if fixture.request("GET", "/api/v1/session", "", cookie, "", "").Code != 401 {
		t.Fatal("old token survived confirmation")
	}
	challenge := securityRequest(fixture, "/auth/password", map[string]string{"username": "admin", "password": testPassword}, nil, "")
	if challenge.Code != 200 {
		t.Fatal(challenge.Body)
	}
	replay := securityRequest(fixture, "/auth/totp", map[string]string{"code": confirmCode}, challenge.Result().Cookies()[0], "")
	if replay.Code != 401 {
		t.Fatal("confirmation code replay accepted")
	}
	if response = loginWithCredentials(fixture, testPassword, fixture.recovery[0], true); response.Code != 401 {
		t.Fatal("old recovery code accepted")
	}
	if response = loginWithCredentials(fixture, testPassword, totpCode((fixture.clock.Load()+61)/30), false); response.Code != 401 {
		t.Fatal("old authenticator accepted")
	}
	if response = loginWithCredentials(fixture, testPassword, codeForSecret(t, secret, (fixture.clock.Load()+61)/30), false); response.Code != 200 {
		t.Fatal("new authenticator rejected", response.Body)
	}
	if response = loginWithCredentials(fixture, testPassword, session.RecoveryCodes[0], true); response.Code != 200 {
		t.Fatal("new recovery code rejected", response.Body)
	}
	for _, path := range []string{"/security", "/sessions", "/audit-events"} {
		response = fixture.request("GET", "/api/v1"+path, "", replacement, "", "")
		if response.Code != 200 {
			t.Fatal(response.Body)
		}
		for _, forbidden := range append([]string{secret, confirmCode, replacement.Value}, session.RecoveryCodes...) {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatal("secret returned by read endpoint", path)
			}
		}
	}
	assertNoStoredSecrets(t, fixture, append([]string{secret, string(decoded)}, session.RecoveryCodes...)...)
}

func TestSecurityPendingAuthenticatorBoundaries(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	fixture.clock.Add(61)
	otherCookie, otherCSRF := fixture.login(t)
	secret := startAuthenticator(t, fixture, cookie, csrf)
	response := securityRequest(fixture, "/security/totp/confirm", map[string]string{"code": codeForSecret(t, secret, fixture.clock.Load()/30)}, otherCookie, otherCSRF)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "totp_pending_missing") {
		t.Fatal("cross-session confirmation accepted", response.Body)
	}
	response = securityRequest(fixture, "/security/totp/confirm", map[string]string{"code": "wrong"}, cookie, csrf)
	if response.Code != 401 {
		t.Fatal("incorrect confirmation accepted")
	}
	fixture.clock.Add(601)
	response = securityRequest(fixture, "/security/totp/confirm", map[string]string{"code": codeForSecret(t, secret, fixture.clock.Load()/30)}, cookie, csrf)
	if !strings.Contains(response.Body.String(), "totp_pending_missing") {
		t.Fatal("expired confirmation accepted", response.Body)
	}
	if response = loginWithCredentials(fixture, testPassword, totpCode((fixture.clock.Load()+61)/30), false); response.Code != 200 {
		t.Fatal("failed confirmation altered old authenticator")
	}
}

func TestSecurityRecoveryReplacementAndRemainingCount(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	fixture.clock.Add(61)
	response := securityRequest(fixture, "/security/recovery-codes", map[string]string{"password": testPassword, "code": totpCode(fixture.clock.Load() / 30)}, cookie, csrf)
	var generated securitySession
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &generated) != nil || len(generated.RecoveryCodes) != 10 {
		t.Fatal("generation failed", response.Body)
	}
	if response = loginWithCredentials(fixture, testPassword, fixture.recovery[0], true); response.Code != 401 {
		t.Fatal("old recovery code accepted")
	}
	if response = loginWithCredentials(fixture, testPassword, generated.RecoveryCodes[0], true); response.Code != 200 {
		t.Fatal("new recovery code rejected", response.Body)
	}
	response = fixture.request("GET", "/api/v1/security", "", cookie, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"recoveryCodesLeft":9`) || strings.Contains(response.Body.String(), `"recoveryCodes":`) {
		t.Fatal("remaining count or secret projection wrong", response.Body)
	}
	assertNoStoredSecrets(t, fixture, generated.RecoveryCodes...)
}

func TestSecuritySecondFactorFailureBudget(t *testing.T) {
	for _, mode := range []string{"start", "confirm", "recovery"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newFixture(t, false)
			cookie, csrf := fixture.login(t)
			if mode == "confirm" {
				startAuthenticator(t, fixture, cookie, csrf)
			}
			for attempt := 0; attempt < 10; attempt++ {
				fixture.clock.Add(61)
				path := "/security/totp/" + mode
				body := map[string]string{"password": testPassword, "code": "invalid"}
				if mode == "confirm" {
					delete(body, "password")
				}
				if mode == "recovery" {
					path = "/security/recovery-codes"
				}
				response := securityRequest(fixture, path, body, cookie, csrf)
				if response.Code != 401 && !(mode == "confirm" && response.Code == 400 && strings.Contains(response.Body.String(), "totp_pending_missing")) {
					t.Fatal("invalid second factor accepted", response.Body)
				}
			}
			response := loginWithCredentials(fixture, testPassword, fixture.recovery[0], true)
			if response.Code != 401 {
				t.Fatal("settings failures did not exhaust shared budget")
			}
		})
	}
}

func TestSecuritySessionsAndRecentAuthentication(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	fixture.clock.Add(61)
	otherCookie, _ := fixture.login(t)
	response := fixture.request("GET", "/api/v1/sessions", "", cookie, "", "")
	var listed struct {
		Sessions []struct {
			PublicID string `json:"publicId"`
			Current  bool   `json:"current"`
			Device   string `json:"device"`
			Address  string `json:"sourceAddress"`
		} `json:"sessions"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &listed) != nil || len(listed.Sessions) != 2 {
		t.Fatal(response.Body)
	}
	currentID, otherID := "", ""
	for _, session := range listed.Sessions {
		if len(session.PublicID) != 43 || session.Device != "未知设备" || session.Address != "203.0.113.10" {
			t.Fatal("invalid public session metadata")
		}
		if session.Current {
			currentID = session.PublicID
		} else {
			otherID = session.PublicID
		}
	}
	for _, forbidden := range []string{cookie.Value, otherCookie.Value, "csrf", "hash", "secret"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatal("session secret leak")
		}
	}
	response = fixture.request("DELETE", "/api/v1/sessions/"+currentID, "", cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "cannot_revoke_current") {
		t.Fatal(response.Body)
	}
	fixture.clock.Add(301)
	for _, test := range []struct{ method, path string }{{method: "DELETE", path: "/sessions/" + otherID}, {method: "POST", path: "/sessions/revoke-others"}} {
		response = fixture.request(test.method, "/api/v1"+test.path, "{}", cookie, csrf, fixture.dependencies.Config.Origin)
		if response.Code != 403 || !strings.Contains(response.Body.String(), "reauthentication_required") {
			t.Fatal("revocation needs recent authentication", response.Body)
		}
	}
	fixture.clock.Add(61)
	response = securityRequest(fixture, "/auth/reauthenticate", map[string]string{"password": testPassword, "code": totpCode(fixture.clock.Load() / 30)}, cookie, csrf)
	cookie, session := rotatedSecuritySession(t, response)
	response = fixture.request("DELETE", "/api/v1/sessions/"+otherID, "", cookie, session.CSRF, fixture.dependencies.Config.Origin)
	if response.Code != 200 || fixture.request("GET", "/api/v1/session", "", otherCookie, "", "").Code != 401 {
		t.Fatal("other session survived revocation", response.Body)
	}
}

func TestSecuritySettingsRequireAudit(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	secret := startAuthenticator(t, fixture, cookie, csrf)
	before, err := fixture.store.AdministratorByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	fixture.dependencies.Audit = failingAudit{store: fixture.store}
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		body map[string]string
	}{
		{path: "/security/password", body: map[string]string{"currentPassword": testPassword, "newPassword": "new-password-123456"}},
		{path: "/security/totp/start", body: map[string]string{"password": testPassword, "code": totpCode(fixture.clock.Load() / 30)}},
		{path: "/security/totp/confirm", body: map[string]string{"code": codeForSecret(t, secret, fixture.clock.Load()/30)}},
		{path: "/security/recovery-codes", body: map[string]string{"password": testPassword, "code": totpCode(fixture.clock.Load() / 30)}},
	} {
		fixture.clock.Add(61)
		response := securityRequest(fixture, test.path, test.body, cookie, csrf)
		if response.Code != 503 || !strings.Contains(response.Body.String(), "audit_unavailable") {
			t.Fatal(test.path, response.Code, response.Body)
		}
	}
	after, err := fixture.store.AdministratorByName(context.Background(), "admin")
	if err != nil || after.Revision != before.Revision || string(after.Password.Digest) != string(before.Password.Digest) || string(after.TOTP.Ciphertext) != string(before.TOTP.Ciphertext) {
		t.Fatal("credentials changed without audit")
	}
}

func TestSecurityMutationsRequireOriginAndCSRF(t *testing.T) {
	fixture := newFixture(t, false)
	cookie, csrf := fixture.login(t)
	for _, test := range []struct{ method, path string }{
		{method: "POST", path: "/security/password"}, {method: "POST", path: "/security/totp/start"},
		{method: "POST", path: "/security/totp/confirm"}, {method: "POST", path: "/security/recovery-codes"},
		{method: "DELETE", path: "/sessions/other"}, {method: "POST", path: "/sessions/revoke-others"},
	} {
		if response := fixture.request(test.method, "/api/v1"+test.path, "{}", cookie, csrf, "https://evil.invalid"); response.Code != 403 || !strings.Contains(response.Body.String(), "origin_rejected") {
			t.Fatal(test.path, response.Code, response.Body)
		}
		if response := fixture.request(test.method, "/api/v1"+test.path, "{}", cookie, "wrong", fixture.dependencies.Config.Origin); response.Code != 403 || !strings.Contains(response.Body.String(), "csrf_rejected") {
			t.Fatal(test.path, response.Code, response.Body)
		}
	}
}

func TestLoginStoresOnlyCoarseDeviceMetadata(t *testing.T) {
	fixture := newFixture(t, false)
	challenge := securityRequest(fixture, "/auth/password", map[string]string{"username": "admin", "password": testPassword}, nil, "")
	if challenge.Code != 200 {
		t.Fatal(challenge.Body)
	}
	request := httptest.NewRequest("POST", "/api/v1/auth/totp", strings.NewReader(`{"code":"`+totpCode(fixture.clock.Load()/30)+`"}`))
	request.RemoteAddr = "203.0.113.40:5555"
	request.Header.Set("Origin", fixture.dependencies.Config.Origin)
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0) Chrome/120.0 private-device-marker")
	request.AddCookie(challenge.Result().Cookies()[0])
	response := httptest.NewRecorder()
	fixture.api.ServeHTTP(response, request)
	cookie, _ := rotatedSecuritySession(t, response)
	response = fixture.request("GET", "/api/v1/sessions", "", cookie, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Chrome · Windows") || !strings.Contains(response.Body.String(), "203.0.113.40") || strings.Contains(response.Body.String(), "private-device-marker") {
		t.Fatal(response.Body)
	}
	assertNoStoredSecrets(t, fixture, "private-device-marker")
}
