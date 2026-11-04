// Package operation coordinates durable machine intentions and upstream tracking.
package operation

import (
	"context"
	"instadeck/internal/account"
	"instadeck/internal/audit"
	"instadeck/internal/instabox"
	"time"
)

// Action keeps the immutable request separate from local and remote progress.
type Action struct {
	ID, CredentialID, MachineID, Kind                                                     string
	RequestBody                                                                           []byte
	UpstreamIdempotencyKey, ClientRequestID, UpstreamOperationID, State, Phase, ErrorCode string
	Method, Path, Digest, OrgID, UpstreamState                                            string
	CredentialVersion, Attempts                                                           int
	NextAttemptAt                                                                         time.Time
	CreatedAt, UpdatedAt                                                                  time.Time
}

// Intent is one user request; confirmation is required for deletion.
type Intent struct {
	RequestID                                                   string `json:"-"`
	AdministratorID                                             string `json:"-"`
	SourceAddress                                               string `json:"-"`
	ClientRequestID, CredentialID, Kind, MachineID, ConfirmName string
	Body                                                        []byte
}

// Repository persists actions, including an atomic audit intent.
type Repository interface {
	FindAction(context.Context, string) (Action, bool, error)
	ActionByID(context.Context, string) (Action, error)
	CreateActionIntent(context.Context, Action, audit.Event) error
	UpdateAction(context.Context, Action) error
	ListUnfinishedActions(context.Context) ([]Action, error)
}

// Accounts binds every request to one credential version.
type Accounts interface {
	WriteClientForCredential(context.Context, string, int) (account.DTO, account.WriteClient, error)
	Observe(context.Context, string, int, account.Capability, error) error
}

// Visibility checks a machine against the selected credential.
type Visibility interface {
	Visible(context.Context, string, string, string) (bool, error)
}

// Cache invalidates account fleet snapshots.
type Cache interface{ Invalidate(string) }

// Clock supports cancellation-aware backoff.
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

// Audit creates the event persisted atomically with an action.
type Audit interface {
	Intent(Action) (audit.Event, error)
}

// Options bounds retries and independent long polling.
type Options struct {
	PollConcurrency, MaxAttempts int
	BaseDelay                    time.Duration
	Clock                        Clock
	Audit                        Audit
	// Events receives the outcome of each action, which the intent cannot record.
	Events audit.Writer
}

// Failure contains only a fixed public code.
type Failure struct{ Code string }

// Error never includes upstream response text.
func (failure *Failure) Error() string { return "operation: " + failure.Code }

// Upstream documents the polling subset of a write client.
type Upstream interface {
	GetOperation(context.Context, string, time.Duration) (instabox.Operation, error)
}
