package integration_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"instadeck/internal/httpapi"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func setupRequest(fixture *fixture, method, path, origin, host string, body any, cookies []*http.Cookie, forwarded bool) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	request := httptest.NewRequest(method, "/api/v1/setup/"+path, bytes.NewReader(encoded))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = host
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if forwarded {
		request.Header.Set("X-Forwarded-For", "203.0.113.5")
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	fixture.api.ServeHTTP(response, request)
	return response
}

func enrollSetup(t *testing.T, fixture *fixture, origin, host, setupCode string, forwarded bool) (string, []*http.Cookie) {
	t.Helper()
	response := setupRequest(fixture, "POST", "start", origin, host, map[string]string{"username": "admin", "password": testPassword, "setupCode": setupCode}, nil, forwarded)
	if response.Code != 200 {
		t.Fatalf("start: %d %s", response.Code, response.Body)
	}
	var enrollment struct {
		Secret string `json:"secret"`
		URI    string `json:"otpauthUri"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.Secret == "" || !strings.HasPrefix(enrollment.URI, "otpauth://totp/") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("invalid enrollment")
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].MaxAge != 900 {
		t.Fatal("unsafe setup cookie")
	}
	return enrollment.Secret, cookies
}

func setupTOTP(t *testing.T, secret string, step int64) string {
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

func TestSetupStatusAndOriginBoundaries(t *testing.T) {
	fixture := newFixture(t, false, true)
	request := httptest.NewRequest("GET", "/api/v1/setup/status", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	fixture.api.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), `"codeRequired":false`) {
		t.Fatal("same-origin browser GET requires a setup code", response.Body)
	}
	for _, test := range []struct {
		origin, host            string
		forwarded, codeRequired bool
	}{
		{origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080"},
		{origin: "http://127.0.0.1:8080", host: "127.0.0.1:8080", forwarded: true, codeRequired: true},
		{origin: "https://panel.example", host: "panel.example", codeRequired: true},
		{host: "127.0.0.1:8080"},
	} {
		response := setupRequest(fixture, "GET", "status", test.origin, test.host, nil, nil, test.forwarded)
		var status struct{ Required, CodeRequired bool }
		_ = json.Unmarshal(response.Body.Bytes(), &status)
		if response.Code != 200 || !status.Required || status.CodeRequired != test.codeRequired {
			t.Fatalf("status: %s", response.Body)
		}
	}
	for _, test := range []struct{ origin, host, code string }{
		{origin: "http://public.example", host: "public.example", code: "https_required"},
		{origin: "https://evil.example", host: "panel.example", code: "origin_rejected"},
		{origin: "http://127.0.0.1:8080/path", host: "127.0.0.1:8080", code: "origin_rejected"},
		{host: "127.0.0.1:8080", code: "origin_rejected"},
	} {
		response := setupRequest(fixture, "POST", "start", test.origin, test.host, map[string]string{}, nil, false)
		if response.Code != 403 || !strings.Contains(response.Body.String(), test.code) {
			t.Fatalf("origin: %d %s", response.Code, response.Body)
		}
	}
}

func TestSetupPendingExpiryAttemptsAndValidation(t *testing.T) {
	fixture := newFixture(t, false, true)
	origin, host := "http://127.0.0.1:8080", "127.0.0.1:8080"
	for _, input := range []map[string]string{
		{"username": "admin", "password": "short"},
		{"username": "", "password": testPassword},
		{"username": strings.Repeat("用", 65), "password": testPassword},
	} {
		response := setupRequest(fixture, "POST", "start", origin, host, input, nil, false)
		if response.Code != 400 {
			t.Fatalf("invalid input accepted: %d %s", response.Code, response.Body)
		}
	}
	secret, cookies := enrollSetup(t, fixture, origin, host, "", false)
	response := setupRequest(fixture, "POST", "complete", origin, host, map[string]string{"code": setupTOTP(t, secret, fixture.clock.Load()/30)}, nil, false)
	if response.Code != 401 {
		t.Fatal("cookie binding missing")
	}
	for attempt := 0; attempt < 6; attempt++ {
		response = setupRequest(fixture, "POST", "complete", origin, host, map[string]string{"code": "invalid"}, cookies, false)
		expected := 401
		if attempt == 5 {
			expected = 429
		}
		if response.Code != expected {
			t.Fatalf("attempt %d: %d", attempt, response.Code)
		}
	}
	initialized, _ := fixture.store.HasAdministrator(context.Background())
	if initialized {
		t.Fatal("failed attempts created administrator")
	}
	secret, cookies = enrollSetup(t, fixture, origin, host, "", false)
	fixture.clock.Add(901)
	response = setupRequest(fixture, "POST", "complete", origin, host, map[string]string{"code": setupTOTP(t, secret, fixture.clock.Load()/30)}, cookies, false)
	if response.Code != 401 {
		t.Fatal("expired enrollment accepted")
	}
}

func TestSetupGlobalCodeLimit(t *testing.T) {
	fixture := newFixture(t, false, true)
	for attempt := 0; attempt < 31; attempt++ {
		request := httptest.NewRequest("POST", "/api/v1/setup/start", strings.NewReader(`{"username":"admin","password":"integration-password-123","setupCode":"wrong"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		request.Host = "127.0.0.1:8080"
		request.Header.Set("Origin", "http://127.0.0.1:8080")
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", attempt+1))
		response := httptest.NewRecorder()
		fixture.api.ServeHTTP(response, request)
		expected := 403
		if attempt == 30 {
			expected = 429
		}
		if response.Code != expected {
			t.Fatalf("global limit at %d: %d", attempt, response.Code)
		}
	}
}

func TestSetupCodeRateLimitAndNoDisclosure(t *testing.T) {
	fixture := newFixture(t, false, true)
	var startup bytes.Buffer
	fixture.dependencies.SetupOutput = &startup
	var err error
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	code := strings.Split(strings.Split(startup.String(), "：")[1], "（")[0]
	if len(code) < 20 {
		t.Fatal("short setup code")
	}
	for attempt := 0; attempt < 6; attempt++ {
		response := setupRequest(fixture, "POST", "start", "http://127.0.0.1:8080", "127.0.0.1:8080", map[string]string{"username": "admin", "password": testPassword, "setupCode": "incorrect"}, nil, true)
		expected := 403
		if attempt == 5 {
			expected = 429
		}
		if response.Code != expected || strings.Contains(response.Body.String(), code) {
			t.Fatalf("code failure %d: %d %s", attempt, response.Code, response.Body)
		}
	}
	if strings.Contains(fixture.logs.String(), code) {
		t.Fatal("setup code leaked to request logs")
	}
}

func TestSetupFlowPersistenceReplayAndConcurrency(t *testing.T) {
	fixture := newFixture(t, false, true)
	fixture.dependencies.Config.Origin = ""
	var startup bytes.Buffer
	fixture.dependencies.SetupOutput = &startup
	var err error
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	origin, host := "https://panel.example", "panel.example"
	code := strings.Split(strings.Split(startup.String(), "：")[1], "（")[0]
	for _, path := range []string{"/api/v1/accounts", "/api/v1/auth/password", "/api/v1/terminal/ws"} {
		method := "POST"
		if strings.HasSuffix(path, "ws") {
			method = "GET"
		}
		response := fixture.request(method, path, `{}`, nil, "", origin)
		if response.Code != 403 {
			t.Fatalf("uninitialized business %s: %d", path, response.Code)
		}
	}
	secret, cookies := enrollSetup(t, fixture, origin, host, code, true)
	initialized, _ := fixture.store.HasAdministrator(context.Background())
	if initialized {
		t.Fatal("administrator persisted before confirmation")
	}
	database, err := sql.Open("sqlite", filepath.Join(fixture.directory, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err = database.QueryRow("SELECT count(*) FROM administrator").Scan(&count); err != nil || count != 0 {
		t.Fatal("secret persisted", err)
	}
	invalid := setupRequest(fixture, "POST", "complete", origin, host, map[string]string{"code": "bad"}, cookies, true)
	if invalid.Code != 401 {
		t.Fatalf("bad totp: %d", invalid.Code)
	}
	initialized, _ = fixture.store.HasAdministrator(context.Background())
	if initialized {
		t.Fatal("invalid code created administrator")
	}
	totp := setupTOTP(t, secret, fixture.clock.Load()/30)
	responses := make(chan *httptest.ResponseRecorder, 2)
	var workers sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			responses <- setupRequest(fixture, "POST", "complete", origin, host, map[string]string{"code": totp}, cookies, true)
		}()
	}
	close(start)
	workers.Wait()
	close(responses)
	successes := 0
	var sessionResponse *httptest.ResponseRecorder
	for response := range responses {
		if response.Code == 200 {
			successes++
			sessionResponse = response
		} else if response.Code != 409 {
			t.Fatalf("concurrent complete: %d %s", response.Code, response.Body)
		}
	}
	if successes != 1 {
		t.Fatal("setup completed more than once")
	}
	var result struct {
		RecoveryCodes []string
		CSRFToken     string
	}
	_ = json.Unmarshal(sessionResponse.Body.Bytes(), &result)
	if len(result.RecoveryCodes) != 10 || result.CSRFToken == "" {
		t.Fatal("invalid setup response")
	}
	stored, err := fixture.store.PanelOrigin(context.Background())
	if err != nil || stored != origin {
		t.Fatal("origin not persisted", stored, err)
	}
	if strings.Contains(fixture.logs.String(), secret) || strings.Contains(fixture.logs.String(), code) {
		t.Fatal("secret leaked")
	}
	for _, path := range []string{"start", "complete"} {
		response := setupRequest(fixture, "POST", path, origin, host, map[string]string{}, cookies, true)
		if response.Code != 409 || !strings.Contains(response.Body.String(), "already_initialized") {
			t.Fatalf("setup overwrite: %d", response.Code)
		}
	}
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	sessionCookies := sessionResponse.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, cookie := range sessionCookies {
		if cookie.Name == "__Host-instadeck_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.Secure {
		t.Fatal("HTTPS cookie missing")
	}
	response := fixture.request("GET", "/api/v1/session", "", sessionCookie, "", origin)
	if response.Code != 200 || response.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("session after restart: %d", response.Code)
	}
	challenge := fixture.request("POST", "/api/v1/auth/password", `{"username":"admin","password":"`+testPassword+`"}`, nil, "", origin)
	if challenge.Code != 200 {
		t.Fatalf("old routes unavailable: %d", challenge.Code)
	}
	replay := fixture.request("POST", "/api/v1/auth/totp", `{"code":"`+totp+`"}`, challenge.Result().Cookies()[0], "", origin)
	if replay.Code != 401 {
		t.Fatal("setup TOTP replay accepted")
	}
	rejected := fixture.request("POST", "/api/v1/auth/logout", `{}`, sessionCookie, "", origin)
	if rejected.Code != 403 {
		t.Fatal("CSRF missing accepted")
	}
	logout := fixture.request("POST", "/api/v1/auth/logout", `{}`, sessionCookie, result.CSRFToken, origin)
	if logout.Code != 200 {
		t.Fatalf("logout after restart: %d %s", logout.Code, logout.Body)
	}
}
