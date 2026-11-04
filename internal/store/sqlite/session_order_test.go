package sqlite

import (
	"testing"
	"time"
)

// A request's timestamp is taken before it queues for the single writer, so
// requests can commit out of order. That must not invalidate the session.
func TestSessionUseOutOfOrder(t *testing.T) {
	store, _ := openTest(t)
	now := seed(t, store)
	ctx := t.Context()
	record := session("out-of-order", now)
	if err := store.CreateSession(ctx, record); err != nil {
		t.Fatal(err)
	}

	later := now.Add(2 * time.Second)
	earlier := now.Add(time.Second)
	if _, err := store.AtomicUseSession(ctx, record.Hash, later, time.Hour, 24*time.Hour); err != nil {
		t.Fatalf("later request: %v", err)
	}
	used, err := store.AtomicUseSession(ctx, record.Hash, earlier, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("earlier request committed second was rejected: %v", err)
	}
	if !used.LastSeen.Equal(later) {
		t.Errorf("last seen = %v, want %v", used.LastSeen, later)
	}

	// The stored value must not have moved back either.
	stored, err := store.AtomicUseSession(ctx, record.Hash, later, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.LastSeen.Equal(later) {
		t.Errorf("stored last seen = %v, want %v", stored.LastSeen, later)
	}
}
