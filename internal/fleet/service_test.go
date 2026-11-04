package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	list  func(context.Context, string) (instabox.MachineList, error)
	usage func(context.Context) (instabox.Usage, error)
}

func (client *fakeClient) GetMe(context.Context) (instabox.Me, error) {
	return instabox.Me{UserID: 1, OrgID: "org"}, nil
}

func (client *fakeClient) ListMachines(ctx context.Context, cursor string) (instabox.MachineList, error) {
	if client.list != nil {
		return client.list(ctx, cursor)
	}
	return instabox.MachineList{Machines: []instabox.Machine{}}, nil
}

func (client *fakeClient) GetUsage(ctx context.Context) (instabox.Usage, error) {
	if client.usage != nil {
		return client.usage(ctx)
	}
	return instabox.Usage{Machines: 1}, nil
}

type fakeAccounts struct {
	mutex       sync.Mutex
	credentials []account.DTO
	clients     map[string]*fakeClient
	listed      chan struct{}
}

func (provider *fakeAccounts) List(context.Context) ([]account.DTO, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	if provider.listed != nil {
		provider.listed <- struct{}{}
	}
	return append([]account.DTO(nil), provider.credentials...), nil
}

func (provider *fakeAccounts) ClientForCredential(_ context.Context, id string) (account.Client, error) {
	provider.mutex.Lock()
	defer provider.mutex.Unlock()
	return provider.clients[id], nil
}

func credential(id, orgID string) account.DTO {
	return account.DTO{CredentialID: id, OrgID: orgID, Enabled: true, Status: "valid", Version: 1, Capabilities: account.Capabilities{Read: account.Unknown, Write: account.Unknown, Delete: account.Unknown}}
}

