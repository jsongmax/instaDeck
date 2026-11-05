package account

import (
	"context"
	"github.com/coder/websocket"
	"instadeck/internal/instabox"
	"io"
)

type ResourceClient interface {
	ListFiles(context.Context, string, string) (instabox.Files, error)
	DownloadFile(context.Context, string, string) (io.ReadCloser, int64, error)
	WriteFile(context.Context, string, string, bool, io.Reader, int64) (instabox.FileWritten, error)
	Exec(context.Context, string, instabox.ExecRequest) (instabox.ExecResult, error)
	ListPorts(context.Context, string) (instabox.PublicPortList, error)
	PublishPort(context.Context, string, instabox.PublishPortRequest) (instabox.PublicPort, error)
	DeletePort(context.Context, string, int) error
	RenameMachine(context.Context, string, string, string) (instabox.Machine, error)
	ListSnapshots(context.Context) (instabox.SnapshotList, error)
	CreateSnapshot(context.Context, string, instabox.CreateSnapshotRequest, string) (instabox.Snapshot, error)
	DeleteSnapshot(context.Context, string, string) error
}

// WithResources obtains a live credential for one bounded request. Snapshot scopes
// are independent of machines:* and must not mark machine capabilities denied.
func (s *Service) WithResources(ctx context.Context, id string, send func(DTO, ResourceClient) error) error {
	reader, err := s.ClientForCredential(ctx, id)
	if err != nil {
		return err
	}
	observed := reader.(*observedClient)
	if err = observed.allowed(ctx); err != nil {
		return err
	}
	client, ok := observed.client.(ResourceClient)
	if !ok {
		return &Failure{Code: "unavailable"}
	}
	record, err := s.record(ctx, id)
	if err != nil {
		return err
	}
	if !record.Enabled || record.Status != "valid" {
		return &Failure{Code: "credential_unavailable"}
	}
	if record.Version != observed.version {
		return &Failure{Code: "replaced"}
	}
	err = send(dto(record), client)
	// Only credential authentication failures apply across scope families.
	if f := FilterError(err); f != nil && f.Code == "unauthorized" {
		_ = s.Observe(ctx, id, record.Version, "", err)
	}
	return err
}

type DesktopClient interface {
	Desktop(context.Context, string, instabox.DesktopRequest) (instabox.Desktop, error)
	DialDesktop(context.Context, string) (*websocket.Conn, error)
}

func (s *Service) DesktopClientForCredential(ctx context.Context, id string, version int) (DTO, DesktopClient, error) {
	metadata, _, err := s.ConnectionClientForCredential(ctx, id, version)
	if err != nil {
		return DTO{}, nil, err
	}
	reader, err := s.ClientForCredential(ctx, id)
	if err != nil {
		return DTO{}, nil, err
	}
	observed := reader.(*observedClient)
	if observed.version != metadata.Version {
		return DTO{}, nil, &Failure{Code: "replaced"}
	}
	if err = observed.allowed(ctx); err != nil {
		return DTO{}, nil, err
	}
	client, ok := observed.client.(DesktopClient)
	if !ok {
		return DTO{}, nil, &Failure{Code: "unavailable"}
	}
	return metadata, client, nil
}
