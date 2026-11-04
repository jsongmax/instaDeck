package account_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serviceFixture(t *testing.T, handler http.HandlerFunc) (*account.Service, *sqlite.Store, *keyring.Ring) {
	t.Helper()
	directory, err := os.MkdirTemp(".", "account-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	masterKey, err := keyring.GenerateKey(1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(keyring.File{Keys: []keyring.Key{masterKey}})
	path := filepath.Join(directory, "keys.json")
	if err = os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(directory, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	factory := func(key string) (account.Client, error) {
		return instabox.NewClient(key, instabox.Options{BaseURL: server.URL})
	}
	service := account.New(store, ring, factory, func() time.Time { return time.Unix(1000, 0).UTC() })
	return service, store, ring
}

func respond(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	fmt.Fprint(writer, body)
}

func identityHandler(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/v1/me":
		respond(writer, 200, `{"userId":42,"orgId":"org","email":"private@example.invalid","csrfToken":"private-csrf"}`)
	case "/v1/machines":
		respond(writer, 200, `{"machines":[],"nextCursor":null}`)
	case "/v1/usage":
		respond(writer, 200, `{"machines":2}`)
	default:
		respond(writer, 404, `{}`)
	}
}

func add(t *testing.T, service *account.Service, key string, declarations account.Declarations) account.DTO {
	t.Helper()
	result, err := service.Add(context.Background(), account.Input{Name: "test account", Key: key, Declarations: declarations})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDTOEncryptionAADAndHistory(t *testing.T) {
	service, store, ring := serviceFixture(t, identityHandler)
	ctx := context.Background()
	secret := "private-account-key-unique"
	first := add(t, service, secret, account.Declarations{})
	second := add(t, service, "second-private-key", account.Declarations{})
	if first.UserID != 42 || first.OrgID != "org" || first.Version != 1 || first.CanDelete() {
		t.Fatal("identity or default permission")
	}
	record, err := store.CredentialByID(ctx, first.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if string(record.Envelope.Ciphertext) == secret || len(record.Envelope.Nonce) == 0 {
		t.Fatal("unencrypted key")
	}
	values, _ := service.List(ctx)
	for _, value := range []any{first, values, account.Input{Key: secret}, record} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{secret, "ciphertext", "nonce", "private-csrf", "private@example.invalid"} {
			if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
				t.Fatal("secret in JSON")
			}
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), secret) {
				t.Fatal("secret in formatting")
			}
		}
	}
	if _, err = ring.Decrypt(record.Envelope, []byte("instadeck:credential:"+first.CredentialID+":v1")); err != nil {
		t.Fatal("incorrect AAD", err)
	}
	moved, _ := store.CredentialByID(ctx, second.CredentialID)
	moved.Envelope = record.Envelope
	if err = store.UpdateCredential(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err = service.ClientForCredential(ctx, second.CredentialID); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatal("swapped ciphertext accepted or leaked")
	}
	now := time.Unix(1000, 0).UTC()
	action := sqlite.Action{ID: "history", CredentialID: first.CredentialID, ClientRequestID: "history-client", UpstreamIdempotencyKey: "history-upstream", State: "queued", CreatedAt: now, UpdatedAt: now}
	if err = store.CreateAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	if err = store.WriteEvent(ctx, audit.Event{Time: now, Type: "test", CredentialID: first.CredentialID}); err != nil {
		t.Fatal(err)
	}
	if err = service.Delete(ctx, first.CredentialID); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CredentialByID(ctx, first.CredentialID)
	if err != nil || deleted.Status != "deleted" || deleted.Enabled || len(deleted.Envelope.Ciphertext) != 0 || len(deleted.Envelope.Nonce) != 0 {
		t.Fatal("credential not erased", err)
	}
	if _, err = store.ActionByID(ctx, action.ID); err != nil {
		t.Fatal("history lost", err)
	}
	events, err := store.ReadAuditEvents(ctx, 0, 10)
	if err != nil || len(events) != 1 {
		t.Fatal("audit lost", err)
	}
	listed, err := service.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatal("tombstone exposed")
	}
	if _, err = service.SetEnabled(ctx, first.CredentialID, true); err == nil {
		t.Fatal("resurrected deleted key")
	}
}

