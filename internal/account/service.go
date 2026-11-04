package account

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"instadeck/internal/instabox"
	"instadeck/internal/keyring"
	"strings"
	"time"
)

// Service owns credential dependencies without retaining plaintext keys.
type Service struct {
	repository Repository
	encryptor  Encryptor
	factory    Factory
	now        func() time.Time
}

// New constructs a service with an injectable clock and upstream factory.
func New(repository Repository, encryptor Encryptor, factory Factory, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if factory == nil {
		factory = func(key string) (Client, error) { return instabox.NewClient(key, instabox.Options{}) }
	}
	return &Service{repository: repository, encryptor: encryptor, factory: factory, now: now}
}

func safeStorage(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FilterError(err)
	}
	return &Failure{Code: "storage_unavailable", Retryable: true}
}

func (service *Service) validateKey(ctx context.Context, key string) (instabox.Me, error) {
	client, err := service.factory(key)
	if err != nil {
		return instabox.Me{}, FilterError(err)
	}
	identity, err := client.GetMe(ctx)
	if err != nil {
		return instabox.Me{}, FilterError(err)
	}
	if identity.OrgID == "" {
		identity.OrgID = identity.Org.ID
	}
	// Real API-key responses can omit userId although the spec requires it. The
	// organization is what binds a key to its machines, and userId is display only.
	if identity.OrgID == "" {
		return instabox.Me{}, &Failure{Code: "invalid_identity", diagnostic: fmt.Sprintf("hasUser=%t hasOrg=false", identity.UserID > 0)}
	}
	return identity, nil
}

// Add validates a key with GetMe and stores only authenticated ciphertext.
func (service *Service) Add(ctx context.Context, input Input) (DTO, error) {
	if strings.TrimSpace(input.Name) == "" {
		return DTO{}, &Failure{Code: "invalid_name"}
	}
	identity, err := service.validateKey(ctx, input.Key)
	if err != nil {
		return DTO{}, err
	}
	identifier := make([]byte, 16)
	if _, err = rand.Read(identifier); err != nil {
		return DTO{}, &Failure{Code: "random_unavailable"}
	}
	id := hex.EncodeToString(identifier)
	envelope, err := service.encryptor.Encrypt([]byte(input.Key), credentialAAD(id))
	if err != nil {
		return DTO{}, &Failure{Code: "encryption_failed"}
	}
	capabilities, _ := json.Marshal(input.Declarations.capabilities())
	now := service.now().UTC()
	record := Record{ID: id, Name: input.Name, OrganizationID: identity.OrgID, UserID: identity.UserID, Version: 1, Revision: 1, Enabled: true, Status: "valid", Capabilities: string(capabilities), Envelope: envelope, CreatedAt: now, UpdatedAt: now}
	if err = service.repository.CreateCredential(ctx, record); err != nil {
		return DTO{}, safeStorage(err)
	}
	return dto(record), nil
}

func (service *Service) record(ctx context.Context, id string) (Record, error) {
	record, err := service.repository.CredentialByID(ctx, id)
	if err != nil {
		return Record{}, safeStorage(err)
	}
	if record.Status == "deleted" {
		return Record{}, &Failure{Code: "deleted"}
	}
	return record, nil
}

func (service *Service) save(ctx context.Context, record Record) (DTO, error) {
	revision := record.Revision
	record.Revision++
	record.UpdatedAt = service.now().UTC()
	if err := service.repository.CompareAndSwapCredential(ctx, record, revision); err != nil {
		return DTO{}, safeStorage(err)
	}
	return dto(record), nil
}

// List returns metadata for all credentials except deleted tombstones.
func (service *Service) List(ctx context.Context) ([]DTO, error) {
	records, err := service.repository.ListCredentials(ctx)
	if err != nil {
		return nil, safeStorage(err)
	}
	result := make([]DTO, 0, len(records))
	for _, record := range records {
		if record.Status != "deleted" {
			result = append(result, dto(record))
		}
	}
	return result, nil
}

// Replace validates and encrypts a replacement key, advancing its credential version.
func (service *Service) Replace(ctx context.Context, id string, input Input) (DTO, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, err
	}
	identity, err := service.validateKey(ctx, input.Key)
	if err != nil {
		return DTO{}, err
	}
	envelope, err := service.encryptor.Encrypt([]byte(input.Key), credentialAAD(id))
	if err != nil {
		return DTO{}, &Failure{Code: "encryption_failed"}
	}
	capabilities, _ := json.Marshal(input.Declarations.capabilities())
	record.Envelope = envelope
	record.Capabilities = string(capabilities)
	record.UserID = identity.UserID
	record.OrganizationID = identity.OrgID
	record.Status = "valid"
	record.Version++
	if input.Name != "" {
		record.Name = input.Name
	}
	return service.save(ctx, record)
}

// SetEnabled toggles automatic use without restoring invalid credentials.
func (service *Service) SetEnabled(ctx context.Context, id string, enabled bool) (DTO, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, err
	}
	record.Enabled = enabled
	return service.save(ctx, record)
}

