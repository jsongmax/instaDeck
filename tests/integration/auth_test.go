package integration_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"instadeck/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func totpCode(step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	digest := hmac.New(sha1.New, []byte("12345678901234567890"))
	_, _ = digest.Write(counter[:])
	sum := digest.Sum(nil)
	offset := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}

func TestEveryRegisteredBusinessRouteRequiresSession(t *testing.T) {
	fixture := newFixture(t, false)
	public := map[string]bool{"GET /api/v1/healthz": true, "POST /api/v1/auth/password": true, "POST /api/v1/auth/totp": true, "POST /api/v1/auth/recovery": true}
	count := 0
	err := chi.Walk(fixture.api.Routes(), func(method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		if public[key] {
			return nil
		}
		count++
		path := strings.ReplaceAll(route, "{id}", "test")
		response := fixture.request(method, path, `{}`, nil, "", fixture.dependencies.Config.Origin)
		if response.Code != 401 {
			t.Errorf("%s: expected 401, got %d: %s", key, response.Code, response.Body)
		}
		return nil
	})
	if err != nil || count < 15 {
		t.Fatal("route enumeration failed", count, err)
	}
}

func TestLoginOriginCSRFSessionLogoutAndSecretBoundaries(t *testing.T) {
	fixture := newFixture(t, false)
	origin := fixture.dependencies.Config.Origin
	for _, supplied := range []string{"", "https://evil.invalid", origin + "/", strings.ToUpper(origin)} {
		response := fixture.request("POST", "/api/v1/auth/password", `{}`, nil, "", supplied)
		if response.Code != 403 {
			t.Fatal("origin accepted", supplied, response.Code)
		}
	}
	password := fixture.request("POST", "/api/v1/auth/password", `{"username":"admin","password":"`+testPassword+`"}`, nil, "", origin)
	if password.Code != 200 {
		t.Fatal(password.Body)
	}
	challenge := password.Result().Cookies()[0]
	if response := fixture.request("GET", "/api/v1/session", "", challenge, "", ""); response.Code != 401 {
		t.Fatal("challenge authorized")
	}
	forged := *challenge
	forged.Name = "instadeck_session"
	if response := fixture.request("GET", "/api/v1/session", "", &forged, "", ""); response.Code != 401 {
		t.Fatal("challenge promoted to session")
	}
	response := fixture.request("POST", "/api/v1/auth/totp", `{"code":"`+totpCode(fixture.clock.Load()/30)+`"}`, challenge, "", origin)
	if response.Code != 200 {
		t.Fatal(response.Body)
	}
	var cookie *http.Cookie
	for _, candidate := range response.Result().Cookies() {
		if candidate.Name == "instadeck_session" {
			cookie = candidate
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.Secure || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("development cookie attributes")
	}
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &session)
	for _, csrf := range []string{"", "wrong"} {
		if fixture.request("POST", "/api/v1/accounts", `{}`, cookie, csrf, origin).Code != 403 {
			t.Fatal("csrf accepted")
		}
	}
	value := fixture.addAccount(t, cookie, session.CSRF)
	for _, path := range []string{"/accounts", "/fleet", "/machines/machine?credentialId=" + value.CredentialID, "/templates?credentialId=" + value.CredentialID, "/audit-events"} {
		response := fixture.request("GET", "/api/v1"+path, "", cookie, "", "")
		if response.Code != 200 {
			t.Fatal(path, response.Code, response.Body)
		}
		for _, forbidden := range []string{testKey, "ciphertext", "certificate", "private-organization", "costUSD", "idempotency"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatal("response leak", path, forbidden)
			}
		}
	}
	for _, file := range []string{"panel.db", "panel.db-wal"} {
		raw, err := os.ReadFile(filepath.Join(fixture.directory, file))
		if err == nil && strings.Contains(string(raw), testKey) {
			t.Fatal("plaintext key in database")
		}
	}
	fixture.clock.Add(301)
	for _, request := range []struct{ method, path, body string }{
		{method: "POST", path: "/accounts", body: `{}`}, {method: "PATCH", path: "/accounts/" + value.CredentialID, body: `{}`}, {method: "DELETE", path: "/accounts/" + value.CredentialID}, {method: "POST", path: "/machines/machine/actions", body: `{"clientRequestId":"delete-1","credentialId":"` + value.CredentialID + `","kind":"delete","confirmName":"test-machine"}`},
	} {
		response := fixture.request(request.method, "/api/v1"+request.path, request.body, cookie, session.CSRF, origin)
		if response.Code != 403 || !strings.Contains(response.Body.String(), "reauthentication_required") {
			t.Fatal("recent authentication missing", response.Code, response.Body)
		}
	}
	fixture.failUpstream.Store(true)
	fixture.dependencies.Fleet.Invalidate(value.CredentialID)
	response = fixture.request("GET", "/api/v1/fleet", "", cookie, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"partial":true`) || !strings.Contains(response.Body.String(), `"error":{`) {
		t.Fatal("all accounts failed contract", response.Code, response.Body)
	}
	response = fixture.request("POST", "/api/v1/auth/logout", `{}`, cookie, session.CSRF, origin)
	if response.Code != 200 || fixture.request("GET", "/api/v1/session", "", cookie, "", "").Code != 401 {
		t.Fatal("logout failed")
	}
	if strings.Contains(fixture.logs.String(), testKey) || strings.Contains(fixture.logs.String(), cookie.Value) {
		t.Fatal("secret in logs")
	}
}

func TestSecureCookieHeadersSPAAndAPIFallback(t *testing.T) {
	fixture := newFixture(t, false)
	fixture.dependencies.Config.Origin = "https://panel.example.invalid"
	var err error
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	cookie, _ := fixture.login(t)
	if !strings.HasPrefix(cookie.Name, "__Host-") || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("secure cookie attributes")
	}
	response := fixture.request("GET", "/deep/link", "", nil, "", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "test SPA") {
		t.Fatal("SPA fallback")
	}
	for _, header := range []string{"Content-Security-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy"} {
		if response.Header().Get(header) == "" {
			t.Fatal("missing header", header)
		}
	}
	for _, path := range []string{"/api", "/api/missing", "/api/v1/missing"} {
		response = fixture.request("GET", path, "", nil, "", "")
		if response.Code != 404 || !strings.Contains(response.Body.String(), `"error":`) {
			t.Fatal("API fell back", path)
		}
	}
	fixture.dependencies.Assets = nil
	fixture.api, _ = httpapi.New(fixture.dependencies)
	if response = fixture.request("GET", "/", "", nil, "", ""); response.Code != 503 || !strings.Contains(response.Body.String(), "frontend_not_built") {
		t.Fatal("missing frontend not reported")
	}
}

func TestForwardedSpoofCannotBypassLoginLimit(t *testing.T) {
	for _, trust := range []bool{false, true} {
		fixture := newFixture(t, trust)
		for index := 0; index < 3; index++ {
			request := httptest.NewRequest("POST", "/api/v1/auth/password", strings.NewReader(`{"username":"admin","password":"`+testPassword+`"}`))
			request.RemoteAddr = "127.0.0.1:5555"
			request.Header.Set("Origin", fixture.dependencies.Config.Origin)
			request.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d, 203.0.113.10", index))
			response := httptest.NewRecorder()
			fixture.api.ServeHTTP(response, request)
			expected := 200
			if index == 2 {
				expected = 401
			}
			if response.Code != expected {
				t.Fatal("spoof bypassed limiter", trust, index, response.Code)
			}
		}
	}
}
