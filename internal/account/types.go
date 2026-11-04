// Package account manages encrypted credentials and observed capabilities.
package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"time"
)

// CapabilityState records evidence for one scope.
type CapabilityState string

const (
	// Unknown means no declaration or observation is available.
	Unknown CapabilityState = "unknown"
	// Declared means the user asserts the scope is available.
	Declared CapabilityState = "declared"
	// Verified means a read request succeeded.
	Verified CapabilityState = "verified"
	// Denied means the upstream rejected the scope.
	Denied CapabilityState = "denied"
)

// Capability identifies the scope associated with a request.
type Capability string

const (
	// Read identifies machines:read.
	Read Capability = "read"
	// Write identifies machines:write.
	Write Capability = "write"
	// Delete identifies machines:delete.
	Delete Capability = "delete"
)

// Capabilities keeps declarations separate from observed permissions.
type Capabilities struct {
	Read   CapabilityState `json:"read"`
	Write  CapabilityState `json:"write"`
	Delete CapabilityState `json:"delete"`
}

// Declarations contains user assertions; omitted scopes remain unknown.
type Declarations struct {
	Read   bool `json:"read"`
	Write  bool `json:"write"`
	Delete bool `json:"delete"`
}

func (declarations Declarations) capabilities() Capabilities {
	result := Capabilities{Read: Unknown, Write: Unknown, Delete: Unknown}
	if declarations.Read {
		result.Read = Declared
	}
	if declarations.Write {
		result.Write = Declared
	}
	if declarations.Delete {
		result.Delete = Declared
	}
	return result
}

// Record is the encrypted repository representation, never an API response.
type Record struct {
	ID, Name, OrganizationID, Capabilities, Status string
	UserID                                         int64
	Version, Revision                              int
	Enabled                                        bool
	Envelope                                       keyring.Envelope `json:"-"`
	CreatedAt, UpdatedAt                           time.Time
}

// String conceals encrypted material during formatting.
func (record Record) String() string { return "account.Record[redacted]" }

// GoString conceals encrypted material during Go formatting.
func (record Record) GoString() string { return record.String() }

// DTO exposes account metadata without credential material.
type DTO struct {
	CredentialID string       `json:"credentialId"`
	Name         string       `json:"name"`
	UserID       int64        `json:"userId"`
	OrgID        string       `json:"orgId"`
	Version      int          `json:"version"`
	Enabled      bool         `json:"enabled"`
	Status       string       `json:"status"`
	Capabilities Capabilities `json:"capabilities"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// CanDelete requires an enabled, valid credential with an explicit declaration.
func (value DTO) CanDelete() bool {
	return value.Enabled && value.Status == "valid" && (value.Capabilities.Delete == Declared || value.Capabilities.Delete == Verified)
}

func dto(record Record) DTO {
	capabilities := Capabilities{Read: Unknown, Write: Unknown, Delete: Unknown}
	_ = json.Unmarshal([]byte(record.Capabilities), &capabilities)
	return DTO{CredentialID: record.ID, Name: record.Name, UserID: record.UserID, OrgID: record.OrganizationID, Version: record.Version, Enabled: record.Enabled, Status: record.Status, Capabilities: capabilities, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

// Input holds a key only for validation and encryption.
type Input struct {
	Name         string       `json:"name"`
	Key          string       `json:"-"`
	Declarations Declarations `json:"declarations"`
}

// String hides plaintext keys.
func (input Input) String() string { return "account.Input[redacted]" }

// GoString hides plaintext keys in Go formatting.
func (input Input) GoString() string { return input.String() }

// Repository persists encrypted records and atomically rejects outdated updates.
type Repository interface {
	CreateCredential(context.Context, Record) error
	CredentialByID(context.Context, string) (Record, error)
	ListCredentials(context.Context) ([]Record, error)
	CompareAndSwapCredential(context.Context, Record, int) error
}

// Encryptor authenticates credential material using the supplied AAD.
type Encryptor interface {
	Encrypt([]byte, []byte) (keyring.Envelope, error)
	Decrypt(keyring.Envelope, []byte) ([]byte, error)
}

// Client contains only the upstream reads needed by accounts and fleet.
type Client interface {
	GetMe(context.Context) (instabox.Me, error)
	ListMachines(context.Context, string) (instabox.MachineList, error)
	GetUsage(context.Context) (instabox.Usage, error)
}

// Factory creates one credential-bound upstream client.
type Factory func(string) (Client, error)

// Failure is a filtered error safe for panel responses and logs.
type Failure struct {
	Code              string `json:"code"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int64  `json:"retryAfterSeconds,omitempty"`
	cause             error
}

// Error omits upstream text.
func (failure Failure) Error() string { return "account: " + failure.Code }

// Unwrap preserves context identity only.
func (failure Failure) Unwrap() error { return failure.cause }

// FilterError maps untrusted upstream errors to fixed codes.
func FilterError(err error) *Failure {
	if err == nil {
		return nil
	}
	result := &Failure{Code: "unavailable", Retryable: true}
	if errors.Is(err, context.Canceled) {
		return &Failure{Code: "canceled", cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Failure{Code: "timeout", Retryable: true, cause: context.DeadlineExceeded}
	}
	var safe *Failure
	if errors.As(err, &safe) {
		copy := *safe
		return &copy
	}
	var upstream *instabox.APIError
	if errors.As(err, &upstream) {
		result.Retryable = upstream.Retryable
		switch {
		case upstream.StatusCode == 401:
			result.Code = "unauthorized"
			result.Retryable = false
		case upstream.Code == "missing_scope":
			result.Code = "missing_scope"
			result.Retryable = false
		case upstream.StatusCode == 429:
			result.Code = "rate_limited"
			result.Retryable = true
		case upstream.StatusCode == 403:
			result.Code = "forbidden"
			result.Retryable = false
		case upstream.Kind == "pagination_cycle" || upstream.Kind == "page_limit":
			result.Code = "pagination_limit"
			result.Retryable = false
		}
		if upstream.RetryAfterSeconds != nil && *upstream.RetryAfterSeconds > 0 {
			result.RetryAfterSeconds = *upstream.RetryAfterSeconds
		}
	}
	return result
}

func credentialAAD(id string) []byte { return []byte(fmt.Sprintf("instadeck:credential:%s:v1", id)) }
