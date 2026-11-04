package fleet

import (
	"context"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
	"sync/atomic"
	"testing"
	"time"
)

// An account waiting for a concurrency slot has not sent anything yet, so that
// wait must not use up its own request timeout.
func TestQueuedAccountTimeoutStartsAfterSlot(t *testing.T) {
	var started atomic.Bool
	list := func(ctx context.Context, _ string) (instabox.MachineList, error) {
		if started.CompareAndSwap(false, true) {
			<-ctx.Done()
			return instabox.MachineList{}, ctx.Err()
		}
		return page(), nil
	}
	provider := &fakeAccounts{
		credentials: []account.DTO{credential("first", "org-1"), credential("second", "org-2"), credential("third", "org-3")},
		clients: map[string]*fakeClient{
			"first":  {list: list},
			"second": {list: list},
			"third":  {list: list},
		},
	}
	service := newService(t, provider, Options{Concurrency: 1, AccountTimeout: 100 * time.Millisecond, OverallTimeout: 5 * time.Second})

	response, err := service.GetFleet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, status := range response.Accounts {
		if status.Error != nil {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("%d accounts failed, want only the slow one", failed)
	}
}

// Cache invalidation racing with a poll must not make an account's machines
// disappear from the table, and must not expose the discarded in-flight result.
func TestInvalidationDuringFetchKeepsPreviousMachines(t *testing.T) {
	var calls atomic.Int32
	fetching := make(chan struct{})
	release := make(chan struct{})
	provider := &fakeAccounts{
		credentials: []account.DTO{credential("key", "org")},
		clients: map[string]*fakeClient{"key": {list: func(context.Context, string) (instabox.MachineList, error) {
			if calls.Add(1) == 1 {
				return page(machine("previous", "org", "previous", "running", true)), nil
			}
			close(fetching)
			<-release
			return page(machine("discarded", "org", "discarded", "running", true)), nil
		}}},
	}
	var now atomic.Int64
	now.Store(time.Unix(1000, 0).UnixNano())
	service := newService(t, provider, Options{MachineTTL: time.Second, Now: func() time.Time { return time.Unix(0, now.Load()) }})

	if _, err := service.GetFleet(t.Context()); err != nil {
		t.Fatal(err)
	}
	now.Add(int64(2 * time.Second))

	result := make(chan Response, 1)
	go func() {
		response, _ := service.GetFleet(context.Background())
		result <- response
	}()
	await(t, fetching)
	service.Invalidate("key")
	close(release)

	response := await(t, result)
	if len(response.Machines) != 1 || response.Machines[0].ID != "previous" {
		t.Fatalf("machines = %+v, want only the previous snapshot", response.Machines)
	}
	if !response.Machines[0].Stale || !response.Accounts[0].Stale {
		t.Error("previous snapshot must be marked stale")
	}
}
