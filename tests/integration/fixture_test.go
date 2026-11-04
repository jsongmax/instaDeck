package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"instadeck/internal/account"
	"instadeck/internal/config"
	"instadeck/internal/fleet"
	"instadeck/internal/httpapi"
	"instadeck/internal/identity"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"instadeck/internal/operation"
	"instadeck/internal/store/sqlite"
	"instadeck/internal/terminal"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

const testPassword = "integration-password-123"
const testKey = "private-api-key-must-never-escape"

type safeBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (buffer *safeBuffer) Write(value []byte) (int, error) {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.Write(value)
}

func (buffer *safeBuffer) String() string {
	buffer.mutex.Lock()
	defer buffer.mutex.Unlock()
	return buffer.buffer.String()
}

type fixture struct {
	api          *httpapi.API
	dependencies httpapi.Dependencies
	store        *sqlite.Store
	ring         *keyring.Ring
	clock        atomic.Int64
	directory    string
	logs         safeBuffer
	upstream     *httptest.Server
	failUpstream atomic.Bool
	connection   func(http.ResponseWriter, *http.Request)
	poll         func(http.ResponseWriter, *http.Request)
	recovery     []string
}

func newFixture(t *testing.T, trust bool, uninitialized ...bool) *fixture {
	t.Helper()
	directory := t.TempDir()
	fixture := &fixture{directory: directory}
	key, err := keyring.GenerateKey(1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(keyring.File{Keys: []keyring.Key{key}})
	keyPath := filepath.Join(directory, "master.key")
	if err = os.WriteFile(keyPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	fixture.ring, err = keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store, err = sqlite.Open(context.Background(), filepath.Join(directory, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.store.Close() })
	fixture.clock.Store(time.Now().Truncate(30 * time.Second).Unix())
	now := func() time.Time { return time.Unix(fixture.clock.Load(), 0).UTC() }
	identities, err := identity.New(fixture.store, fixture.ring, now, identity.Options{ChallengeTTL: 5 * time.Minute, IdleTTL: 30 * time.Minute, AbsoluteTTL: 8 * time.Hour, RecentTTL: 5 * time.Minute, ChallengeAttempts: 5}, identity.NewLimiter(time.Minute, 2, 100))
	if err != nil {
		t.Fatal(err)
	}
	if len(uninitialized) == 0 || !uninitialized[0] {
		fixture.recovery, err = identities.Initialize(context.Background(), "admin-id", "admin", testPassword, []byte("12345678901234567890"))
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture.upstream = httptest.NewServer(http.HandlerFunc(fixture.upstreamHTTP))
	t.Cleanup(fixture.upstream.Close)
	accounts := account.New(fixture.store, fixture.ring, func(key string) (account.Client, error) {
		return instabox.NewClient(key, instabox.Options{BaseURL: fixture.upstream.URL})
	}, nil)
	fleets, err := fleet.New(accounts, fleet.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := operation.New(fixture.store, accounts, fleets, fleets, operation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	terminals, err := terminal.New(httpapi.TerminalUpstream{Accounts: accounts}, terminal.Options{AllowLoopbackForTests: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(terminals.Shutdown)
	t.Cleanup(actions.Stop)
	fixture.dependencies = httpapi.Dependencies{Config: config.Config{Origin: "http://127.0.0.1:8080", MasterKeyPath: keyPath}, SetupStore: fixture.store, Identity: identities, Accounts: accounts, Fleet: fleets, Operations: actions, Terminal: terminals, Audit: fixture.store, Logger: slog.New(slog.NewJSONHandler(&fixture.logs, nil)), Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>test SPA</html>")}}, AbsoluteSessionTTL: 8 * time.Hour}
	fixture.api, err = httpapi.New(fixture.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (fixture *fixture) upstreamHTTP(w http.ResponseWriter, r *http.Request) {
	if fixture.failUpstream.Load() {
		respond(w, 503, map[string]any{"error": map[string]any{"code": testKey, "message": testKey}})
		return
	}
	switch {
	case r.URL.Path == "/v1/me":
		respond(w, 200, instabox.Me{UserID: 1, OrgID: "org"})
	case r.URL.Path == "/v1/usage":
		respond(w, 200, instabox.Usage{Machines: 1, Org: instabox.OrgRef{Name: "private-organization"}, Cost: instabox.OrgUsage{CostUSD: 999}})
	case r.URL.Path == "/v1/machines" && r.Method == "GET":
		respond(w, 200, instabox.MachineList{Machines: []instabox.Machine{{ID: "machine", Name: "test-machine", OrgID: "org", State: "running"}}})
	case r.URL.Path == "/v1/machines/machine" && r.Method == "GET":
		respond(w, 200, instabox.Machine{ID: "machine", Name: "test-machine", OrgID: "org", State: "running", Metadata: instabox.Metadata{"private": testKey}})
	case strings.HasPrefix(r.URL.Path, "/v1/operations/"):
		if fixture.poll != nil {
			fixture.poll(w, r)
			return
		}
		respond(w, 200, instabox.Operation{ID: "upstream-operation", State: "succeeded", Phase: testKey})
	case strings.HasSuffix(r.URL.Path, "/connection-info"):
		respond(w, 200, instabox.ConnectionInfo{Capabilities: []string{"pty"}})
	case strings.HasSuffix(r.URL.Path, "/connections") && fixture.connection != nil:
		fixture.connection(w, r)
	case r.Method == "POST" || r.Method == "DELETE":
		respond(w, 202, instabox.Operation{ID: "upstream-operation", State: "queued"})
	case r.URL.Path == "/v1/templates":
		respond(w, 200, instabox.TemplateList{Templates: []instabox.Template{{ID: "ubuntu", Runtime: map[string]string{"secret": testKey}}}})
	default:
		respond(w, 404, map[string]string{})
	}
}

func (fixture *fixture) request(method, path, body string, cookie *http.Cookie, csrf, origin string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "203.0.113.10:12345"
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	fixture.api.ServeHTTP(response, request)
	return response
}

func (fixture *fixture) login(t *testing.T) (*http.Cookie, string) {
	t.Helper()
	response := fixture.request("POST", "/api/v1/auth/password", `{"username":"admin","password":"`+testPassword+`"}`, nil, "", fixture.dependencies.Config.Origin)
	if response.Code != 200 {
		t.Fatalf("password: %d %s", response.Code, response.Body)
	}
	challenge := response.Result().Cookies()[0]
	response = fixture.request("POST", "/api/v1/auth/totp", `{"code":"`+totpCode(fixture.clock.Load()/30)+`"}`, challenge, "", fixture.dependencies.Config.Origin)
	if response.Code != 200 {
		t.Fatalf("totp: %d %s", response.Code, response.Body)
	}
	var cookie *http.Cookie
	for _, candidate := range response.Result().Cookies() {
		if strings.Contains(candidate.Name, "session") {
			cookie = candidate
		}
	}
	var session struct {
		CSRF string `json:"csrfToken"`
	}
	if json.Unmarshal(response.Body.Bytes(), &session) != nil || cookie == nil || session.CSRF == "" {
		t.Fatal("missing session")
	}
	return cookie, session.CSRF
}

func (fixture *fixture) addAccount(t *testing.T, cookie *http.Cookie, csrf string) account.DTO {
	t.Helper()
	response := fixture.request("POST", "/api/v1/accounts", `{"name":"test","key":"`+testKey+`","declarations":{"write":true,"delete":true}}`, cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 200 {
		t.Fatalf("account: %d %s", response.Code, response.Body)
	}
	var value account.DTO
	if json.Unmarshal(response.Body.Bytes(), &value) != nil {
		t.Fatal("bad account")
	}
	return value
}

func bodyString(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	value, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}
