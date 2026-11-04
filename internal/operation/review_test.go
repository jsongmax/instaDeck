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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateRejectsSecretValues(t *testing.T) {
	for _, body := range []string{`{"env":{"TOKEN":"private-value"}}`, `{"directSecrets":[{"secret":"secret-reference","env":"TOKEN"}]}`, `{"metadata":{"token":"private-value"}}`} {
		t.Run(body, func(t *testing.T) {
			fixture := setup(t, operation.Options{})
			value := intent("secret")
			value.Kind = "create"
			value.MachineID = ""
			value.Body = []byte(body)
			_, err := fixture.service.Submit(context.Background(), value)
			var failure *operation.Failure
			if !errors.As(err, &failure) || failure.Code != "invalid_body" {
				t.Fatal("secret-bearing create was accepted and persisted")
			}
			if _, found, err := fixture.store.FindAction(context.Background(), value.ClientRequestID); err != nil || found {
				t.Fatal("rejected body persisted")
			}
		})
	}
	fixture := setup(t, operation.Options{})
	value := intent("references")
	value.Kind = "create"
	value.MachineID = ""
	value.Body = []byte(`{"secrets":["proxy-name"],"envSecrets":["env-name"],"env":{},"directSecrets":[],"metadata":{}}`)
	submit(t, fixture, value)
}

type controlledVisibility struct {
	check func(context.Context, string) (bool, error)
}

func (visibility controlledVisibility) Visible(ctx context.Context, credentialID, orgID, machineID string) (bool, error) {
	return visibility.check(ctx, machineID)
}

