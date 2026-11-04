package account

import (
	"context"
	"instadeck/internal/instabox"
	"strings"
)

// ConnectionClient supplies credential-bound terminal and template requests.
type ConnectionClient interface {
	GetMachine(context.Context, string) (instabox.Machine, error)
	ListTemplates(context.Context) (instabox.TemplateList, error)
	GetConnectionInfo(context.Context, string) (instabox.ConnectionInfo, error)
	CreateConnection(context.Context, string, instabox.CreateConnectionRequest, string) (instabox.Connection, error)
}

// ConnectionClientForCredential selects an exact live credential version.
func (service *Service) ConnectionClientForCredential(ctx context.Context, id string, version int) (DTO, ConnectionClient, error) {
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, nil, err
	}
	if version != 0 && record.Version != version {
		return DTO{}, nil, &Failure{Code: "replaced"}
	}
	reader, err := service.ClientForCredential(ctx, id)
	if err != nil {
		return DTO{}, nil, err
	}
	observed := reader.(*observedClient)
	if observed.version != record.Version {
		return DTO{}, nil, &Failure{Code: "replaced"}
	}
	connection, ok := observed.client.(ConnectionClient)
	if !ok {
		return DTO{}, nil, &Failure{Code: "connection_unavailable"}
	}
	return dto(record), &observedConnectionClient{observedClient: observed, connection: connection}, nil
}

// Rename updates the display name without replacing credential material.
func (service *Service) Rename(ctx context.Context, id, name string) (DTO, error) {
	if strings.TrimSpace(name) == "" {
		return DTO{}, &Failure{Code: "invalid_name"}
	}
	record, err := service.record(ctx, id)
	if err != nil {
		return DTO{}, err
	}
	record.Name = name
	return service.save(ctx, record)
}

type observedConnectionClient struct {
	*observedClient
	connection ConnectionClient
}

func (client *observedConnectionClient) GetMachine(ctx context.Context, id string) (instabox.Machine, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Machine{}, err
	}
	value, err := client.connection.GetMachine(ctx, id)
	return value, client.observe(ctx, Read, err)
}

func (client *observedConnectionClient) ListTemplates(ctx context.Context) (instabox.TemplateList, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.TemplateList{}, err
	}
	value, err := client.connection.ListTemplates(ctx)
	return value, client.observe(ctx, Read, err)
}

func (client *observedConnectionClient) GetConnectionInfo(ctx context.Context, id string) (instabox.ConnectionInfo, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.ConnectionInfo{}, err
	}
	value, err := client.connection.GetConnectionInfo(ctx, id)
	return value, client.observe(ctx, Read, err)
}

func (client *observedConnectionClient) CreateConnection(ctx context.Context, id string, body instabox.CreateConnectionRequest, key string) (instabox.Connection, error) {
	if err := client.allowed(ctx); err != nil {
		return instabox.Connection{}, err
	}
	value, err := client.connection.CreateConnection(ctx, id, body, key)
	if observeErr := client.service.Observe(ctx, client.id, client.version, Write, err); observeErr != nil {
		return instabox.Connection{}, observeErr
	}
	return value, err
}
