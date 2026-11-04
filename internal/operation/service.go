package operation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/instabox"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
func (systemClock) Wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Service owns one process's workers and independent polling quota.
type Service struct {
	repository Repository
	accounts   Accounts
	visibility Visibility
	cache      Cache
	options    Options
	slots      chan struct{}
	mutex      sync.Mutex
	cancel     context.CancelFunc
	ctx        context.Context
	workers    sync.WaitGroup
	active     map[string]bool
	stopping   chan struct{}
}

// New constructs an idle coordinator; Start enables execution and recovery.
func New(repository Repository, accounts Accounts, visibility Visibility, cache Cache, options Options) (*Service, error) {
	if options.PollConcurrency == 0 {
		options.PollConcurrency = 4
	}
	if options.MaxAttempts == 0 {
		options.MaxAttempts = 5
	}
	if options.BaseDelay == 0 {
		options.BaseDelay = time.Second
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if repository == nil || accounts == nil || visibility == nil || cache == nil || options.MaxAttempts < 1 || options.PollConcurrency < 1 || options.BaseDelay < 0 {
		return nil, &Failure{Code: "invalid_options"}
	}
	return &Service{repository: repository, accounts: accounts, visibility: visibility, cache: cache, options: options, slots: make(chan struct{}, options.PollConcurrency), active: make(map[string]bool)}, nil
}

func identifier() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", &Failure{Code: "random_unavailable"}
	}
	return hex.EncodeToString(value), nil
}

func fixedRequest(intent Intent) (string, string, []byte, error) {
	method, path := "POST", "/v1/machines"
	var body any
	switch intent.Kind {
	case "create":
		body = &instabox.CreateMachineRequest{}
	case "start":
		body = &instabox.StartMachineRequest{}
	case "stop":
		body = &instabox.StopMachineRequest{}
	case "resume":
		body = &instabox.ResumeMachineRequest{}
	case "suspend":
		body = &struct{}{}
	case "delete":
		method = "DELETE"
	default:
		return "", "", nil, &Failure{Code: "invalid_intent"}
	}
	if intent.Kind != "create" {
		if strings.TrimSpace(intent.MachineID) == "" || strings.ContainsAny(intent.MachineID, "/\\?#\r\n") {
			return "", "", nil, &Failure{Code: "invalid_intent"}
		}
		path += "/" + url.PathEscape(intent.MachineID)
		if intent.Kind != "delete" {
			path += "/" + intent.Kind
		}
	}
	if body == nil {
		if len(intent.Body) > 0 {
			return "", "", nil, &Failure{Code: "invalid_body"}
		}
		return method, path, nil, nil
	}
	input := intent.Body
	if len(input) == 0 {
		input = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		return "", "", nil, &Failure{Code: "invalid_body"}
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return "", "", nil, &Failure{Code: "invalid_body"}
	}
	if create, ok := body.(*instabox.CreateMachineRequest); ok && (len(create.Env) > 0 || len(create.DirectSecrets) > 0 || len(create.Metadata) > 0) {
		return "", "", nil, &Failure{Code: "invalid_body"}
	}
	payload, err := json.Marshal(body)
	return method, path, payload, err
}