// SetDeclarations replaces user assertions without overriding an observed denial.
func (service *Service) SetDeclarations(ctx context.Context, id string, declarations Declarations) (DTO, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, err
	}
	previous := dto(record).Capabilities
	next := declarations.capabilities()
	if previous.Read == Verified || previous.Read == Denied {
		next.Read = previous.Read
	}
	if previous.Write == Denied {
		next.Write = Denied
	}
	if previous.Delete == Denied {
		next.Delete = Denied
	}
	encoded, _ := json.Marshal(next)
	record.Capabilities = string(encoded)
	return service.save(ctx, record)
}

// Delete clears ciphertext and retains the row for historical actions and audit.
func (service *Service) Delete(ctx context.Context, id string) error {
	record, err := service.record(ctx, id)
	if err != nil {
		return err
	}
	record.Status = "deleted"
	record.Enabled = false
	record.Envelope = keyring.Envelope{}
	_, err = service.save(ctx, record)
	return err
}

func (service *Service) decrypt(record Record) ([]byte, error) {
	plaintext, err := service.encryptor.Decrypt(record.Envelope, credentialAAD(record.ID))
	if err != nil {
		return nil, &Failure{Code: "decryption_failed"}
	}
	return plaintext, nil
}

// Validate explicitly rechecks a key, permitting a previously invalid key to recover.
func (service *Service) Validate(ctx context.Context, id string) (DTO, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, err
	}
	plaintext, err := service.decrypt(record)
	if err != nil {
		return DTO{}, err
	}
	defer clear(plaintext)
	identity, err := service.validateKey(ctx, string(plaintext))
	if err != nil {
		_ = service.Observe(ctx, id, record.Version, "", err)
		return DTO{}, err
	}
	record.UserID = identity.UserID
	record.OrganizationID = identity.OrgID
	record.Status = "valid"
	return service.save(ctx, record)
}

// ClientForCredential decrypts only on demand and returns an observed read client.
func (service *Service) ClientForCredential(ctx context.Context, id string) (Client, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return nil, err
	}
	if !record.Enabled {
		return nil, &Failure{Code: "disabled"}
	}
	if record.Status != "valid" {
		return nil, &Failure{Code: "unauthorized"}
	}
	plaintext, err := service.decrypt(record)
	if err != nil {
		return nil, err
	}
	defer clear(plaintext)
	client, err := service.factory(string(plaintext))
	if err != nil {
		return nil, FilterError(err)
	}
	return &observedClient{service: service, id: id, version: record.Version, client: client}, nil
}

// Observe records read success, missing_scope denial, or credential invalidation.
// Results from replaced credential versions cannot modify current credentials.
func (service *Service) Observe(ctx context.Context, id string, version int, capability Capability, result error) error {
	failure := FilterError(result)
	if result != nil && failure.Code != "unauthorized" && failure.Code != "missing_scope" {
		return nil
	}
	if result == nil && capability != Read {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		record, err := service.record(ctx, id)
		if err != nil {
			return err
		}
		if record.Version != version {
			return nil
		}
		capabilities := dto(record).Capabilities
		previous := record.Capabilities
		status := record.Status
		switch {
		case failure != nil && failure.Code == "unauthorized":
			record.Status = "invalid"
		case failure != nil && failure.Code == "missing_scope":
			switch capability {
			case Read:
				capabilities.Read = Denied
			case Write:
				capabilities.Write = Denied
			case Delete:
				capabilities.Delete = Denied
			}
		case result == nil:
			capabilities.Read = Verified
		}
		encoded, _ := json.Marshal(capabilities)
		record.Capabilities = string(encoded)
		if previous == record.Capabilities && status == record.Status {
			return nil
		}
		if _, err = service.save(ctx, record); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return FilterError(ctx.Err())
		}
	}
	return &Failure{Code: "storage_unavailable", Retryable: true}
}

type observedClient struct {
	service *Service
	id      string
	version int
	client  Client
}

func (client observedClient) String() string { return "account.Client[redacted]" }

func (client observedClient) GoString() string { return client.String() }

func (client *observedClient) allowed(ctx context.Context) error {
	record, err := client.service.record(ctx, client.id)
	if err != nil {
		return err
	}
	if record.Version != client.version {
		return &Failure{Code: "replaced"}
	}
	if !record.Enabled {
		return &Failure{Code: "disabled"}
	}
	if record.Status != "valid" {
		return &Failure{Code: "unauthorized"}
	}
	return nil
}

func (client *observedClient) observe(ctx context.Context, capability Capability, err error) error {
	if updateErr := client.service.Observe(ctx, client.id, client.version, capability, err); updateErr != nil {
		return updateErr
	}
	if err != nil {
		return FilterError(err)
	}
	return nil
}

func (client *observedClient) GetMe(ctx context.Context) (instabox.Me, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Me{}, err
	}
	result, err := client.client.GetMe(ctx)
	return result, client.observe(ctx, "", err)
}

func (client *observedClient) ListMachines(ctx context.Context, cursor string) (instabox.MachineList, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.MachineList{}, err
	}
	result, err := client.client.ListMachines(ctx, cursor)
	return result, client.observe(ctx, Read, err)
}

func (client *observedClient) GetUsage(ctx context.Context) (instabox.Usage, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Usage{}, err
	}
	result, err := client.client.GetUsage(ctx)
	return result, client.observe(ctx, Read, err)
}