func TestCapabilitiesAndCredentialLifecycle(t *testing.T) {
	var machineCalls atomic.Int32
	var unauthorized atomic.Bool
	service, _, _ := serviceFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/machines" {
			machineCalls.Add(1)
			if unauthorized.Load() {
				respond(writer, 401, `{"error":{"message":"private-account-key","code":"private-account-key"}}`)
				return
			}
		}
		identityHandler(writer, request)
	})
	ctx := context.Background()
	value := add(t, service, "private-account-key", account.Declarations{Write: true, Delete: true})
	if value.Capabilities.Read != account.Unknown || value.Capabilities.Write != account.Declared || !value.CanDelete() {
		t.Fatal("declaration model")
	}
	client, err := service.ClientForCredential(ctx, value.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetMe(ctx); err != nil {
		t.Fatal(err)
	}
	values, _ := service.List(ctx)
	if values[0].Capabilities.Read != account.Unknown {
		t.Fatal("GetMe verified scope")
	}
	if _, err = client.ListMachines(ctx, ""); err != nil {
		t.Fatal(err)
	}
	values, _ = service.List(ctx)
	if values[0].Capabilities.Read != account.Verified {
		t.Fatal("successful read not verified")
	}
	for _, capability := range []account.Capability{account.Read, account.Write, account.Delete} {
		if err = service.Observe(ctx, value.CredentialID, value.Version, capability, &instabox.APIError{StatusCode: 403, Code: "missing_scope"}); err != nil {
			t.Fatal(err)
		}
	}
	values, _ = service.List(ctx)
	if values[0].Capabilities != (account.Capabilities{Read: account.Denied, Write: account.Denied, Delete: account.Denied}) || values[0].CanDelete() {
		t.Fatal("missing scope not denied")
	}
	declared, err := service.SetDeclarations(ctx, value.CredentialID, account.Declarations{Delete: true})
	if err != nil || declared.CanDelete() {
		t.Fatal("declaration overrode denial")
	}
	unauthorized.Store(true)
	if _, err = client.ListMachines(ctx, ""); err == nil || strings.Contains(err.Error(), "private-account-key") {
		t.Fatal("401 missing or leaked")
	}
	before := machineCalls.Load()
	for attempt := 0; attempt < 3; attempt++ {
		if _, err = client.ListMachines(ctx, ""); err == nil {
			t.Fatal("invalid key reused")
		}
		if _, err = service.ClientForCredential(ctx, value.CredentialID); err == nil {
			t.Fatal("invalid client created")
		}
	}
	if machineCalls.Load() != before {
		t.Fatal("401 retried")
	}
	disabled, err := service.SetEnabled(ctx, value.CredentialID, false)
	if err != nil || disabled.Enabled {
		t.Fatal("disable failed")
	}
	if _, err = service.ClientForCredential(ctx, value.CredentialID); err == nil {
		t.Fatal("disabled key used")
	}
	enabled, err := service.SetEnabled(ctx, value.CredentialID, true)
	if err != nil || enabled.Status != "invalid" {
		t.Fatal("enable restored invalid key")
	}
	unauthorized.Store(false)
	validated, err := service.Validate(ctx, value.CredentialID)
	if err != nil || validated.Status != "valid" {
		t.Fatal("explicit validation failed", err)
	}
	if _, err = client.ListMachines(ctx, ""); err != nil {
		t.Fatal("validation did not restore access", err)
	}
	replacement, err := service.Replace(ctx, value.CredentialID, account.Input{Key: "replacement-private-key", Declarations: account.Declarations{Delete: true}})
	if err != nil || replacement.Version != 2 || !replacement.CanDelete() {
		t.Fatal("replacement failed", err)
	}
	if err = service.Observe(ctx, value.CredentialID, 1, account.Read, &instabox.APIError{StatusCode: 401}); err != nil {
		t.Fatal(err)
	}
	values, _ = service.List(ctx)
	if values[0].Status != "valid" {
		t.Fatal("old request invalidated replacement")
	}
	if _, err = client.ListMachines(ctx, ""); err == nil {
		t.Fatal("old client accepted")
	}
}

func TestUntrustedErrorsAreFiltered(t *testing.T) {
	service, store, ring := serviceFixture(t, identityHandler)
	secret := "never-include-this-secret"
	service = account.New(store, ring, func(string) (account.Client, error) { return nil, errors.New(secret) }, nil)
	_, err := service.Add(context.Background(), account.Input{Name: "test", Key: secret})
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), secret) {
		t.Fatal("factory error leaked")
	}
	failure := account.FilterError(&instabox.APIError{StatusCode: 503, Code: secret, Retryable: true})
	encoded, _ := json.Marshal(failure)
	if strings.Contains(string(encoded), secret) || failure.Code != "unavailable" {
		t.Fatal("upstream code leaked")
	}
	if !errors.Is(account.FilterError(context.Canceled), context.Canceled) {
		t.Fatal("context identity lost")
	}
}

func TestIdentityRequiresOrganizationOnly(t *testing.T) {
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"personal key without user ID", `{"login":"tester","org":{"id":"org_1","name":"p","tier":"free","kind":"personal"}}`, true},
		{"workspace key", `{"orgId":"org_2"}`, true},
		{"no organization", `{"userId":42,"login":"tester"}`, false},
		{"empty", `{}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, _, _ := serviceFixture(t, func(writer http.ResponseWriter, request *http.Request) { respond(writer, 200, test.body) })
			_, err := service.Add(context.Background(), account.Input{Name: "test account", Key: "private-key"})
			if (err == nil) != test.valid {
				t.Fatal(err)
			}
			if err != nil && account.FilterError(err).Code != "invalid_identity" {
				t.Fatal(err)
			}
		})
	}
}