func newService(t *testing.T, provider *fakeAccounts, options Options) *Service {
	t.Helper()
	service, err := New(provider, options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func machine(id, orgID, name, state string, ready bool) instabox.Machine {
	return instabox.Machine{ID: id, OrgID: orgID, Name: name, State: instabox.MachineState(state), Runtime: instabox.Runtime{Ready: ready}}
}

func page(values ...instabox.Machine) instabox.MachineList {
	return instabox.MachineList{Machines: values}
}

func await[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("blocked test did not finish")
		var zero T
		return zero
	}
}

func TestPartialFailureNeverSuccessfulAndAllFailed(t *testing.T) {
	provider := &fakeAccounts{credentials: []account.DTO{credential("good", "org"), credential("bad", "other")}, clients: map[string]*fakeClient{
		"good": {list: func(context.Context, string) (instabox.MachineList, error) {
			return page(machine("box", "org", "good", "future_state", true)), nil
		}},
		"bad": {list: func(context.Context, string) (instabox.MachineList, error) {
			return instabox.MachineList{}, &instabox.APIError{StatusCode: 503, Code: "private-key-upstream-text", Retryable: true}
		}},
	}}
	service := newService(t, provider, Options{})
	result, err := service.GetFleet(context.Background())
	if err != nil || !result.Partial || len(result.Machines) != 1 || result.Machines[0].State != "future_state" {
		t.Fatal("partial isolation", err)
	}
	if result.Accounts[0].Complete || !result.Accounts[0].Stale || result.Accounts[0].FetchedAt != nil || result.Accounts[0].Error.Code != "unavailable" {
		t.Fatal("failed account treated as empty success")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private-key-upstream-text") {
		t.Fatal("upstream text leaked")
	}
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	if decoded["partial"] != true {
		t.Fatal("missing response metadata")
	}
	provider.mutex.Lock()
	provider.credentials = provider.credentials[1:]
	provider.mutex.Unlock()
	result, err = service.GetFleet(context.Background())
	var failure *account.Failure
	if !errors.As(err, &failure) || failure.Code != "all_accounts_failed" || !result.Partial || len(result.Accounts) != 1 || len(result.Machines) != 0 {
		t.Fatal("all-failed response", err)
	}
}

func TestSeparateCacheTTLsStaleAndVisibility(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	var listCalls, usageCalls atomic.Int32
	var fail atomic.Bool
	provider := &fakeAccounts{credentials: []account.DTO{credential("key", "org")}, clients: map[string]*fakeClient{"key": {
		list: func(context.Context, string) (instabox.MachineList, error) {
			listCalls.Add(1)
			if fail.Load() {
				return instabox.MachineList{}, errors.New("private-upstream-text")
			}
			return page(machine("box", "org", "cached", "running", true)), nil
		},
		usage: func(context.Context) (instabox.Usage, error) {
			usageCalls.Add(1)
			if fail.Load() {
				return instabox.Usage{}, errors.New("private-upstream-text")
			}
			return instabox.Usage{Machines: 3, Limits: instabox.UsageLimits{Unlimited: []string{"maxMachines"}}}, nil
		},
	}}}
	service := newService(t, provider, Options{Now: func() time.Time { return now }})
	initial, err := service.GetFleet(context.Background())
	if err != nil || initial.Partial || !initial.Accounts[0].Complete {
		t.Fatal("initial fetch", err)
	}
	fetchedAt := *initial.Accounts[0].FetchedAt
	initial.Accounts[0].Usage.Limits.Unlimited[0] = "mutated"
	now = now.Add(11 * time.Second)
	fresh, err := service.GetFleet(context.Background())
	if err != nil || fresh.Accounts[0].Usage.Limits.Unlimited[0] != "maxMachines" || listCalls.Load() != 2 || usageCalls.Load() != 1 {
		t.Fatal("TTLs or cache ownership")
	}
	if !fresh.Accounts[0].FetchedAt.After(fetchedAt) || !fresh.Accounts[0].UsageFetchedAt.Equal(fetchedAt) {
		t.Fatal("timestamps")
	}
	now = now.Add(61 * time.Second)
	fail.Store(true)
	stale, err := service.GetFleet(context.Background())
	if err == nil || !stale.Partial || len(stale.Machines) != 1 || !stale.Machines[0].Stale || !stale.Machines[0].Sources[0].Stale || !stale.Accounts[0].Complete || stale.Accounts[0].Usage == nil || stale.Accounts[0].UsageError == nil {
		t.Fatal("old snapshot missing", err)
	}
	if !stale.Machines[0].FetchedAt.Equal(*fresh.Accounts[0].FetchedAt) {
		t.Fatal("failed fetch changed timestamp")
	}
	if visible, err := service.Visible(context.Background(), "key", "org", "box"); err == nil || visible {
		t.Fatal("stale visibility accepted")
	}
	fail.Store(false)
	service.Invalidate("key")
	if visible, err := service.Visible(context.Background(), "key", "org", "box"); err != nil || !visible {
		t.Fatal("visibility failed", err)
	}
	for _, request := range [][3]string{{"key", "other", "box"}, {"key", "org", "absent"}, {"other-key", "org", "box"}} {
		if visible, _ := service.Visible(context.Background(), request[0], request[1], request[2]); visible {
			t.Fatal("visibility crossed credential or organization")
		}
	}
}

func TestPaginationFailureCycleAndPageLimit(t *testing.T) {
	for _, mode := range []string{"midway", "cycle", "limit", "success"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			provider := &fakeAccounts{credentials: []account.DTO{credential("key", "org")}, clients: map[string]*fakeClient{"key": {list: func(_ context.Context, cursor string) (instabox.MachineList, error) {
				number := calls.Add(1)
				if mode == "midway" && cursor != "" {
					return instabox.MachineList{}, errors.New("private-upstream-text")
				}
				next := fmt.Sprintf("page-%d", number)
				if mode == "cycle" {
					next = "cycle"
				}
				if mode == "success" && number == 2 {
					return page(machine("two", "org", "two", "stopped", false)), nil
				}
				return instabox.MachineList{Machines: []instabox.Machine{machine("one", "org", "one", "running", true)}, NextCursor: &next}, nil
			}}}}
			service := newService(t, provider, Options{MaxPages: 3})
			result, err := service.GetFleet(context.Background())
			if mode == "success" {
				if err != nil || len(result.Machines) != 2 || !result.Accounts[0].Complete {
					t.Fatal("pagination failed", err)
				}
			} else if err == nil || len(result.Machines) != 0 || result.Accounts[0].Complete {
				t.Fatal("partial page results exposed")
			}
			if mode == "limit" && calls.Load() != 3 {
				t.Fatal("unbounded pagination")
			}
		})
	}
}