func intentDigest(intent Intent) string {
	encoded, _ := json.Marshal(intent)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// Submit atomically records an intent and audit event before scheduling any request.
func (service *Service) Submit(ctx context.Context, intent Intent) (Action, error) {
	if intent.ClientRequestID == "" || intent.CredentialID == "" {
		return Action{}, &Failure{Code: "invalid_intent"}
	}
	digest := intentDigest(intent)
	previous, found, err := service.repository.FindAction(ctx, intent.ClientRequestID)
	if err != nil {
		return Action{}, &Failure{Code: "storage_unavailable"}
	}
	if found {
		if previous.Digest != digest {
			return Action{}, &Failure{Code: "request_conflict"}
		}
		return previous, nil
	}
	method, path, body, err := fixedRequest(intent)
	if err != nil {
		return Action{}, err
	}
	credential, client, err := service.accounts.WriteClientForCredential(ctx, intent.CredentialID, 0)
	if err != nil {
		return Action{}, &Failure{Code: "credential_unavailable"}
	}
	capability := credential.Capabilities.Write
	if intent.Kind == "delete" {
		capability = credential.Capabilities.Delete
	}
	if capability == account.Denied || (intent.Kind == "delete" && !credential.CanDelete()) {
		return Action{}, &Failure{Code: "capability_denied"}
	}
	if intent.Kind != "create" {
		visible, err := service.visibility.Visible(ctx, intent.CredentialID, credential.OrgID, intent.MachineID)
		if err != nil || !visible {
			return Action{}, &Failure{Code: "machine_not_visible"}
		}
	}
	if intent.Kind == "delete" {
		machine, err := client.GetMachine(ctx, intent.MachineID)
		if err != nil || machine.ID != intent.MachineID || machine.Name != intent.ConfirmName || intent.ConfirmName == "" {
			return Action{}, &Failure{Code: "confirmation_mismatch"}
		}
	}
	id, err := identifier()
	if err != nil {
		return Action{}, err
	}
	key, err := identifier()
	if err != nil {
		return Action{}, err
	}
	now := service.options.Clock.Now().UTC()
	action := Action{ID: id, CredentialID: intent.CredentialID, MachineID: intent.MachineID, Kind: intent.Kind, RequestBody: body, UpstreamIdempotencyKey: key, ClientRequestID: intent.ClientRequestID, Method: method, Path: path, Digest: digest, OrgID: credential.OrgID, CredentialVersion: credential.Version, State: "pending", CreatedAt: now, UpdatedAt: now}
	event := audit.Event{Time: now, Type: "operation.intent", CredentialID: action.CredentialID, MachineID: action.MachineID, ActionID: id, Result: "pending", RequestID: intent.ClientRequestID}
	if service.options.Audit != nil {
		event, err = service.options.Audit.Intent(action)
		if err != nil {
			return Action{}, &Failure{Code: "audit_unavailable"}
		}
	}
	event.SubjectID = intent.AdministratorID
	event.SourceAddress = intent.SourceAddress
	if intent.RequestID != "" {
		event.RequestID = intent.RequestID
	}
	if err = service.repository.CreateActionIntent(ctx, action, event); err != nil {
		existing, found, lookupErr := service.repository.FindAction(ctx, intent.ClientRequestID)
		if lookupErr == nil && found {
			if existing.Digest != digest {
				return Action{}, &Failure{Code: "request_conflict"}
			}
			return existing, nil
		}
		return Action{}, &Failure{Code: "storage_unavailable"}
	}
	service.mutex.Lock()
	service.launchLocked(action)
	service.mutex.Unlock()
	return action, nil
}

// Get returns a durable action without exposing storage errors.
func (service *Service) Get(ctx context.Context, id string) (Action, error) {
	action, err := service.repository.ActionByID(ctx, id)
	if err != nil {
		return Action{}, &Failure{Code: "action_unavailable"}
	}
	return action, nil
}

// Start recovers durable pending, submitting, and tracking actions.
func (service *Service) Start(parent context.Context) error {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.cancel != nil {
		return &Failure{Code: "already_started"}
	}
	actions, err := service.repository.ListUnfinishedActions(parent)
	if err != nil {
		return &Failure{Code: "storage_unavailable"}
	}
	service.ctx, service.cancel = context.WithCancel(parent)
	for _, action := range actions {
		service.launchLocked(action)
	}
	return nil
}

func (service *Service) launchLocked(action Action) {
	if service.cancel == nil || service.stopping != nil || service.ctx.Err() != nil || service.active[action.ID] || action.State == "unknown" || action.State == "completed" {
		return
	}
	service.active[action.ID] = true
	service.workers.Add(1)
	go func() {
		defer service.workers.Done()
		defer func() { service.mutex.Lock(); delete(service.active, action.ID); service.mutex.Unlock() }()
		service.run(service.ctx, action)
	}()
}

// Stop cancels requests and waits until all owned workers exit.
func (service *Service) Stop() {
	service.mutex.Lock()
	if service.stopping != nil {
		done := service.stopping
		service.mutex.Unlock()
		<-done
		return
	}
	if service.cancel == nil {
		service.mutex.Unlock()
		return
	}
	done := make(chan struct{})
	service.stopping = done
	service.cancel()
	service.mutex.Unlock()
	service.workers.Wait()
	service.mutex.Lock()
	service.cancel = nil
	service.stopping = nil
	close(done)
	service.mutex.Unlock()
}

// saveAttempts bounds retries of a state write. A lost write would otherwise stop
// tracking the action until the next restart.
const saveAttempts = 4

func (service *Service) save(ctx context.Context, action *Action) bool {
	action.UpdatedAt = service.options.Clock.Now().UTC()
	final := action.State == "completed" || action.State == "unknown"
	for attempt := 0; attempt < saveAttempts; attempt++ {
		var err error
		if final {
			err = service.repository.FinishAction(ctx, *action, outcomeEvent(*action))
		} else {
			err = service.repository.UpdateAction(ctx, *action)
		}
		if err == nil {
			return true
		}
		if ctx.Err() != nil || attempt == saveAttempts-1 {
			return false
		}
		if service.options.Clock.Wait(ctx, time.Second<<attempt) != nil {
			return false
		}
	}
	return false
}

func outcomeEvent(action Action) audit.Event {
	result := "success"
	if action.State == "unknown" {
		result = "unknown"
	} else if action.ErrorCode != "" {
		result = "failure"
	}
	event := audit.Event{Time: action.UpdatedAt, Type: "operation.result", CredentialID: action.CredentialID, MachineID: action.MachineID, ActionID: action.ID, Result: result, RequestID: action.ClientRequestID}
	event.SetDetail(action.ErrorCode)
	return event
}

func (service *Service) unknown(ctx context.Context, action *Action, code string) {
	action.State = "unknown"
	action.ErrorCode = code
	service.save(ctx, action)
}

func (service *Service) delay(attempt int, err error) time.Duration {
	delay := service.options.BaseDelay
	for index := 1; index < attempt && delay < time.Minute; index++ {
		delay *= 2
	}
	if delay > time.Minute {
		delay = time.Minute
	}
	random := make([]byte, 1)
	if _, randomErr := rand.Read(random); randomErr == nil {
		delay += time.Duration(random[0]) * delay / 256
	}
	failure := account.FilterError(err)
	if failure != nil && failure.RetryAfterSeconds > 0 {
		seconds := failure.RetryAfterSeconds
		if seconds > 86400*7 {
			seconds = 86400 * 7
		}
		if minimum := time.Duration(seconds) * time.Second; minimum > delay {
			delay = minimum
		}
	}
	return delay
}

func (service *Service) pause(ctx context.Context, attempt int, err error) bool {
	return service.options.Clock.Wait(ctx, service.delay(attempt, err)) == nil
}

func submitUpstream(ctx context.Context, client account.WriteClient, action Action) (instabox.Operation, error) {
	switch action.Kind {
	case "create":
		var body instabox.CreateMachineRequest
		_ = json.Unmarshal(action.RequestBody, &body)
		return client.CreateMachine(ctx, body, action.UpstreamIdempotencyKey)
	case "start":
		var body instabox.StartMachineRequest
		_ = json.Unmarshal(action.RequestBody, &body)
		return client.StartMachine(ctx, action.MachineID, body, action.UpstreamIdempotencyKey)
	case "stop":
		var body instabox.StopMachineRequest
		_ = json.Unmarshal(action.RequestBody, &body)
		return client.StopMachine(ctx, action.MachineID, body, action.UpstreamIdempotencyKey)
	case "resume":
		var body instabox.ResumeMachineRequest
		_ = json.Unmarshal(action.RequestBody, &body)
		return client.ResumeMachine(ctx, action.MachineID, body, action.UpstreamIdempotencyKey)
	case "suspend":
		return client.SuspendMachine(ctx, action.MachineID, action.UpstreamIdempotencyKey)
	case "delete":
		return client.DeleteMachine(ctx, action.MachineID, action.UpstreamIdempotencyKey, action.MachineID)
	}
	return instabox.Operation{}, &Failure{Code: "invalid_intent"}
}

func (service *Service) observe(ctx context.Context, action Action, err error) {
	capability := account.Write
	if action.Kind == "delete" {
		capability = account.Delete
	}
	_ = service.accounts.Observe(ctx, action.CredentialID, action.CredentialVersion, capability, err)
}

func safeErrorCode(code, fallback string) string {
	if len(code) == 0 || len(code) > 64 {
		return fallback
	}
	for _, character := range code {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' {
			return fallback
		}
	}
	return code
}

