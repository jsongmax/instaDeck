package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"instadeck/internal/account"
	"instadeck/internal/fleet"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func accountStore(t *testing.T) (*sqlite.Store, *keyring.Ring) {
	t.Helper()
	directory, err := os.MkdirTemp(".", "account-integration-")
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
	return store, ring
}

func TestAccountFleetUnauthorizedValidationAndReplacement(t *testing.T) {
	store, ring := accountStore(t)
	var reject atomic.Bool
	var machineCalls, usageCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/me":
			fmt.Fprint(writer, `{"userId":42,"orgId":"org"}`)
		case "/v1/machines":
			machineCalls.Add(1)
			if reject.Load() {
				writer.WriteHeader(401)
				fmt.Fprint(writer, `{"error":{"code":"untrusted-private-text","message":"private-key"}}`)
				return
			}
			fmt.Fprint(writer, `{"machines":[{"id":"box","orgId":"org","name":"example","state":"running","runtime":{"ready":true}}],"nextCursor":null}`)
		case "/v1/usage":
			usageCalls.Add(1)
			fmt.Fprint(writer, `{"machines":1}`)
		default:
			writer.WriteHeader(404)
		}
	}))
	defer server.Close()
	accounts := account.New(store, ring, func(key string) (account.Client, error) {
		return instabox.NewClient(key, instabox.Options{BaseURL: server.URL})
	}, nil)
	ctx := context.Background()
	imported, err := accounts.Add(ctx, account.Input{Name: "example", Key: "private-key", Declarations: account.Declarations{Delete: true}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	aggregator, err := fleet.New(accounts, fleet.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := aggregator.GetFleet(ctx)
	if err != nil || initial.Partial || len(initial.Machines) != 1 || !initial.Machines[0].Sources[0].CanDelete {
		t.Fatal("initial aggregation failed", err)
	}
	reject.Store(true)
	now = now.Add(11 * time.Second)
	failed, err := aggregator.GetFleet(ctx)
	if err == nil || !failed.Partial || !failed.Machines[0].Stale || failed.Machines[0].Sources[0].CanDelete || !failed.Accounts[0].Complete {
		t.Fatal("invalid credential did not retain safe old snapshot", err)
	}
	before := machineCalls.Load()
	for query := 0; query < 3; query++ {
		now = now.Add(time.Hour)
		_, _ = aggregator.GetFleet(ctx)
	}
	if machineCalls.Load() != before || usageCalls.Load() != 1 {
		t.Fatal("invalid credential requested again")
	}
	record, err := store.CredentialByID(ctx, imported.CredentialID)
	if err != nil || record.Status != "invalid" {
		t.Fatal("401 not persisted", err)
	}
	reject.Store(false)
	if _, err = accounts.Validate(ctx, imported.CredentialID); err != nil {
		t.Fatal(err)
	}
	recovered, err := aggregator.GetFleet(ctx)
	if err != nil || recovered.Partial || machineCalls.Load() != before+1 {
		t.Fatal("explicit validation did not resume fetching", err)
	}
	replacement, err := accounts.Replace(ctx, imported.CredentialID, account.Input{Key: "replacement-key"})
	if err != nil || replacement.Version != 2 {
		t.Fatal("replacement failed", err)
	}
	replaced, err := aggregator.GetFleet(ctx)
	if err != nil || replaced.Machines[0].Sources[0].CanDelete || machineCalls.Load() != before+2 {
		t.Fatal("old version cache reused", err)
	}
}

func TestCredentialRepositoryCASAndErrorIdentity(t *testing.T) {
	store, _ := accountStore(t)
	ctx := context.Background()
	now := time.Unix(1000, 0)
	record := sqlite.Credential{ID: "credential", Name: "test", Status: "valid", Version: 1, Revision: 1, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateCredential(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateCredential(ctx, record); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("constraint conflict identity", err)
	}
	if _, err := store.CredentialByID(ctx, "missing"); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("not-found identity", err)
	}
	updated := record
	updated.Revision = 2
	updated.Version = 2
	if err := store.CompareAndSwapCredential(ctx, updated, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwapCredential(ctx, record, 1); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("stale update accepted", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.CreateCredential(canceled, sqlite.Credential{ID: "canceled"}); !errors.Is(err, context.Canceled) {
		t.Fatal("context error lost", err)
	}
	if err := store.DeleteCredential(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwapCredential(ctx, updated, 2); !errors.Is(err, sqlite.ErrConflict) {
		t.Fatal("CAS resurrected tombstone", err)
	}
	if err := store.UpdateCredential(ctx, updated); !errors.Is(err, sqlite.ErrNotFound) {
		t.Fatal("legacy update resurrected tombstone", err)
	}
}
func TestUsageUnauthorizedResumesAfterValidation(t *testing.T) {
	store, ring := accountStore(t)
	var reject atomic.Bool
	var usageCalls atomic.Int32
	reject.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/me":
			fmt.Fprint(writer, `{"userId":42,"orgId":"org"}`)
		case "/v1/machines":
			fmt.Fprint(writer, `{"machines":[],"nextCursor":null}`)
		case "/v1/usage":
			usageCalls.Add(1)
			if reject.Load() {
				writer.WriteHeader(401)
				fmt.Fprint(writer, `{"error":{}}`)
				return
			}
			fmt.Fprint(writer, `{"machines":0}`)
		}
	}))
	defer server.Close()
	accounts := account.New(store, ring, func(key string) (account.Client, error) {
		return instabox.NewClient(key, instabox.Options{BaseURL: server.URL})
	}, nil)
	ctx := context.Background()
	imported, err := accounts.Add(ctx, account.Input{Name: "test", Key: "private-key"})
	if err != nil {
		t.Fatal(err)
	}
	aggregator, err := fleet.New(accounts, fleet.Options{Now: func() time.Time { return time.Unix(1000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := aggregator.GetFleet(ctx)
	if err != nil || !initial.Partial || initial.Accounts[0].UsageError.Code != "unauthorized" {
		t.Fatal("usage unauthorized missing", err)
	}
	_, _ = aggregator.GetFleet(ctx)
	if usageCalls.Load() != 1 {
		t.Fatal("usage 401 retried")
	}
	reject.Store(false)
	if _, err = accounts.Validate(ctx, imported.CredentialID); err != nil {
		t.Fatal(err)
	}
	recovered, err := aggregator.GetFleet(ctx)
	if err != nil || recovered.Partial || usageCalls.Load() != 2 {
		t.Fatal("validated usage request did not resume immediately", err)
	}
}