func TestMergeConflictDeletedAndSeparateOrganizations(t *testing.T) {
	first := credential("a", "org")
	first.Capabilities.Delete = account.Declared
	provider := &fakeAccounts{credentials: []account.DTO{first, credential("b", "org"), credential("c", "other")}, clients: map[string]*fakeClient{
		"a": {list: func(context.Context, string) (instabox.MachineList, error) {
			return page(machine("box", "org", "first", "running", true), machine("gone", "org", "gone", "deleted", false)), nil
		}},
		"b": {list: func(context.Context, string) (instabox.MachineList, error) {
			return page(machine("box", "org", "second", "stopped", false)), nil
		}},
		"c": {list: func(context.Context, string) (instabox.MachineList, error) {
			return page(machine("box", "other", "third", "running", true)), nil
		}},
	}}
	now := time.Unix(1000, 0)
	service := newService(t, provider, Options{Now: func() time.Time { return now }})
	result, err := service.GetFleet(context.Background())
	if err != nil || len(result.Machines) != 2 || len(result.Accounts) != 3 {
		t.Fatal("Key count or deduplication", err)
	}
	combined := result.Machines[0]
	if !combined.Conflict || len(combined.Sources) != 2 || combined.Name != "first" || combined.State != "running" || !combined.Ready || !combined.Sources[0].CanDelete || combined.Sources[1].CanDelete {
		t.Fatal("incoherent merge")
	}
	if result.Machines[1].Conflict || result.Machines[1].OrgID != "other" {
		t.Fatal("merged across organizations")
	}
}

func TestConcurrentFleetQueriesCoalesce(t *testing.T) {
	const callers = 12
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var machineCalls, usageCalls atomic.Int32
	provider := &fakeAccounts{credentials: []account.DTO{credential("key", "org")}, listed: make(chan struct{}, callers), clients: map[string]*fakeClient{"key": {
		list: func(ctx context.Context, _ string) (instabox.MachineList, error) {
			machineCalls.Add(1)
			started <- struct{}{}
			select {
			case <-release:
				return page(machine("box", "org", "test", "running", true)), nil
			case <-ctx.Done():
				return instabox.MachineList{}, ctx.Err()
			}
		},
		usage: func(context.Context) (instabox.Usage, error) { usageCalls.Add(1); return instabox.Usage{}, nil },
	}}}
	service := newService(t, provider, Options{})
	completed := make(chan error, callers)
	for caller := 0; caller < callers; caller++ {
		go func() {
			result, err := service.GetFleet(context.Background())
			if err == nil && len(result.Machines) != 1 {
				err = errors.New("missing snapshot")
			}
			completed <- err
		}()
	}
	await(t, started)
	for caller := 0; caller < callers; caller++ {
		await(t, provider.listed)
	}
	close(release)
	for caller := 0; caller < callers; caller++ {
		if err := await(t, completed); err != nil {
			t.Fatal(err)
		}
	}
	if machineCalls.Load() != 1 || usageCalls.Load() != 1 {
		t.Fatal("duplicate upstream requests", machineCalls.Load(), usageCalls.Load())
	}
}

