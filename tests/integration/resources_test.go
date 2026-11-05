package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/httpapi"
	"instadeck/internal/instabox"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPortsSnapshotIsolationValidationAndSensitive(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	origin := f.dependencies.Config.Origin
	var writes atomic.Int32
	var key string
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.Contains(r.URL.Path, "/ports") {
			if r.Method == "GET" {
				respond(w, 200, map[string]any{"enabled": true, "ports": []map[string]any{{"port": 3000, "token": "one-time-secret", "tokenUrl": "https://port.test/?_token=one-time-secret"}}})
			} else if r.Method == "POST" {
				writes.Add(1)
				respond(w, 201, instabox.PublicPort{Port: 3000, Token: "one-time-secret", TokenURL: "https://port.test/?_token=one-time-secret"})
			} else {
				writes.Add(1)
				w.WriteHeader(204)
			}
			return true
		}
		if strings.Contains(r.URL.Path, "/snapshots") {
			if r.Method == "GET" {
				respond(w, 200, instabox.SnapshotList{Snapshots: []instabox.Snapshot{{ID: "sn_test", Name: "saved", OrgID: "org", Status: "pending"}, {ID: "sn_foreign", OrgID: "foreign"}}})
			} else if r.Method == "POST" {
				writes.Add(1)
				if key != "" && key != r.Header.Get("Idempotency-Key") {
					t.Error("key changed")
				}
				key = r.Header.Get("Idempotency-Key")
				respond(w, 202, instabox.Snapshot{ID: "sn_test", Name: "saved", OrgID: "org", Status: "pending"})
			} else {
				writes.Add(1)
				if r.Header.Get("X-Confirm-Delete") != "sn_test" || r.Header.Get("Idempotency-Key") == "" {
					t.Error("delete headers absent")
				}
				w.WriteHeader(204)
			}
			return true
		}
		return false
	}
	post := func(path, body string) *http.Response {
		return f.request("POST", path, body, cookie, csrf, origin).Result()
	}
	valid := fmt.Sprintf(`{"credentialId":%q,"port":3000,"access":"private","protocol":"http"}`, a.CredentialID)
	for _, body := range []string{strings.Replace(valid, "3000", "0", 1), strings.Replace(valid, "3000", "5901", 1), strings.Replace(valid, "3000", "6768", 1), strings.Replace(valid, "3000", "65536", 1), strings.Replace(valid, "http", "tcp", 1), strings.Replace(valid, "private", "invalid", 1)} {
		if r := post("/api/v1/machines/machine/ports", body); r.StatusCode != 400 {
			t.Fatal("bad input accepted", body, r.StatusCode)
		}
	}
	if writes.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
	if r := post("/api/v1/machines/machine/ports", strings.Replace(valid, a.CredentialID, "unknown", 1)); r.StatusCode != 404 {
		t.Fatal("credential isolation", r.StatusCode)
	}
	if r := f.request("POST", "/api/v1/machines/machine/ports", valid, cookie, "", origin); r.Code != 403 {
		t.Fatal("csrf missing")
	}
	if r := f.request("POST", "/api/v1/machines/machine/ports", valid, cookie, csrf, origin); r.Code != 200 || !strings.Contains(r.Body.String(), "one-time-secret") {
		t.Fatal("publish failed", r.Code, r.Body)
	}
	if r := f.request("GET", "/api/v1/machines/machine/ports?credentialId="+a.CredentialID, "", cookie, "", ""); strings.Contains(r.Body.String(), "one-time-secret") {
		t.Fatal("list leaked token")
	}
	snapshot := fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"stable","name":"saved","rootOnly":true}`, a.CredentialID)
	for i := 0; i < 2; i++ {
		if r := post("/api/v1/machines/machine/snapshots", snapshot); r.StatusCode != 200 {
			t.Fatal("snapshot create", r.StatusCode)
		}
	}
	if r := f.request("GET", "/api/v1/snapshots", "", cookie, "", ""); r.Code != 200 || strings.Contains(r.Body.String(), "sn_foreign") {
		t.Fatal("foreign snapshot", r.Body)
	}
	if r := f.request("DELETE", "/api/v1/snapshots/sn_test?credentialId="+a.CredentialID, `{"clientRequestId":"delete","confirmName":"wrong"}`, cookie, csrf, origin); r.Code != 400 {
		t.Fatal("confirmation")
	}
	if r := f.request("DELETE", "/api/v1/snapshots/sn_test?credentialId="+a.CredentialID, `{"clientRequestId":"delete","confirmName":"saved"}`, cookie, csrf, origin); r.Code != 200 {
		t.Fatal("delete snapshot", r.Code, r.Body)
	}
	f.clock.Add(301)
	if r := post("/api/v1/machines/machine/ports", valid); r.StatusCode != 403 {
		t.Fatal("sensitive skipped")
	}
	if err := f.store.SetBoolSetting(context.Background(), "require_reauth", false); err != nil {
		t.Fatal(err)
	}
	restarted, err := httpapi.New(f.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	f.api = restarted
	if r := post("/api/v1/machines/machine/ports", valid); r.StatusCode != 200 {
		t.Fatal("sensitive switch", r.StatusCode)
	}
	records, _ := f.store.ReadAuditEvents(context.Background(), 0, 1000)
	raw, _ := json.Marshal(records)
	if strings.Contains(string(raw), "one-time-secret") || strings.Contains(f.logs.String(), "one-time-secret") {
		t.Fatal("token persisted in audit or logs")
	}
	credentials, _ := f.store.ListCredentials(context.Background())
	for _, c := range credentials {
		if strings.Contains(string(c.Envelope.Ciphertext), "one-time-secret") {
			t.Fatal("token persisted")
		}
	}
}
func TestSnapshotScopeAndPartialAccounts(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	b, err := f.dependencies.Accounts.Add(context.Background(), account.Input{Name: "other", Key: "other-key"})
	if err != nil {
		t.Fatal(err)
	}
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/v1/snapshots" {
			return false
		}
		if r.Header.Get("X-API-Key") == "other-key" {
			respond(w, 403, map[string]any{"error": map[string]string{"code": "missing_scope", "message": testKey}})
		} else {
			respond(w, 200, instabox.SnapshotList{Snapshots: []instabox.Snapshot{{ID: "sn_good", OrgID: "org"}}})
		}
		return true
	}
	r := f.request("GET", "/api/v1/snapshots", "", cookie, "", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), "snapshot_scope_missing") || !strings.Contains(r.Body.String(), a.CredentialID) || !strings.Contains(r.Body.String(), b.CredentialID) || strings.Contains(r.Body.String(), testKey) {
		t.Fatal("aggregation not isolated", r.Code, r.Body)
	}
}
func TestAuditExportFilters(t *testing.T) {
	f := newFixture(t, false)
	cookie, _ := f.login(t)
	event := audit.Event{Type: "operation.stop", CredentialID: "=unsafe", MachineID: "machine", Result: "failure"}
	event.SetDetail("no_capacity")
	if err := f.store.WriteEvent(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	r := f.request("GET", "/api/v1/audit-events/export?type=operation.&result=failure", "", cookie, "", "")
	if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Disposition"), "attachment") || !strings.Contains(r.Body.String(), "'=unsafe") || !strings.Contains(r.Body.String(), "no_capacity") {
		t.Fatal("export", r.Code, r.Body)
	}
	r = f.request("GET", "/api/v1/audit-events?since=invalid", "", cookie, "", "")
	if r.Code != 400 {
		t.Fatal("invalid time accepted")
	}
}

func TestNotificationCRUDAndSensitive(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	origin := f.dependencies.Config.Origin
	body := `{"type":"telegram","name":"alerts","enabled":true,"events":["operation.failed"],"config":{"botToken":"123456:never-return-this","chatId":"-123"}}`
	r := f.request("POST", "/api/v1/notification-channels", body, cookie, "", origin)
	if r.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	r = f.request("POST", "/api/v1/notification-channels", body, cookie, csrf, origin)
	if r.Code != 200 || strings.Contains(r.Body.String(), "never-return-this") {
		t.Fatal("channel create", r.Code, r.Body)
	}
	r = f.request("GET", "/api/v1/notification-channels", "", cookie, "", "")
	if r.Code != 200 || strings.Contains(r.Body.String(), "never-return-this") || strings.Contains(r.Body.String(), "ciphertext") {
		t.Fatal("channel secret exposed")
	}
	var result struct {
		Channels []struct {
			ID string `json:"id"`
		}
	}
	if json.Unmarshal(r.Body.Bytes(), &result) != nil || len(result.Channels) != 1 {
		t.Fatal("missing channel")
	}
	id := result.Channels[0].ID
	r = f.request("PATCH", "/api/v1/notification-channels/"+id, `{"name":"changed","config":{}}`, cookie, csrf, origin)
	if r.Code != 200 {
		t.Fatal("patch", r.Code, r.Body)
	}
	records, _ := f.store.ListChannels(context.Background())
	plain, err := f.ring.Decrypt(records[0].Envelope, []byte("instadeck:notification:"+id+":v1"))
	if err != nil || !strings.Contains(string(plain), "never-return-this") {
		t.Fatal("blank edit lost secret")
	}
	clear(plain)
	f.clock.Add(301)
	r = f.request("DELETE", "/api/v1/notification-channels/"+id, "", cookie, csrf, origin)
	if r.Code != 403 {
		t.Fatal("sensitive bypass")
	}
	if strings.Contains(f.logs.String(), "never-return-this") {
		t.Fatal("token logged")
	}
}
func TestCredentialInvalidTransitionEmitsOnce(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	calls := 0
	f.dependencies.Accounts.OnInvalid = func(value account.DTO) {
		calls++
		if value.CredentialID != a.CredentialID {
			t.Error("wrong account")
		}
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := f.dependencies.Accounts.Observe(ctx, a.CredentialID, a.Version, "", &instabox.APIError{StatusCode: 401, Code: "invalid_key"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("invalid transition duplicate", calls)
	}
}

func TestPortPermissionDoesNotDenySnapshotScope(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	origin := f.dependencies.Config.Origin
	calls := 0
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/ports") {
			calls++
			respond(w, 201, instabox.PublicPort{Port: 3000})
			return true
		}
		if strings.HasSuffix(r.URL.Path, "/snapshots") {
			respond(w, 201, instabox.Snapshot{ID: "sn", OrgID: "org"})
			return true
		}
		return false
	}
	if err := f.dependencies.Accounts.Observe(context.Background(), a.CredentialID, a.Version, account.Write, &instabox.APIError{StatusCode: 403, Code: "missing_scope"}); err != nil {
		t.Fatal(err)
	}
	r := f.request("POST", "/api/v1/machines/machine/ports", fmt.Sprintf(`{"credentialId":%q,"port":3000,"access":"private","protocol":"http"}`, a.CredentialID), cookie, csrf, origin)
	if r.Code != 400 || !strings.Contains(r.Body.String(), "capability_denied") || calls != 0 {
		t.Fatal("denied write reached upstream", r.Code, r.Body)
	}
	r = f.request("POST", "/api/v1/machines/machine/snapshots", fmt.Sprintf(`{"credentialId":%q,"name":"test","clientRequestId":"independent-scope"}`, a.CredentialID), cookie, csrf, origin)
	if r.Code != 200 {
		t.Fatal("machine scope blocked image scope", r.Code, r.Body)
	}
}

func TestRenameMachine(t *testing.T) {
	f := newFixture(t, false)
	cookie, csrf := f.login(t)
	a := f.addAccount(t, cookie, csrf)
	origin := f.dependencies.Config.Origin
	var writes atomic.Int32
	var key, sent string
	f.resource = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "PATCH" || r.URL.Path != "/v1/machines/machine" {
			return false
		}
		writes.Add(1)
		key = r.Header.Get("Idempotency-Key")
		var input map[string]any
		_ = json.NewDecoder(r.Body).Decode(&input)
		if len(input) != 1 {
			t.Errorf("only the name may be sent: %v", input)
		}
		sent, _ = input["name"].(string)
		if sent == "taken" {
			respond(w, 409, map[string]any{"error": map[string]any{"code": "operation_conflict"}})
			return true
		}
		respond(w, 200, instabox.Machine{ID: "machine", OrgID: "org", Name: sent})
		return true
	}
	body := func(credential, name string) string {
		return fmt.Sprintf(`{"credentialId":%q,"clientRequestId":"rename-1","name":%q}`, credential, name)
	}
	const path = "/api/v1/machines/machine"
	for _, name := range []string{"", "Upper", "-leading", "has space", strings.Repeat("a", 64)} {
		if r := f.request("PATCH", path, body(a.CredentialID, name), cookie, csrf, origin); r.Code != 400 {
			t.Errorf("name %q: %d", name, r.Code)
		}
	}
	if r := f.request("PATCH", path, body("unknown", "new-name"), cookie, csrf, origin); r.Code != 404 {
		t.Errorf("unknown credential: %d", r.Code)
	}
	if r := f.request("PATCH", path, body(a.CredentialID, "new-name"), cookie, "", origin); r.Code != 403 {
		t.Errorf("missing csrf: %d", r.Code)
	}
	if writes.Load() != 0 {
		t.Fatalf("rejected requests reached upstream: %d", writes.Load())
	}
	r := f.request("PATCH", path, body(a.CredentialID, "new-name"), cookie, csrf, origin)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "new-name") || sent != "new-name" || key == "" {
		t.Fatalf("rename: %d %s sent=%q key=%q", r.Code, r.Body, sent, key)
	}
	if r = f.request("PATCH", path, body(a.CredentialID, "taken"), cookie, csrf, origin); r.Code != 400 || !strings.Contains(r.Body.String(), "machine_name_taken") {
		t.Fatalf("taken: %d %s", r.Code, r.Body)
	}
	if r = f.request("GET", "/api/v1/audit-events?type=machine.rename&result=failure", "", cookie, "", ""); r.Code != 200 || !strings.Contains(r.Body.String(), "machine_name_taken") {
		t.Fatalf("audit: %d %s", r.Code, r.Body)
	}
}
