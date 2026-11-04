package account

import (
	"context"
	"instadeck/internal/instabox"
	"time"
)

// WriteClient is the credential-bound machine mutation interface.
type WriteClient interface {
	GetMachine(context.Context, string) (instabox.Machine, error)
	CreateMachine(context.Context, instabox.CreateMachineRequest, string) (instabox.Operation, error)
	StartMachine(context.Context, string, instabox.StartMachineRequest, string) (instabox.Operation, error)
	StopMachine(context.Context, string, instabox.StopMachineRequest, string) (instabox.Operation, error)
	SuspendMachine(context.Context, string, string) (instabox.Operation, error)
	ResumeMachine(context.Context, string, instabox.ResumeMachineRequest, string) (instabox.Operation, error)
	DeleteMachine(context.Context, string, string, string) (instabox.Operation, error)
	GetOperation(context.Context, string, time.Duration) (instabox.Operation, error)
}

// WriteClientForCredential returns metadata and a client for exactly one version.
// A zero version selects the current version when accepting a new intent.
func (service *Service) WriteClientForCredential(ctx context.Context, id string, version int) (DTO, WriteClient, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, nil, err
	}
	if version != 0 && record.Version != version {
		return DTO{}, nil, &Failure{Code: "replaced"}
	}
	if !record.Enabled || record.Status != "valid" {
		return DTO{}, nil, &Failure{Code: "credential_unavailable"}
	}
	plaintext, err := service.decrypt(record)
	if err != nil {
		return DTO{}, nil, err
	}
	defer clear(plaintext)
	client, err := service.factory(string(plaintext))
	if err != nil {
		return DTO{}, nil, FilterError(err)
	}
	writer, ok := client.(WriteClient)
	if !ok {
		return DTO{}, nil, &Failure{Code: "write_unavailable"}
	}
	return dto(record), &observedWriteClient{service: service, id: id, version: record.Version, client: writer}, nil
}

type observedWriteClient struct {
	service *Service
	id      string
	version int
	client  WriteClient
}

func (client *observedWriteClient) allowed(ctx context.Context) error {
	record, err := client.service.record(ctx, client.id)
	if err != nil {
		return err
	}
	if record.Version != client.version {
		return &Failure{Code: "replaced"}
	}
	if !record.Enabled || record.Status != "valid" {
		return &Failure{Code: "credential_unavailable"}
	}
	return nil
}

func (client *observedWriteClient) mutate(ctx context.Context, capability Capability, send func() (instabox.Operation, error)) (instabox.Operation, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Operation{}, err
	}
	record, err := client.service.record(ctx, client.id)
	if err != nil {
		return instabox.Operation{}, err
	}
	permissions := dto(record).Capabilities
	if (capability == Write && permissions.Write == Denied) || (capability == Delete && !dto(record).CanDelete()) {
		return instabox.Operation{}, &Failure{Code: "capability_denied"}
	}
	result, err := send()
	_ = client.service.Observe(ctx, client.id, client.version, capability, err)
	return result, err
}

func (client *observedWriteClient) GetMachine(ctx context.Context, id string) (instabox.Machine, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Machine{}, err
	}
	result, err := client.client.GetMachine(ctx, id)
	_ = client.service.Observe(ctx, client.id, client.version, Read, err)
	return result, err
}

func (client *observedWriteClient) CreateMachine(ctx context.Context, body instabox.CreateMachineRequest, key string) (instabox.Operation, error) {
	return client.mutate(ctx, Write, func() (instabox.Operation, error) { return client.client.CreateMachine(ctx, body, key) })
}

func (client *observedWriteClient) StartMachine(ctx context.Context, id string, body instabox.StartMachineRequest, key string) (instabox.Operation, error) {
	return client.mutate(ctx, Write, func() (instabox.Operation, error) { return client.client.StartMachine(ctx, id, body, key) })
}

func (client *observedWriteClient) StopMachine(ctx context.Context, id string, body instabox.StopMachineRequest, key string) (instabox.Operation, error) {
	return client.mutate(ctx, Write, func() (instabox.Operation, error) { return client.client.StopMachine(ctx, id, body, key) })
}

func (client *observedWriteClient) SuspendMachine(ctx context.Context, id, key string) (instabox.Operation, error) {
	return client.mutate(ctx, Write, func() (instabox.Operation, error) { return client.client.SuspendMachine(ctx, id, key) })
}

func (client *observedWriteClient) ResumeMachine(ctx context.Context, id string, body instabox.ResumeMachineRequest, key string) (instabox.Operation, error) {
	return client.mutate(ctx, Write, func() (instabox.Operation, error) { return client.client.ResumeMachine(ctx, id, body, key) })
}

func (client *observedWriteClient) DeleteMachine(ctx context.Context, id, key, confirmation string) (instabox.Operation, error) {
	return client.mutate(ctx, Delete, func() (instabox.Operation, error) { return client.client.DeleteMachine(ctx, id, key, id) })
}

func (client *observedWriteClient) GetOperation(ctx context.Context, id string, wait time.Duration) (instabox.Operation, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Operation{}, err
	}
	result, err := client.client.GetOperation(ctx, id, wait)
	if err != nil {
		_ = client.service.Observe(ctx, client.id, client.version, Read, err)
	}
	return result, err
}

func (client observedWriteClient) String() string { return "account.WriteClient[redacted]" }

func (client observedWriteClient) GoString() string { return client.String() }
