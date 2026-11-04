// Package fleet aggregates independent account snapshots in memory.
package fleet

import (
	"context"
	"instadeck/internal/account"
	"instadeck/internal/instabox"
	"time"
)

// Accounts is the credential service interface consumed by aggregation.
type Accounts interface {
	List(context.Context) ([]account.DTO, error)
	ClientForCredential(context.Context, string) (account.Client, error)
}

// Options controls request bounds and independent cache lifetimes.
type Options struct {
	Concurrency    int
	AccountTimeout time.Duration
	OverallTimeout time.Duration
	MachineTTL     time.Duration
	UsageTTL       time.Duration
	MaxPages       int
	Now            func() time.Time
}

// Source identifies the credential and snapshot behind a merged machine.
type Source struct {
	CredentialID string    `json:"credentialId"`
	Stale        bool      `json:"stale"`
	FetchedAt    time.Time `json:"fetchedAt"`
	CanDelete    bool      `json:"canDelete"`
}

// Machine contains fields from one coherent snapshot and all visibility sources.
type Machine struct {
	ID        string                `json:"id"`
	OrgID     string                `json:"orgId"`
	Name      string                `json:"name"`
	State     instabox.MachineState `json:"state"`
	Ready     bool                  `json:"ready"`
	Stale     bool                  `json:"stale"`
	FetchedAt time.Time             `json:"fetchedAt"`
	Sources   []Source              `json:"sources"`
	Conflict  bool                  `json:"conflict"`
}

// Account reports whether a complete machine snapshot is available for a credential.
type Account struct {
	CredentialID   string           `json:"credentialId"`
	Complete       bool             `json:"complete"`
	Stale          bool             `json:"stale"`
	FetchedAt      *time.Time       `json:"fetchedAt"`
	Error          *account.Failure `json:"error"`
	Usage          *instabox.Usage  `json:"usage"`
	UsageFetchedAt *time.Time       `json:"usageFetchedAt"`
	UsageError     *account.Failure `json:"usageError"`
}

// Response preserves account failures even when every upstream fails.
type Response struct {
	Machines []Machine `json:"machines"`
	Accounts []Account `json:"accounts"`
	Partial  bool      `json:"partial"`
}

type machineSnapshot struct {
	id, orgID, name string
	state           instabox.MachineState
	ready           bool
}

type snapshot struct {
	machines       []machineSnapshot
	fetchedAt      time.Time
	machineError   *account.Failure
	machineRetryAt time.Time
	usage          *instabox.Usage
	usageFetchedAt time.Time
	usageError     *account.Failure
	usageRetryAt   time.Time
}

type entry struct {
	version int
	value   snapshot
	done    chan struct{}
}