func TestSlowAccountDoesNotBlockFastAccount(t *testing.T) {
	slowStarted := make(chan struct{})
	fastFinished := make(chan struct{})
	releaseSlow := make(chan struct{})
	provider := &fakeAccounts{credentials: []account.DTO{credential("slow", "org"), credential("fast", "other")}, clients: map[string]*fakeClient{
		"slow": {list: func(ctx context.Context, _ string) (instabox.MachineList, error) {
			close(slowStarted)
			select {
			case <-releaseSlow:
				return instabox.MachineList{}, context.DeadlineExceeded
			case <-ctx.Done():
				return instabox.MachineList{}, ctx.Err()
			}
		}},
		"fast": {list: func(context.Context, string) (instabox.MachineList, error) {
			close(fastFinished)
			return page(machine("box", "other", "fast", "running", true)), nil
		}},
	}}
	service := newService(t, provider, Options{})
	finished := make(chan Response, 1)
	go func() { response, _ := service.GetFleet(context.Background()); finished <- response }()
	await(t, slowStarted)
	await(t, fastFinished)
	visible, visibilityErr := service.Visible(context.Background(), "fast", "other", "box")
	if visibilityErr != nil || !visible {
		t.Fatal("fast snapshot unavailable while slow account blocked", visibilityErr)
	}
	close(releaseSlow)
	result := await(t, finished)
	if len(result.Machines) != 1 || result.Machines[0].Name != "fast" || !result.Accounts[0].Complete || result.Accounts[1].Complete || !result.Partial {
		t.Fatal("slow account affected fast account")
	}
}

func TestGlobalConcurrencyLimitAndCancellation(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			const limit = 3
			const total = 12
			entered := make(chan struct{}, total*2)
			release := make(chan struct{})
			var active, peak atomic.Int32
			request := func(ctx context.Context) error {
				current := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); current > old; old = peak.Load() {
					if peak.CompareAndSwap(old, current) {
						break
					}
				}
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			provider := &fakeAccounts{clients: map[string]*fakeClient{}}
			for index := 0; index < total; index++ {
				id := fmt.Sprintf("key-%02d", index)
				provider.credentials = append(provider.credentials, credential(id, "org"))
				provider.clients[id] = &fakeClient{list: func(ctx context.Context, _ string) (instabox.MachineList, error) { return page(), request(ctx) }, usage: func(ctx context.Context) (instabox.Usage, error) { return instabox.Usage{}, request(ctx) }}
			}
			service := newService(t, provider, Options{Concurrency: limit})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completed := make(chan error, 2)
			for caller := 0; caller < 2; caller++ {
				go func() { _, err := service.GetFleet(ctx); completed <- err }()
			}
			for index := 0; index < limit; index++ {
				await(t, entered)
			}
			if active.Load() != limit {
				t.Fatal("configured concurrency not reached")
			}
			if cancelRequest {
				cancel()
			} else {
				close(release)
			}
			for caller := 0; caller < 2; caller++ {
				err := await(t, completed)
				if cancelRequest && !errors.Is(err, context.Canceled) {
					t.Fatal("context identity lost", err)
				}
				if !cancelRequest && err != nil {
					t.Fatal(err)
				}
			}
			if peak.Load() > limit || active.Load() != 0 {
				t.Fatal("concurrency exceeded or worker leaked", peak.Load(), active.Load())
			}
		})
	}
}

func TestDeadlinesBoundBlockedRequests(t *testing.T) {
	for _, overall := range []bool{false, true} {
		t.Run(fmt.Sprint(overall), func(t *testing.T) {
			exited := make(chan struct{})
			provider := &fakeAccounts{credentials: []account.DTO{credential("key", "org")}, clients: map[string]*fakeClient{"key": {list: func(ctx context.Context, _ string) (instabox.MachineList, error) {
				defer close(exited)
				<-ctx.Done()
				return instabox.MachineList{}, ctx.Err()
			}}}}
			options := Options{AccountTimeout: 50 * time.Millisecond, OverallTimeout: time.Second}
			if overall {
				options.AccountTimeout = time.Second
				options.OverallTimeout = 50 * time.Millisecond
			}
			service := newService(t, provider, options)
			result, err := service.GetFleet(context.Background())
			await(t, exited)
			if err == nil || !result.Partial || result.Accounts[0].Complete {
				t.Fatal("deadline ignored")
			}
			if overall && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("overall deadline identity", err)
			}
		})
	}
}