func TestSlowValidationDoesNotBlockOtherSubmitOrStop(t *testing.T) {
	fixture := setup(t, operation.Options{})
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	visible := controlledVisibility{check: func(ctx context.Context, machineID string) (bool, error) {
		if machineID == "slow" {
			entered <- struct{}{}
			<-release
		}
		return true, nil
	}}
	service, err := operation.New(fixture.store, fixture.accounts, visible, fixture.cache, operation.Options{Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	slowDone := make(chan struct{})
	defer func() { close(release); <-slowDone }()
	go func() {
		value := intent("slow")
		value.MachineID = "slow"
		_, _ = service.Submit(context.Background(), value)
		close(slowDone)
	}()
	receive(t, entered)
	fastDone := make(chan error, 1)
	go func() { _, err := service.Submit(context.Background(), intent("fast")); fastDone <- err }()
	select {
	case err := <-fastDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("slow visibility blocked unrelated submission")
	}
	stopped := make(chan struct{})
	go func() { service.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Error("slow visibility blocked Stop")
	}
}

func TestConcurrentSameRequestCreatesAndSendsOnce(t *testing.T) {
	fixture := setup(t, operation.Options{})
	entered := make(chan struct{}, 12)
	release := make(chan struct{})
	visible := controlledVisibility{check: func(context.Context, string) (bool, error) { entered <- struct{}{}; <-release; return true, nil }}
	service, err := operation.New(fixture.store, fixture.accounts, visible, fixture.cache, operation.Options{Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	responses := make(chan operation.Action, 12)
	failures := make(chan error, 12)
	for index := 0; index < 12; index++ {
		go func() {
			action, err := service.Submit(context.Background(), intent("same"))
			responses <- action
			failures <- err
		}()
	}
	for index := 0; index < 12; index++ {
		receive(t, entered)
	}
	close(release)
	first := receive(t, responses)
	for index := 1; index < 12; index++ {
		if receive(t, responses).ID != first.ID {
			t.Fatal("multiple actions for same intent")
		}
	}
	for index := 0; index < 12; index++ {
		if err := receive(t, failures); err != nil {
			t.Fatal(err)
		}
	}
	waitForAction(t, fixture, first.ID, func(action operation.Action) bool { return action.State == "completed" })
	service.Stop()
	if len(fixture.client.requests) != 1 {
		t.Fatal("intent sent more than once")
	}
	events, err := fixture.store.ReadAuditEvents(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	intents, outcomes := 0, 0
	for _, record := range events {
		switch record.Event.Type {
		case "operation.intent":
			intents++
		case "operation.result":
			outcomes++
		}
	}
	if intents != 1 || outcomes != 1 {
		t.Fatalf("audit duplicated: intents=%d outcomes=%d", intents, outcomes)
	}
}

type controlledAccounts struct {
	*fakeAccounts
	acquire func(context.Context, string, int) (account.DTO, account.WriteClient, error)
}

func (accounts controlledAccounts) WriteClientForCredential(ctx context.Context, id string, version int) (account.DTO, account.WriteClient, error) {
	return accounts.acquire(ctx, id, version)
}

func TestTransientCredentialLookupRecovers(t *testing.T) {
	for _, recoveringSubmission := range []bool{false, true} {
		t.Run(fmt.Sprint(recoveringSubmission), func(t *testing.T) {
			fixture := setup(t, operation.Options{})
			fixture.clock.wait = func(context.Context) error { return nil }
			action := submit(t, fixture, intent("transient"))
			if recoveringSubmission {
				action.State = "submitting"
				action.Attempts = 1
				if err := fixture.store.UpdateAction(context.Background(), action); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			failureAt := 1
			if recoveringSubmission {
				failureAt = 2
			}
			accounts := controlledAccounts{fakeAccounts: fixture.accounts, acquire: func(ctx context.Context, id string, version int) (account.DTO, account.WriteClient, error) {
				calls++
				if calls == failureAt {
					return account.DTO{}, nil, &account.Failure{Code: "storage_unavailable", Retryable: true}
				}
				return fixture.accounts.WriteClientForCredential(ctx, id, version)
			}}
			service, err := operation.New(fixture.store, accounts, visibility{}, fixture.cache, operation.Options{Clock: fixture.clock})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Stop()
			if err = service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			waitForAction(t, fixture, action.ID, func(action operation.Action) bool { return action.State == "unknown" || action.State == "completed" })
			service.Stop()
			if result := read(t, fixture, action.ID); result.State != "completed" {
				t.Fatalf("transient storage error permanently changed state to %s (%s)", result.State, result.ErrorCode)
			}
		})
	}
}

func TestFailureCodesArePreservedAndSanitized(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		for _, code := range []string{"no_capacity", strings.Repeat("a", 64), "", strings.Repeat("a", 65), "Bad_Code", "bad-code", "secret\nvalue", "秘密"} {
			t.Run(fmt.Sprintf("%t/%q", terminal, code), func(t *testing.T) {
				fixture := setup(t, operation.Options{})
				fallback := "upstream_rejected"
				if terminal {
					fallback = "upstream_failed"
					fixture.client.poll = func(context.Context) (instabox.Operation, error) {
						return instabox.Operation{ID: "remote", State: "failed", Error: &instabox.Error{Code: code, Message: "private-message", Hint: "private-hint"}}, nil
					}
				} else {
					fixture.client.submit = func(context.Context, request) (instabox.Operation, error) {
						return instabox.Operation{}, &instabox.APIError{StatusCode: 409, Code: code}
					}
				}
				action := submit(t, fixture, intent("code"))
				start(t, fixture)
				waitForAction(t, fixture, action.ID, func(action operation.Action) bool { return action.State == "completed" })
				fixture.service.Stop()
				result := read(t, fixture, action.ID)
				expected := fallback
				if code == "no_capacity" || code == strings.Repeat("a", 64) {
					expected = code
				}
				if result.ErrorCode != expected {
					t.Fatalf("error code = %q, want %q", result.ErrorCode, expected)
				}
				if strings.Contains(fmt.Sprintf("%+v", result), "private-") {
					t.Fatal("upstream message or hint persisted")
				}
			})
		}
	}
}

func TestRetryableClientErrorUsesSameKey(t *testing.T) {
	fixture := setup(t, operation.Options{})
	fixture.clock.wait = func(context.Context) error { return nil }
	calls := 0
	fixture.client.submit = func(context.Context, request) (instabox.Operation, error) {
		calls++
		if calls == 1 {
			return instabox.Operation{}, &instabox.APIError{StatusCode: 409, Code: "operation_conflict", Retryable: true}
		}
		return instabox.Operation{ID: "remote", State: "queued"}, nil
	}
	action := submit(t, fixture, intent("retryable"))
	start(t, fixture)
	waitForAction(t, fixture, action.ID, func(action operation.Action) bool { return action.State == "completed" })
	fixture.service.Stop()
	if len(fixture.client.requests) != 2 {
		t.Fatalf("retryable 409 was treated as rejection; requests=%d", len(fixture.client.requests))
	}
	if fixture.client.requests[0].key != fixture.client.requests[1].key || string(fixture.client.requests[0].body) != string(fixture.client.requests[1].body) {
		t.Fatal("retry changed request")
	}
}

type measuredClock struct {
	mutex sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (clock *measuredClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *measuredClock) advance(duration time.Duration) {
	clock.mutex.Lock()
	clock.now = clock.now.Add(duration)
	clock.mutex.Unlock()
}

func (clock *measuredClock) Wait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clock.mutex.Lock()
	clock.waits = append(clock.waits, delay)
	clock.now = clock.now.Add(delay)
	clock.mutex.Unlock()
	return nil
}

type observingRepository struct {
	*sqlite.Store
	updates atomic.Int32
}

func (repository *observingRepository) UpdateAction(ctx context.Context, action operation.Action) error {
	repository.updates.Add(1)
	return repository.Store.UpdateAction(ctx, action)
}

func (repository *observingRepository) FinishAction(ctx context.Context, action operation.Action, event audit.Event) error {
	repository.updates.Add(1)
	return repository.Store.FinishAction(ctx, action, event)
}

func TestShortPollsWaitAndUnchangedProgressDoesNotWrite(t *testing.T) {
	fixture := setup(t, operation.Options{})
	clock := &measuredClock{now: fixture.clock.now}
	repository := &observingRepository{Store: fixture.store}
	polls := 0
	fixture.client.poll = func(context.Context) (instabox.Operation, error) {
		polls++
		if polls == 1 {
			clock.advance(200 * time.Millisecond)
		}
		remote := instabox.Operation{ID: "remote", State: "queued"}
		if polls >= 3 {
			remote.State = "running"
			remote.Phase = "booting"
		}
		if polls == 5 {
			remote.State = "succeeded"
		}
		return remote, nil
	}
	service, err := operation.New(repository, fixture.accounts, visibility{}, fixture.cache, operation.Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	action := submit(t, fixture, intent("short-polls"))
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForAction(t, fixture, action.ID, func(action operation.Action) bool { return action.State == "completed" })
	service.Stop()
	expected := []time.Duration{800 * time.Millisecond, time.Second, time.Second, time.Second}
	if !reflect.DeepEqual(clock.waits, expected) {
		t.Errorf("short polls did not wait: got %v, want %v", clock.waits, expected)
	}
	if updates := repository.updates.Load(); updates != 4 {
		t.Errorf("unchanged progress wrote database: updates=%d, want 4", updates)
	}
}

func TestTrackingFailuresBackOffAndSuccessResets(t *testing.T) {
	fixture := setup(t, operation.Options{})
	clock := &measuredClock{now: fixture.clock.now}
	polls := 0
	fixture.client.poll = func(context.Context) (instabox.Operation, error) {
		polls++
		if polls == 4 {
			clock.advance(2 * time.Second)
			return instabox.Operation{ID: "remote", State: "running"}, nil
		}
		if polls == 7 {
			return instabox.Operation{ID: "remote", State: "succeeded"}, nil
		}
		return instabox.Operation{}, errors.New("transport unavailable")
	}
	service, err := operation.New(fixture.store, fixture.accounts, visibility{}, fixture.cache, operation.Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	action := submit(t, fixture, intent("tracking-errors"))
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForAction(t, fixture, action.ID, func(action operation.Action) bool { return action.State == "completed" })
	service.Stop()
	expected := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, time.Second, 2 * time.Second}
	if len(clock.waits) != len(expected) {
		t.Fatalf("waits=%v", clock.waits)
	}
	for index, base := range expected {
		if clock.waits[index] < base || clock.waits[index] >= 2*base {
			t.Errorf("tracking delay[%d]=%s, want [%s,%s)", index, clock.waits[index], base, 2*base)
		}
	}
}

func TestAcceptedOperationInvalidatesBeforeTerminal(t *testing.T) {
	fixture := setup(t, operation.Options{})
	entered := make(chan struct{}, 1)
	fixture.client.poll = func(ctx context.Context) (instabox.Operation, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return instabox.Operation{}, ctx.Err()
	}
	action := submit(t, fixture, intent("accepted"))
	start(t, fixture)
	receive(t, entered)
	select {
	case id := <-fixture.cache.done:
		if id != action.CredentialID {
			t.Fatal("wrong account invalidated")
		}
	default:
		t.Error("accepted operation did not invalidate fleet cache")
	}
	fixture.service.Stop()
	if result := read(t, fixture, action.ID); result.State != "tracking" {
		t.Fatal("expected persisted tracking state")
	}
}

func TestConcurrentDifferentInputsConflict(t *testing.T) {
	fixture := setup(t, operation.Options{})
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	visible := controlledVisibility{check: func(context.Context, string) (bool, error) { entered <- struct{}{}; <-release; return true, nil }}
	service, err := operation.New(fixture.store, fixture.accounts, visible, fixture.cache, operation.Options{Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Stop()
	results := make(chan error, 2)
	for _, body := range []string{`{"host":"one"}`, `{"host":"two"}`} {
		go func(body string) {
			value := intent("conflict")
			value.Body = []byte(body)
			_, err := service.Submit(context.Background(), value)
			results <- err
		}(body)
	}
	receive(t, entered)
	receive(t, entered)
	close(release)
	successes, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		err := receive(t, results)
		if err == nil {
			successes++
			continue
		}
		var failure *operation.Failure
		if errors.As(err, &failure) && failure.Code == "request_conflict" {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

type cancellationRepository struct {
	*sqlite.Store
	updates atomic.Int32
}

func (repository *cancellationRepository) UpdateAction(ctx context.Context, action operation.Action) error {
	repository.updates.Add(1)
	return repository.Store.UpdateAction(context.Background(), action)
}

func TestCredentialLookupCancellationPreservesPending(t *testing.T) {
	fixture := setup(t, operation.Options{})
	action := submit(t, fixture, intent("cancel-acquire"))
	entered := make(chan struct{}, 1)
	accounts := controlledAccounts{fakeAccounts: fixture.accounts, acquire: func(context.Context, string, int) (account.DTO, account.WriteClient, error) {
		entered <- struct{}{}
		return account.DTO{}, nil, context.Canceled
	}}
	repository := &cancellationRepository{Store: fixture.store}
	service, err := operation.New(repository, accounts, visibility{}, fixture.cache, operation.Options{Clock: fixture.clock})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	receive(t, entered)
	service.Stop()
	if repository.updates.Load() != 0 || read(t, fixture, action.ID).State != "pending" {
		t.Fatal("context cancellation changed durable state")
	}
}

func TestAcceptedAndTerminalBothInvalidateCache(t *testing.T) {
	fixture := setup(t, operation.Options{})
	action := submit(t, fixture, intent("cache-events"))
	start(t, fixture)
	waitForAction(t, fixture, action.ID, func(action operation.Action) bool { return action.State == "completed" })
	fixture.service.Stop()
	if len(fixture.cache.done) != 2 {
		t.Fatalf("invalidations=%d, want acceptance and terminal", len(fixture.cache.done))
	}
}
