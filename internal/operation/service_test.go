package operation_test

import (
	"context"
	"errors"
	"fmt"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/instabox"
	"instadeck/internal/operation"
	"instadeck/internal/store/sqlite"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type request struct {
	kind, key, confirmation string
	body                    []byte
}
type fakeClient struct {
	mutex    sync.Mutex
	requests []request
	submit   func(context.Context, request) (instabox.Operation, error)
	poll     func(context.Context) (instabox.Operation, error)
}

func (client *fakeClient) call(ctx context.Context, value request) (instabox.Operation, error) {
	client.mutex.Lock()
	client.requests = append(client.requests, value)
	client.mutex.Unlock()
	if client.submit != nil {
		return client.submit(ctx, value)
	}
	return instabox.Operation{ID: "remote", State: "queued"}, nil
}
func (client *fakeClient) GetMachine(context.Context, string) (instabox.Machine, error) {
	return instabox.Machine{ID: "machine", Name: "name"}, nil
}
func (client *fakeClient) CreateMachine(ctx context.Context, body instabox.CreateMachineRequest, key string) (instabox.Operation, error) {
	return client.call(ctx, request{kind: "create", key: key})
}
func (client *fakeClient) StartMachine(ctx context.Context, id string, body instabox.StartMachineRequest, key string) (instabox.Operation, error) {
	return client.call(ctx, request{kind: "start", key: key, body: []byte(fmt.Sprintf("%v", body))})
}
func (client *fakeClient) StopMachine(ctx context.Context, id string, body instabox.StopMachineRequest, key string) (instabox.Operation, error) {
	return client.call(ctx, request{kind: "stop", key: key})
}
func (client *fakeClient) ResumeMachine(ctx context.Context, id string, body instabox.ResumeMachineRequest, key string) (instabox.Operation, error) {
	return client.call(ctx, request{kind: "resume", key: key})
}
func (client *fakeClient) SuspendMachine(ctx context.Context, id, key string) (instabox.Operation, error) {
	return client.call(ctx, request{kind: "suspend", key: key})
}
func (client *fakeClient) DeleteMachine(ctx context.Context, id, key, confirmation string) (instabox.Operation, error) {
	return client.call(ctx, request{kind: "delete", key: key, confirmation: confirmation})
}
func (client *fakeClient) GetOperation(ctx context.Context, id string, wait time.Duration) (instabox.Operation, error) {
	if wait != 25*time.Second {
		panic("wrong polling wait")
	}
	if client.poll != nil {
		return client.poll(ctx)
	}
	return instabox.Operation{ID: id, State: "succeeded"}, nil
}

type fakeAccounts struct {
	mutex      sync.Mutex
	credential account.DTO
	client     *fakeClient
	denied     atomic.Int32
}

func (accounts *fakeAccounts) WriteClientForCredential(ctx context.Context, id string, version int) (account.DTO, account.WriteClient, error) {
	accounts.mutex.Lock()
	defer accounts.mutex.Unlock()
	if id != accounts.credential.CredentialID || (version != 0 && version != accounts.credential.Version) {
		return account.DTO{}, nil, &account.Failure{Code: "replaced"}
	}
	return accounts.credential, accounts.client, nil
}
func (accounts *fakeAccounts) Observe(ctx context.Context, id string, version int, capability account.Capability, err error) error {
	if account.FilterError(err) != nil && account.FilterError(err).Code == "missing_scope" {
		accounts.denied.Add(1)
	}
	return nil
}

type visibility struct{}

func (visibility) Visible(context.Context, string, string, string) (bool, error) { return true, nil }

type cache struct{ done chan string }

func (cache cache) Invalidate(id string) { cache.done <- id }

type fakeClock struct {
	now  time.Time
	wait func(context.Context) error
}

func (clock *fakeClock) Now() time.Time { return clock.now }
func (clock *fakeClock) Wait(ctx context.Context, delay time.Duration) error {
	if clock.wait != nil {
		return clock.wait(ctx)
	}
	return ctx.Err()
}

type failingAudit struct{}

func (failingAudit) Intent(operation.Action) (audit.Event, error) {
	return audit.Event{}, errors.New("audit failure")
}

type fixture struct {
	store    *sqlite.Store
	accounts *fakeAccounts
	client   *fakeClient
	cache    cache
	clock    *fakeClock
	service  *operation.Service
}

func setup(t *testing.T, options operation.Options) *fixture {
	t.Helper()
	directory, err := os.MkdirTemp(".", "operation-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	store, err := sqlite.Open(context.Background(), filepath.Join(directory, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err = store.CreateCredential(context.Background(), account.Record{ID: "credential", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	accounts := &fakeAccounts{credential: account.DTO{CredentialID: "credential", OrgID: "org", Version: 1, Enabled: true, Status: "valid", Capabilities: account.Capabilities{Write: account.Declared, Delete: account.Declared}}, client: client}
	clock := &fakeClock{now: now}
	options.Clock = clock
	invalidator := cache{done: make(chan string, 64)}
	service, err := operation.New(store, accounts, visibility{}, invalidator, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Stop)
	return &fixture{store: store, accounts: accounts, client: client, cache: invalidator, clock: clock, service: service}
}
func intent(id string) operation.Intent {
	return operation.Intent{ClientRequestID: id, CredentialID: "credential", Kind: "start", MachineID: "machine", Body: []byte(`{"host":"host"}`)}
}
func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("event deadline exceeded")
		var zero T
		return zero
	}
}
func start(t *testing.T, fixture *fixture) {
	t.Helper()
	if err := fixture.service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func submit(t *testing.T, fixture *fixture, value operation.Intent) operation.Action {
	t.Helper()
	action, err := fixture.service.Submit(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	return action
}
func read(t *testing.T, fixture *fixture, id string) operation.Action {
	t.Helper()
	action, err := fixture.store.ActionByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func TestDurableIntentIdempotencyAndConflict(t *testing.T) {
	fixture := setup(t, operation.Options{})
	action := submit(t, fixture, intent("one"))
	if len(fixture.client.requests) != 0 || action.State != "pending" {
		t.Fatal("request before execution")
	}
	same := submit(t, fixture, intent("one"))
	if same.ID != action.ID {
		t.Fatal("not idempotent")
	}
	changed := intent("one")
	changed.Body = []byte(`{}`)
	if _, err := fixture.service.Submit(context.Background(), changed); err == nil {
		t.Fatal("missing conflict")
	}
	events, err := fixture.store.ReadAuditEvents(context.Background(), 0, 100)
	if err != nil || len(events) != 1 {
		t.Fatal("intent audit missing")
	}
	start(t, fixture)
	waitForCompletion(t, fixture)
	fixture.service.Stop()
	if result := read(t, fixture, action.ID); result.State != "completed" || result.UpstreamState != "succeeded" {
		t.Fatalf("result: %+v", result)
	}
}

func TestResponseLossRetriesSameRequest(t *testing.T) {
	fixture := setup(t, operation.Options{})
	fixture.clock.wait = func(context.Context) error { return nil }
	var calls int
	fixture.client.submit = func(ctx context.Context, value request) (instabox.Operation, error) {
		calls++
		if calls == 1 {
			return instabox.Operation{}, errors.New("connection lost")
		}
		return instabox.Operation{ID: "remote", State: "queued"}, nil
	}
	action := submit(t, fixture, intent("one"))
	start(t, fixture)
	waitForCompletion(t, fixture)
	fixture.service.Stop()
	requests := fixture.client.requests
	if len(requests) != 2 || requests[0].key != requests[1].key || string(requests[0].body) != string(requests[1].body) {
		t.Fatal("request changed on retry")
	}
	result := read(t, fixture, action.ID)
	if result.Method != "POST" || result.Path != "/v1/machines/machine/start" || string(result.RequestBody) != `{"host":"host"}` || result.Attempts != 2 {
		t.Fatalf("immutable request missing: %+v", result)
	}
}

func TestRestartRecovery(t *testing.T) {
	for _, state := range []string{"pending", "submitting", "tracking"} {
		t.Run(state, func(t *testing.T) {
			fixture := setup(t, operation.Options{})
			fixture.clock.wait = func(context.Context) error { return nil }
			action := submit(t, fixture, intent("one"))
			action.State = state
			if state == "submitting" {
				action.Attempts = 1
			}
			if state == "tracking" {
				action.UpstreamOperationID = "remote"
			}
			if err := fixture.store.UpdateAction(context.Background(), action); err != nil {
				t.Fatal(err)
			}
			start(t, fixture)
			waitForCompletion(t, fixture)
			fixture.service.Stop()
			if (state == "tracking" && len(fixture.client.requests) != 0) || (state != "tracking" && len(fixture.client.requests) != 1) {
				t.Fatal("incorrect restart request count")
			}
			if state != "tracking" && fixture.client.requests[0].key != action.UpstreamIdempotencyKey {
				t.Fatal("restart changed key")
			}
		})
	}
}

func TestRemoteFailureNeedsNewKey(t *testing.T) {
	fixture := setup(t, operation.Options{})
	fixture.client.poll = func(context.Context) (instabox.Operation, error) {
		return instabox.Operation{ID: "remote", State: "failed", Error: &instabox.Error{Code: "no_capacity"}}, nil
	}
	first := submit(t, fixture, intent("one"))
	start(t, fixture)
	waitForCompletion(t, fixture)
	second := submit(t, fixture, intent("two"))
	waitForCompletion(t, fixture)
	fixture.service.Stop()
	if first.UpstreamIdempotencyKey == second.UpstreamIdempotencyKey || len(fixture.client.requests) != 2 {
		t.Fatal("failed action retried or key reused")
	}
	if result := read(t, fixture, first.ID); result.State != "completed" || result.UpstreamState != "failed" {
		t.Fatal("failure not terminal")
	}
}

func TestRetryGuards(t *testing.T) {
	for _, reason := range []string{"version", "expired", "budget", "missing_scope", "no_capacity"} {
		t.Run(reason, func(t *testing.T) {
			fixture := setup(t, operation.Options{MaxAttempts: 2})
			fixture.clock.wait = func(context.Context) error { return nil }
			fixture.client.submit = func(ctx context.Context, value request) (instabox.Operation, error) {
				if reason == "missing_scope" {
					return instabox.Operation{}, &instabox.APIError{StatusCode: 403, Code: "missing_scope"}
				}
				if reason == "no_capacity" {
					return instabox.Operation{}, &instabox.APIError{StatusCode: 409, Code: "no_capacity"}
				}
				return instabox.Operation{}, errors.New("lost")
			}
			action := submit(t, fixture, intent("one"))
			if reason == "version" {
				fixture.accounts.credential.Version = 2
			}
			if reason == "expired" {
				fixture.clock.now = fixture.clock.now.Add(7 * 24 * time.Hour)
			}
			start(t, fixture)
			// Stop is a barrier after workers have observed a deterministic storage update.
			waitForAction(t, fixture, action.ID, func(value operation.Action) bool { return value.State == "unknown" || value.State == "completed" })
			fixture.service.Stop()
			result := read(t, fixture, action.ID)
			expected := 0
			if reason == "budget" {
				expected = 2
			}
			if reason == "missing_scope" || reason == "no_capacity" {
				expected = 1
			}
			if len(fixture.client.requests) != expected {
				t.Fatalf("calls=%d expected=%d", len(fixture.client.requests), expected)
			}
			if reason == "missing_scope" && fixture.accounts.denied.Load() != 1 {
				t.Fatal("missing scope not observed")
			}
			if reason == "budget" && result.State != "unknown" {
				t.Fatal("budget became remote failure")
			}
		})
	}
}

func waitForAction(t *testing.T, fixture *fixture, id string, predicate func(operation.Action) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		if predicate(read(t, fixture, id)) {
			return
		}
	}
	t.Fatal("action did not reach expected state")
}

func TestDeleteGatesAndConfirmation(t *testing.T) {
	for _, state := range []account.CapabilityState{account.Unknown, account.Denied, account.Declared, account.Verified} {
		t.Run(string(state), func(t *testing.T) {
			fixture := setup(t, operation.Options{})
			fixture.accounts.credential.Capabilities.Delete = state
			value := intent("delete")
			value.Kind = "delete"
			value.Body = nil
			value.ConfirmName = "name"
			action, err := fixture.service.Submit(context.Background(), value)
			if state == account.Unknown || state == account.Denied {
				if err == nil {
					t.Fatal("delete gate bypassed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			mismatch := value
			mismatch.ClientRequestID = "mismatch"
			mismatch.ConfirmName = "wrong"
			if _, err := fixture.service.Submit(context.Background(), mismatch); err == nil {
				t.Fatal("name not checked")
			}
			start(t, fixture)
			waitForCompletion(t, fixture)
			fixture.service.Stop()
			if len(fixture.client.requests) != 1 || fixture.client.requests[0].confirmation != "machine" || action.Method != "DELETE" {
				t.Fatal("missing delete header")
			}
		})
	}
}

func TestAuditFailureSendsNothing(t *testing.T) {
	fixture := setup(t, operation.Options{Audit: failingAudit{}})
	start(t, fixture)
	if _, err := fixture.service.Submit(context.Background(), intent("one")); err == nil {
		t.Fatal("audit failure accepted")
	}
	fixture.service.Stop()
	if len(fixture.client.requests) != 0 {
		t.Fatal("sent unaudited request")
	}
}

func TestPollingQuotaAndStop(t *testing.T) {
	fixture := setup(t, operation.Options{})
	entered := make(chan struct{}, 16)
	exited := make(chan struct{}, 16)
	var active, maximum atomic.Int32
	fixture.client.poll = func(ctx context.Context) (instabox.Operation, error) {
		count := active.Add(1)
		for {
			previous := maximum.Load()
			if count <= previous || maximum.CompareAndSwap(previous, count) {
				break
			}
		}
		entered <- struct{}{}
		<-ctx.Done()
		active.Add(-1)
		exited <- struct{}{}
		return instabox.Operation{}, ctx.Err()
	}
	for index := 0; index < 12; index++ {
		submit(t, fixture, intent(fmt.Sprintf("request-%d", index)))
	}
	start(t, fixture)
	for index := 0; index < 4; index++ {
		receive(t, entered)
	}
	fixture.service.Stop()
	if maximum.Load() != 4 || active.Load() != 0 {
		t.Fatalf("poll count max=%d active=%d", maximum.Load(), active.Load())
	}
	for index := 0; index < 4; index++ {
		receive(t, exited)
	}
}

func TestCrashAfterSendBeforeResponseRestartsSameKey(t *testing.T) {
	fixture := setup(t, operation.Options{})
	sent := make(chan struct{}, 1)
	fixture.client.submit = func(ctx context.Context, value request) (instabox.Operation, error) {
		sent <- struct{}{}
		<-ctx.Done()
		return instabox.Operation{}, ctx.Err()
	}
	action := submit(t, fixture, intent("one"))
	start(t, fixture)
	receive(t, sent)
	fixture.service.Stop()
	if result := read(t, fixture, action.ID); result.State != "submitting" {
		t.Fatal("uncertain submission not durable")
	}
	fixture.clock.wait = func(context.Context) error { return nil }
	fixture.client.submit = nil
	start(t, fixture)
	waitForCompletion(t, fixture)
	fixture.service.Stop()
	if len(fixture.client.requests) != 2 || fixture.client.requests[0].key != fixture.client.requests[1].key || string(fixture.client.requests[0].body) != string(fixture.client.requests[1].body) {
		t.Fatal("restart changed request")
	}
}

func TestAllMachineActions(t *testing.T) {
	for _, kind := range []string{"create", "start", "stop", "suspend", "resume", "delete"} {
		t.Run(kind, func(t *testing.T) {
			fixture := setup(t, operation.Options{})
			value := intent(kind)
			value.Kind = kind
			value.Body = []byte(`{}`)
			if kind == "delete" {
				value.Body = nil
				value.ConfirmName = "name"
			}
			if kind == "create" {
				value.MachineID = ""
				value.Body = []byte(`{"name":"new-machine"}`)
			}
			action := submit(t, fixture, value)
			start(t, fixture)
			waitForCompletion(t, fixture)
			fixture.service.Stop()
			if len(fixture.client.requests) != 1 || fixture.client.requests[0].kind != kind || read(t, fixture, action.ID).State != "completed" {
				t.Fatal("action was not executed")
			}
		})
	}
}

func TestVersionChangesDuringRetryDelay(t *testing.T) {
	fixture := setup(t, operation.Options{})
	fixture.client.submit = func(context.Context, request) (instabox.Operation, error) {
		return instabox.Operation{}, errors.New("lost")
	}
	fixture.clock.wait = func(context.Context) error {
		fixture.accounts.mutex.Lock()
		fixture.accounts.credential.Version++
		fixture.accounts.mutex.Unlock()
		return nil
	}
	action := submit(t, fixture, intent("one"))
	start(t, fixture)
	waitForAction(t, fixture, action.ID, func(value operation.Action) bool { return value.State == "unknown" })
	fixture.service.Stop()
	if len(fixture.client.requests) != 1 {
		t.Fatal("sent after replacement")
	}
}

func TestStopCancelsRetryAfterAndRecoveryRetainsDelay(t *testing.T) {
	fixture := setup(t, operation.Options{})
	retryAfter := int64(120)
	fixture.client.submit = func(context.Context, request) (instabox.Operation, error) {
		return instabox.Operation{}, &instabox.APIError{StatusCode: 429, Retryable: true, RetryAfterSeconds: &retryAfter}
	}
	waiting := make(chan time.Duration, 2)
	fixture.clock.wait = func(ctx context.Context) error { waiting <- 120 * time.Second; <-ctx.Done(); return ctx.Err() }
	action := submit(t, fixture, intent("one"))
	start(t, fixture)
	receive(t, waiting)
	fixture.service.Stop()
	saved := read(t, fixture, action.ID)
	if saved.NextAttemptAt.Before(fixture.clock.now.Add(120*time.Second)) || saved.State != "pending" {
		t.Fatal("retry-after not durable")
	}
	start(t, fixture)
	receive(t, waiting)
	fixture.service.Stop()
	if len(fixture.client.requests) != 1 {
		t.Fatal("restart ignored retry-after")
	}
}

type rejectingRepository struct {
	*sqlite.Store
	attempted chan struct{}
}

func (repository rejectingRepository) UpdateAction(ctx context.Context, action operation.Action) error {
	repository.attempted <- struct{}{}
	return errors.New("crash before submission")
}

func TestCrashBeforeSubmittingCommitSendsNothing(t *testing.T) {
	fixture := setup(t, operation.Options{})
	attempted := make(chan struct{}, 1)
	service, err := operation.New(rejectingRepository{Store: fixture.store, attempted: attempted}, fixture.accounts, visibility{}, fixture.cache, operation.Options{Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	action := submit(t, fixture, intent("one"))
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	receive(t, attempted)
	service.Stop()
	if len(fixture.client.requests) != 0 || read(t, fixture, action.ID).State != "pending" {
		t.Fatal("sent before submitting commit")
	}
}

func waitForCompletion(t *testing.T, fixture *fixture) {
	t.Helper()
	receive(t, fixture.cache.done)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		actions, err := fixture.store.ListUnfinishedActions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(actions) == 0 {
			return
		}
	}
	t.Fatal("action did not complete after cache invalidation")
}