func rejectionCode(err error) string {
	var upstream *instabox.APIError
	if errors.As(err, &upstream) {
		return safeErrorCode(upstream.Code, "upstream_rejected")
	}
	return "upstream_rejected"
}

// Limits share status 429 with rate limiting, but retrying cannot help until the
// user frees something up.
func limitReached(code string) bool {
	switch code {
	case "quota_exceeded", "template_limit", "spend_limit_reached", "credit_exhausted":
		return true
	}
	return false
}

func rejected(err error) bool {
	var upstream *instabox.APIError
	if errors.As(err, &upstream) {
		if limitReached(upstream.Code) {
			return true
		}
		return upstream.StatusCode >= 400 && upstream.StatusCode < 500 && upstream.StatusCode != 429 && !upstream.Retryable
	}
	failure := account.FilterError(err)
	return failure != nil && !failure.Retryable
}

func credentialUnavailable(err error) bool {
	var failure *account.Failure
	if !errors.As(err, &failure) {
		return false
	}
	switch failure.Code {
	case "replaced", "credential_unavailable", "disabled", "deleted", "decryption_failed", "unauthorized", "capability_denied":
		return true
	default:
		return false
	}
}

func (service *Service) acquireClient(ctx context.Context, action *Action) (account.WriteClient, error) {
	for attempt := 1; ctx.Err() == nil; attempt++ {
		_, client, err := service.accounts.WriteClientForCredential(ctx, action.CredentialID, action.CredentialVersion)
		if err == nil {
			return client, nil
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if credentialUnavailable(err) {
			service.unknown(ctx, action, "credential_changed")
			return nil, err
		}
		if !service.pause(ctx, attempt, err) {
			return nil, ctx.Err()
		}
	}
	return nil, ctx.Err()
}

func (service *Service) run(ctx context.Context, action Action) {
	trackingFailures := 0
	for ctx.Err() == nil {
		client, err := service.acquireClient(ctx, &action)
		if err != nil {
			return
		}
		if action.State != "tracking" {
			if !service.options.Clock.Now().Before(action.CreatedAt.Add(7 * 24 * time.Hour)) {
				service.unknown(ctx, &action, "key_expired")
				return
			}
			if action.Attempts >= service.options.MaxAttempts {
				service.unknown(ctx, &action, "retry_exhausted")
				return
			}
			if action.State == "submitting" || !action.NextAttemptAt.IsZero() {
				if action.NextAttemptAt.IsZero() {
					action.NextAttemptAt = service.options.Clock.Now().Add(service.delay(action.Attempts, nil))
					if !service.save(ctx, &action) {
						return
					}
				}
				delay := action.NextAttemptAt.Sub(service.options.Clock.Now())
				if delay > 0 && service.options.Clock.Wait(ctx, delay) != nil {
					return
				}
				client, err = service.acquireClient(ctx, &action)
				if err != nil {
					return
				}
				if !service.options.Clock.Now().Before(action.CreatedAt.Add(7 * 24 * time.Hour)) {
					service.unknown(ctx, &action, "key_expired")
					return
				}
			}

			method, path, body, requestErr := fixedRequest(Intent{Kind: action.Kind, MachineID: action.MachineID, Body: action.RequestBody})
			if requestErr != nil || method != action.Method || path != action.Path || !bytes.Equal(body, action.RequestBody) {
				service.unknown(ctx, &action, "request_changed")
				return
			}
			action.NextAttemptAt = time.Time{}
			action.State = "submitting"
			action.Attempts++
			if !service.save(ctx, &action) {
				return
			}
			remote, err := submitUpstream(ctx, client, action)
			if ctx.Err() != nil {
				return
			}
			service.observe(ctx, action, err)
			if err != nil {
				if credentialUnavailable(err) {
					service.unknown(ctx, &action, "credential_changed")
					return
				}
				if rejected(err) {
					action.State = "completed"
					action.ErrorCode = rejectionCode(err)
					service.save(ctx, &action)
					return
				}
				if action.Attempts >= service.options.MaxAttempts {
					service.unknown(ctx, &action, "retry_exhausted")
					return
				}
				action.NextAttemptAt = service.options.Clock.Now().Add(service.delay(action.Attempts, err))
				action.State = "pending"
				if !service.save(ctx, &action) {
					return
				}
				continue
			}
			if remote.ID == "" {
				service.unknown(ctx, &action, "invalid_response")
				return
			}
			action.UpstreamOperationID = remote.ID
			action.State = "tracking"
			action.UpstreamState = string(remote.State)
			action.Phase = remote.Phase
			if !service.save(ctx, &action) {
				return
			}
			service.cache.Invalidate(action.CredentialID)
		}
		select {
		case service.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		pollStarted := service.options.Clock.Now()
		remote, err := client.GetOperation(ctx, action.UpstreamOperationID, 25*time.Second)
		<-service.slots
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if rejected(err) {
				service.unknown(ctx, &action, "tracking_unavailable")
				return
			}
			trackingFailures++
			if !service.pause(ctx, trackingFailures, err) {
				return
			}
			continue
		}
		trackingFailures = 0
		changed := action.UpstreamState != string(remote.State) || action.Phase != remote.Phase
		action.UpstreamState = string(remote.State)
		action.Phase = remote.Phase
		if remote.State == "succeeded" || remote.State == "failed" {
			action.State = "completed"
			if remote.State == "failed" {
				action.ErrorCode = "upstream_failed"
				if remote.Error != nil {
					action.ErrorCode = safeErrorCode(remote.Error.Code, "upstream_failed")
				}
			}
			if service.save(ctx, &action) {
				service.cache.Invalidate(action.CredentialID)
			}
			return
		}
		if changed && !service.save(ctx, &action) {
			return
		}
		remaining := time.Second - service.options.Clock.Now().Sub(pollStarted)
		if remaining > 0 && service.options.Clock.Wait(ctx, remaining) != nil {
			return
		}
	}
}