func TestRetryAfterAndUnauthorizedStopRequests(t *testing.T) {
	now := time.Unix(1000, 0)
	var calls, usageCalls atomic.Int32
	var reject atomic.Bool
	reject.Store(true)
	seconds := int64(30)
	provider := &fakeAccounts{credentials: []account.DTO{credential("key", "org")}, clients: map[string]*fakeClient{}}
	provider.clients["key"] = &fakeClient{
		list: func(context.Context, string) (instabox.MachineList, error) {
			calls.Add(1)
			if reject.Load() {
				return instabox.MachineList{}, &instabox.APIError{StatusCode: 429, RetryAfterSeconds: &seconds}
			}
			return page(), nil
		},
		usage: func(context.Context) (instabox.Usage, error) { usageCalls.Add(1); return instabox.Usage{}, nil },
	}
	service := newService(t, provider, Options{Now: func() time.Time { return now }})
	_, _ = service.GetFleet(context.Background())
	service.Invalidate("key")
	for query := 0; query < 3; query++ {
		now = now.Add(9 * time.Second)
		_, _ = service.GetFleet(context.Background())
	}
	if calls.Load() != 1 || usageCalls.Load() != 0 {
		t.Fatal("retryAfter not respected")
	}
	now = now.Add(4 * time.Second)
	reject.Store(false)
	if result, err := service.GetFleet(context.Background()); err != nil || !result.Accounts[0].Complete {
		t.Fatal("backoff did not expire", err)
	}
	service.Invalidate("key")
	provider.clients["key"].list = func(context.Context, string) (instabox.MachineList, error) {
		calls.Add(1)
		provider.mutex.Lock()
		provider.credentials[0].Status = "invalid"
		provider.mutex.Unlock()
		return instabox.MachineList{}, &instabox.APIError{StatusCode: 401}
	}
	_, _ = service.GetFleet(context.Background())
	before := calls.Load()
	for query := 0; query < 3; query++ {
		now = now.Add(time.Hour)
		result, _ := service.GetFleet(context.Background())
		if result.Accounts[0].Error.Code != "unauthorized" {
			t.Fatal("invalid key status missing")
		}
	}
	if calls.Load() != before {
		t.Fatal("401 retried")
	}
	provider.mutex.Lock()
	provider.credentials[0].Status = "valid"
	provider.credentials[0].Version++
	provider.mutex.Unlock()
	provider.clients["key"].list = func(context.Context, string) (instabox.MachineList, error) { calls.Add(1); return page(), nil }
	if _, err := service.GetFleet(context.Background()); err != nil || calls.Load() != before+1 {
		t.Fatal("replacement did not reset cache", err)
	}
}

func TestInvalidationDiscardsInflightSnapshot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	provider := &fakeAccounts{credentials: []account.DTO{credential("key", "org")}, clients: map[string]*fakeClient{"key": {list: func(ctx context.Context, _ string) (instabox.MachineList, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
				return page(machine("old", "org", "old", "running", true)), nil
			case <-ctx.Done():
				return instabox.MachineList{}, ctx.Err()
			}
		}
		return page(machine("new", "org", "new", "running", true)), nil
	}}}}
	service := newService(t, provider, Options{})
	completed := make(chan Response, 1)
	go func() { response, _ := service.GetFleet(context.Background()); completed <- response }()
	await(t, started)
	service.Invalidate("key")
	fresh, err := service.GetFleet(context.Background())
	if err != nil || len(fresh.Machines) != 1 || fresh.Machines[0].ID != "new" {
		t.Fatal("new snapshot missing", err)
	}
	close(release)
	old := await(t, completed)
	if len(old.Machines) != 0 || !old.Partial {
		t.Fatal("invalidated in-flight result exposed")
	}
	final, err := service.GetFleet(context.Background())
	if err != nil || final.Machines[0].ID != "new" || calls.Load() != 2 {
		t.Fatal("old flight repopulated cache", err)
	}
}
