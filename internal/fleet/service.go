package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
	"sort"
	"sync"
	"time"
)

// Service owns bounded concurrency, single-flight requests, and account caches.
type Service struct {
	accounts Accounts
	options  Options
	slots    chan struct{}
	mutex    sync.Mutex
	cache    map[string]*entry
}

// New constructs an isolated cache; zero options use production defaults.
func New(accounts Accounts, options Options) (*Service, error) {
	if options.Concurrency == 0 {
		options.Concurrency = 8
	}
	if options.AccountTimeout == 0 {
		options.AccountTimeout = 5 * time.Second
	}
	if options.OverallTimeout == 0 {
		options.OverallTimeout = 20 * time.Second
	}
	if options.MachineTTL == 0 {
		options.MachineTTL = 10 * time.Second
	}
	if options.UsageTTL == 0 {
		options.UsageTTL = 60 * time.Second
	}
	if options.MaxPages == 0 {
		options.MaxPages = 100
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if accounts == nil || options.Concurrency < 1 || options.AccountTimeout < 0 || options.OverallTimeout < 0 || options.MachineTTL < 0 || options.UsageTTL < 0 || options.MaxPages < 1 {
		return nil, errors.New("fleet: invalid options")
	}
	return &Service{accounts: accounts, options: options, slots: make(chan struct{}, options.Concurrency), cache: make(map[string]*entry)}, nil
}

// Invalidate removes both caches and prevents in-flight results from repopulating them.
func (service *Service) Invalidate(credentialID string) {
	service.mutex.Lock()
	previous := service.cache[credentialID]
	if previous != nil {
		value := previous.value
		if value.machineError == nil || (value.machineError.RetryAfterSeconds == 0 && value.machineError.Code != "unauthorized") {
			value.machineError = nil
			value.machineRetryAt = time.Time{}
		}
		if value.usageError == nil || (value.usageError.RetryAfterSeconds == 0 && value.usageError.Code != "unauthorized") {
			value.usageError = nil
			value.usageRetryAt = time.Time{}
		}
		service.cache[credentialID] = &entry{version: previous.version, value: snapshot{machineError: value.machineError, machineRetryAt: value.machineRetryAt, usageError: value.usageError, usageRetryAt: value.usageRetryAt}}
	}
	service.mutex.Unlock()
}

func (service *Service) now() time.Time { return service.options.Now().UTC() }

func due(now, fetchedAt, retryAt time.Time, ttl time.Duration) bool {
	return !now.Before(retryAt) && (fetchedAt.IsZero() || !now.Before(fetchedAt.Add(ttl)))
}

func retryAt(now time.Time, failure *account.Failure) time.Time {
	seconds := failure.RetryAfterSeconds
	if seconds < 1 {
		seconds = 1
	}
	const maxSeconds = int64((1<<63 - 1) / int64(time.Second))
	if seconds > maxSeconds {
		seconds = maxSeconds
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func (service *Service) cached(ctx context.Context, credential account.DTO) snapshot {
	service.mutex.Lock()
	cached := service.cache[credential.CredentialID]
	if cached == nil || cached.version != credential.Version {
		cached = &entry{version: credential.Version}
		service.cache[credential.CredentialID] = cached
	}
	if credential.Status != "valid" {
		result := cached.value
		result.machineError = &account.Failure{Code: "unauthorized"}
		result.usageError = &account.Failure{Code: "unauthorized"}
		service.mutex.Unlock()
		return result
	}
	if cached.done != nil {
		done := cached.done
		service.mutex.Unlock()
		select {
		case <-done:
			service.mutex.Lock()
			result := cached.value
			service.mutex.Unlock()
			return result
		case <-ctx.Done():
			service.mutex.Lock()
			result := cached.value
			service.mutex.Unlock()
			result.machineError = account.FilterError(ctx.Err())
			result.usageError = account.FilterError(ctx.Err())
			return result
		}
	}
	now := service.now()
	previous := cached.value
	for _, backoff := range []struct {
		failure *account.Failure
		until   time.Time
	}{{failure: previous.machineError, until: previous.machineRetryAt}, {failure: previous.usageError, until: previous.usageRetryAt}} {
		if backoff.failure != nil && backoff.failure.RetryAfterSeconds > 0 && now.Before(backoff.until) {
			if previous.fetchedAt.IsZero() || !now.Before(previous.fetchedAt.Add(service.options.MachineTTL)) {
				previous.machineError = backoff.failure
			}
			service.mutex.Unlock()
			return previous
		}
	}
	fetchMachines := due(now, previous.fetchedAt, previous.machineRetryAt, service.options.MachineTTL)
	fetchUsage := due(now, previous.usageFetchedAt, previous.usageRetryAt, service.options.UsageTTL)
	if (previous.machineError != nil && previous.machineError.Code == "unauthorized") || (previous.usageError != nil && previous.usageError.Code == "unauthorized") {
		fetchMachines = true
		fetchUsage = true
	}
	if !fetchMachines && !fetchUsage {
		service.mutex.Unlock()
		return previous
	}
	done := make(chan struct{})
	cached.done = done
	service.mutex.Unlock()
	result := service.fetch(ctx, credential, previous, fetchMachines, fetchUsage)
	service.mutex.Lock()
	if service.cache[credential.CredentialID] != cached {
		// Discard the in-flight result, but keep showing the previous snapshot
		// as stale so the account's machines do not vanish from the table.
		result = previous
		result.machineError = &account.Failure{Code: "invalidated", Retryable: true}
		result.usageError = &account.Failure{Code: "invalidated", Retryable: true}
	}
	cached.value = result
	cached.done = nil
	close(done)
	service.mutex.Unlock()
	return result
}

func (service *Service) fetch(parent context.Context, credential account.DTO, previous snapshot, machines, usage bool) snapshot {
	result := previous
	select {
	case service.slots <- struct{}{}:
		defer func() { <-service.slots }()
	case <-parent.Done():
		if machines {
			result.machineError = account.FilterError(parent.Err())
		}
		if usage {
			result.usageError = account.FilterError(parent.Err())
		}
		return result
	}
	// Queueing is bounded by the overall deadline. The account's own timeout
	// starts here so that slow accounts holding slots cannot time out the rest.
	ctx, cancel := context.WithTimeout(parent, service.options.AccountTimeout)
	defer cancel()
	client, err := service.accounts.ClientForCredential(ctx, credential.CredentialID)
	if err != nil {
		failure := account.FilterError(err)
		if machines {
			result.machineError = failure
			result.machineRetryAt = retryAt(service.now(), failure)
		}
		if usage {
			result.usageError = failure
			result.usageRetryAt = retryAt(service.now(), failure)
		}
		return result
	}
	if machines {
		values, failure := service.listMachines(ctx, client, credential.OrgID)
		result.machineError = account.FilterError(failure)
		if failure == nil {
			result.machines = values
			result.fetchedAt = service.now()
			result.machineRetryAt = time.Time{}
		} else {
			result.machineRetryAt = retryAt(service.now(), result.machineError)
		}
	}
	if result.machineError != nil && (result.machineError.Code == "unauthorized" || result.machineError.RetryAfterSeconds > 0) {
		result.usageError = result.machineError
		result.usageRetryAt = result.machineRetryAt
		return result
	}
	if usage {
		value, failure := client.GetUsage(ctx)
		result.usageError = account.FilterError(failure)
		if failure == nil {
			result.usage = &value
			result.usageFetchedAt = service.now()
			result.usageRetryAt = time.Time{}
		} else {
			result.usageRetryAt = retryAt(service.now(), result.usageError)
		}
	}
	return result
}

func (service *Service) listMachines(ctx context.Context, client account.Client, defaultOrgID string) ([]machineSnapshot, error) {
	result := []machineSnapshot{}
	cursor := ""
	seen := map[string]bool{}
	for pageNumber := 0; pageNumber < service.options.MaxPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := client.ListMachines(ctx, cursor)
		if err != nil {
			return nil, err
		}
		for _, value := range page.Machines {
			if value.State == instabox.MachineStateDeleted {
				continue
			}
			orgID := value.OrgID
			if orgID == "" {
				orgID = defaultOrgID
			}
			result = append(result, machineSnapshot{id: value.ID, orgID: orgID, name: value.Name, state: value.State, ready: value.Runtime.Ready})
		}
		if page.NextCursor == nil || *page.NextCursor == "" {
			return result, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, &instabox.APIError{Kind: "pagination_cycle"}
		}
		seen[cursor] = true
	}
	return nil, &instabox.APIError{Kind: "page_limit"}
}

// GetFleet queries every enabled credential with independent deadlines and failure isolation.
func (service *Service) GetFleet(parent context.Context) (Response, error) {
	ctx, cancel := context.WithTimeout(parent, service.options.OverallTimeout)
	defer cancel()
	credentials, err := service.accounts.List(ctx)
	response := Response{Machines: []Machine{}, Accounts: []Account{}}
	if err != nil {
		response.Partial = true
		return response, account.FilterError(err)
	}
	enabled := make([]account.DTO, 0, len(credentials))
	for _, credential := range credentials {
		if credential.Enabled && credential.Status != "deleted" {
			enabled = append(enabled, credential)
		}
	}
	sort.Slice(enabled, func(first, second int) bool { return enabled[first].CredentialID < enabled[second].CredentialID })
	snapshots := make([]snapshot, len(enabled))
	var workers sync.WaitGroup
	for index, credential := range enabled {
		workers.Add(1)
		go func() { defer workers.Done(); snapshots[index] = service.cached(ctx, credential) }()
	}
	workers.Wait()
	failed := 0
	merged := map[[2]string]*Machine{}
	for index, credential := range enabled {
		value := snapshots[index]
		status := accountStatus(credential.CredentialID, value)
		response.Accounts = append(response.Accounts, status)
		if status.Error != nil || status.UsageError != nil || status.Stale || !status.Complete {
			response.Partial = true
		}
		if status.Error != nil || !status.Complete {
			failed++
		}
		merge(merged, credential, value, status.Stale)
	}
	for _, machine := range merged {
		response.Machines = append(response.Machines, *machine)
	}
	sort.Slice(response.Machines, func(first, second int) bool {
		if response.Machines[first].OrgID != response.Machines[second].OrgID {
			return response.Machines[first].OrgID < response.Machines[second].OrgID
		}
		return response.Machines[first].ID < response.Machines[second].ID
	})
	if ctx.Err() != nil {
		response.Partial = true
		return response, ctx.Err()
	}
	if len(enabled) > 0 && failed == len(enabled) {
		return response, &account.Failure{Code: "all_accounts_failed", Retryable: true}
	}
	return response, nil
}

func timestamp(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func accountStatus(id string, value snapshot) Account {
	var usage *instabox.Usage
	if value.usage != nil {
		encoded, _ := json.Marshal(value.usage)
		_ = json.Unmarshal(encoded, &usage)
	}
	return Account{CredentialID: id, Complete: !value.fetchedAt.IsZero(), Stale: value.machineError != nil, FetchedAt: timestamp(value.fetchedAt), Error: copyFailure(value.machineError), Usage: usage, UsageFetchedAt: timestamp(value.usageFetchedAt), UsageError: copyFailure(value.usageError)}
}

func merge(merged map[[2]string]*Machine, credential account.DTO, value snapshot, stale bool) {
	for _, candidate := range value.machines {
		key := [2]string{candidate.orgID, candidate.id}
		canDelete := credential.CanDelete()
		if (value.machineError != nil && value.machineError.Code == "unauthorized") || (value.usageError != nil && value.usageError.Code == "unauthorized") {
			canDelete = false
		}
		source := Source{CredentialID: credential.CredentialID, Stale: stale, FetchedAt: value.fetchedAt, CanDelete: canDelete}
		existing := merged[key]
		if existing == nil {
			merged[key] = &Machine{ID: candidate.id, OrgID: candidate.orgID, Name: candidate.name, State: candidate.state, Ready: candidate.ready, Stale: stale, FetchedAt: value.fetchedAt, Sources: []Source{source}}
			continue
		}
		if existing.State != candidate.state || existing.Name != candidate.name || existing.Ready != candidate.ready {
			existing.Conflict = true
		}
		duplicate := false
		for _, previous := range existing.Sources {
			if previous.CredentialID == credential.CredentialID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			existing.Sources = append(existing.Sources, source)
		}
		if (existing.Stale && !stale) || (existing.Stale == stale && value.fetchedAt.After(existing.FetchedAt)) {
			existing.Name = candidate.name
			existing.State = candidate.state
			existing.Ready = candidate.ready
			existing.Stale = stale
			existing.FetchedAt = value.fetchedAt
		}
	}
}

// Visible verifies visibility from a complete, fresh snapshot for this credential only.
func (service *Service) Visible(parent context.Context, credentialID, orgID, machineID string) (bool, error) {
	ctx, cancel := context.WithTimeout(parent, service.options.OverallTimeout)
	defer cancel()
	credentials, err := service.accounts.List(ctx)
	if err != nil {
		return false, account.FilterError(err)
	}
	for _, credential := range credentials {
		if credential.CredentialID != credentialID || !credential.Enabled || credential.Status != "valid" {
			continue
		}
		value := service.cached(ctx, credential)
		if value.machineError != nil {
			return false, value.machineError
		}
		if value.fetchedAt.IsZero() {
			return false, &account.Failure{Code: "incomplete", Retryable: true}
		}
		for _, machine := range value.machines {
			if machine.id == machineID && machine.orgID == orgID {
				return true, nil
			}
		}
		return false, nil
	}
	return false, &account.Failure{Code: "credential_unavailable"}
}

func copyFailure(failure *account.Failure) *account.Failure {
	if failure == nil {
		return nil
	}
	copy := *failure
	return &copy
}
