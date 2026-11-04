package sqlite

import (
	"context"
	"instadeck/internal/audit"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditFiltersAndStablePages(t *testing.T) {
	store, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, e := range []audit.Event{{Time: now, Type: "operation.stop", CredentialID: "a", MachineID: "m", Result: "failure"}, {Time: now, Type: "operation.start", CredentialID: "b", MachineID: "n", Result: "success"}, {Time: now, Type: "operation.stop", CredentialID: "a", MachineID: "m", Result: "failure"}} {
		if err := store.WriteEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	f := AuditFilter{CredentialID: "a", MachineID: "m", Type: "operation.", Result: "failure", Since: &now, Until: &now}
	first, err := store.FilterAuditEvents(ctx, 0, 1, f)
	if err != nil || len(first) != 1 {
		t.Fatal(err, first)
	}
	second, err := store.FilterAuditEvents(ctx, first[0].Cursor, 1, f)
	if err != nil || len(second) != 1 || second[0].Cursor <= first[0].Cursor {
		t.Fatal("unstable cursor")
	}
	for _, v := range []string{"' OR 1=1 --", "operation.%", "operation._"} {
		page, err := store.FilterAuditEvents(ctx, 0, 100, AuditFilter{Type: v})
		if err != nil || len(page) != 0 {
			t.Fatal("nonliteral prefix", v, err)
		}
	}
}

func TestAuditDescendingShowsNewestFirst(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := 0; index < 5; index++ {
		if err = store.WriteEvent(ctx, audit.Event{Time: time.Now(), Type: "login", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.FilterAuditEvents(ctx, 0, 2, AuditFilter{Descending: true})
	if err != nil || len(first) != 2 || first[0].Cursor != 5 || first[1].Cursor != 4 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	next, err := store.FilterAuditEvents(ctx, 0, 2, AuditFilter{Descending: true, Before: first[1].Cursor})
	if err != nil || len(next) != 2 || next[0].Cursor != 3 || next[1].Cursor != 2 {
		t.Fatalf("next page: %+v %v", next, err)
	}
}
