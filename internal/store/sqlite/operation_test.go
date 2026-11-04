package sqlite

import (
	"context"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestActionAuditTransactionRollback(t *testing.T) {
	directory, err := os.MkdirTemp(".", "operation-store-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	store, err := Open(context.Background(), filepath.Join(directory, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = store.CreateCredential(ctx, account.Record{ID: "credential", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.writer.ExecContext(ctx, `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	action := Action{ID: "action", CredentialID: "credential", ClientRequestID: "request", UpstreamIdempotencyKey: "key", State: "pending", CreatedAt: now, UpdatedAt: now}
	if err = store.CreateActionIntent(ctx, action, audit.Event{Time: now, Type: "operation.intent"}); err == nil {
		t.Fatal("audit failure accepted")
	}
	if _, err = store.ActionByID(ctx, action.ID); err != ErrNotFound {
		t.Fatal("action survived rolled-back audit")
	}
	if _, err = store.writer.ExecContext(ctx, "DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateActionIntent(ctx, action, audit.Event{Time: now, Type: "operation.intent"}); err != nil {
		t.Fatal(err)
	}
	records, err := store.ReadAuditEvents(ctx, 0, 10)
	if err != nil || len(records) != 1 {
		t.Fatal("atomic intent missing")
	}
}

func TestUnfinishedActionsOnlyLocalRecoverableStates(t *testing.T) {
	directory, err := os.MkdirTemp(".", "operation-state-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	store, err := Open(context.Background(), filepath.Join(directory, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = store.CreateCredential(ctx, account.Record{ID: "credential", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "submitting", "tracking", "unknown", "completed", "queued", "running", "succeeded", "failed", "unexpected", ""} {
		action := Action{ID: "action-" + state, CredentialID: "credential", ClientRequestID: "request-" + state, UpstreamIdempotencyKey: "key-" + state, State: state, CreatedAt: now, UpdatedAt: now}
		if err = store.CreateAction(ctx, action); err != nil {
			t.Fatal(err)
		}
	}
	actions, err := store.ListUnfinishedActions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	states := make([]string, 0, len(actions))
	for _, action := range actions {
		states = append(states, action.State)
	}
	if !reflect.DeepEqual(states, []string{"pending", "submitting", "tracking"}) {
		t.Fatalf("unfinished states = %q, want only pending/submitting/tracking", states)
	}
}
