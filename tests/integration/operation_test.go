package integration_test

import (
	"context"
	"encoding/json"
	"instadeck/internal/account"
	"instadeck/internal/fleet"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"instadeck/internal/operation"
	"instadeck/internal/store/sqlite"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOperationProgressAndProcessRestart(t *testing.T) {
	fixture := newFixture(t, false)
	var pollCount atomic.Int32
	pollEntered := make(chan struct{}, 1)
	fixture.poll = func(w http.ResponseWriter, r *http.Request) {
		if pollCount.Add(1) == 1 {
			pollEntered <- struct{}{}
			<-r.Context().Done()
			return
		}
		respond(w, 200, instabox.Operation{ID: "upstream-operation", State: "succeeded", Phase: testKey})
	}
	cookie, csrf := fixture.login(t)
	credential := fixture.addAccount(t, cookie, csrf)
	if err := fixture.dependencies.Operations.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := fixture.request("POST", "/api/v1/machines/machine/actions", `{"clientRequestId":"start-1","credentialId":"`+credential.CredentialID+`","kind":"start","body":{}}`, cookie, csrf, fixture.dependencies.Config.Origin)
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body)
	}
	var action struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(response.Body.Bytes(), &action) != nil || action.ID == "" {
		t.Fatal("missing action")
	}
	select {
	case <-pollEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("action did not run")
	}
	progress := fixture.request("GET", "/api/v1/actions/"+action.ID, "", cookie, "", "")
	if progress.Code != 200 || !strings.Contains(progress.Body.String(), `"state":"tracking"`) {
		t.Fatal("progress unavailable", progress.Body)
	}
	stored, err := fixture.dependencies.Operations.Get(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{stored.UpstreamIdempotencyKey, "requestBody", "clientRequestId", testKey} {
		if strings.Contains(progress.Body.String(), forbidden) {
			t.Fatal("action leaked", forbidden)
		}
	}
	events, err := fixture.store.ReadAuditEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Event.Type == "operation.intent" {
			found = true
			if event.Event.SubjectID != "admin-id" {
				t.Fatal("administrator missing from operation audit")
			}
		}
	}
	if !found {
		t.Fatal("no action audit")
	}
	fixture.dependencies.Operations.Stop()
	if err = fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	absolute, _ := filepath.Abs(fixture.directory)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryProcess$")
	command.Env = append(os.Environ(), "INSTADECK_TEST_RECOVERY="+absolute, "INSTADECK_TEST_UPSTREAM="+fixture.upstream.URL, "INSTADECK_TEST_ACTION="+action.ID)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("recovery subprocess: %v %s", err, output)
	}
	fixture.store, err = sqlite.Open(context.Background(), filepath.Join(fixture.directory, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.store.Close()
	recovered, err := fixture.store.ActionByID(context.Background(), action.ID)
	if err != nil || recovered.State != "completed" || recovered.UpstreamState != "succeeded" || recovered.UpstreamIdempotencyKey != stored.UpstreamIdempotencyKey {
		t.Fatal("restart recovery failed", err)
	}
}

func TestRecoveryProcess(t *testing.T) {
	directory := os.Getenv("INSTADECK_TEST_RECOVERY")
	if directory == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, filepath.Join(directory, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ring, err := keyring.Load(filepath.Join(directory, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	accounts := account.New(store, ring, func(key string) (account.Client, error) {
		return instabox.NewClient(key, instabox.Options{BaseURL: os.Getenv("INSTADECK_TEST_UPSTREAM")})
	}, nil)
	fleets, err := fleet.New(accounts, fleet.Options{})
	if err != nil {
		t.Fatal(err)
	}
	actions, err := operation.New(store, accounts, fleets, fleets, operation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = actions.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer actions.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		value, err := actions.Get(ctx, os.Getenv("INSTADECK_TEST_ACTION"))
		if err != nil {
			t.Fatal(err)
		}
		if value.State == "completed" {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("recovery timed out")
		case <-ticker.C:
		}
	}
}
