package instabox

import (
	"context"
	"strconv"
)

// Only fields consumed by the panel are decoded; unrelated upstream metadata is ignored.
type PublicPort struct {
	Port     int    `json:"port"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Access   string `json:"access"`
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"`
	TokenURL string `json:"tokenUrl,omitempty"`
}
type PublicPortList struct {
	Enabled bool         `json:"enabled"`
	Ports   []PublicPort `json:"ports"`
}
type PublishPortRequest struct {
	Port        int    `json:"port"`
	Name        string `json:"name,omitempty"`
	Access      string `json:"access"`
	Protocol    string `json:"protocol"`
	RotateToken bool   `json:"rotateToken,omitempty"`
}
type Snapshot struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	OrgID           string `json:"orgId"`
	SourceMachineID string `json:"sourceMachineId"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	RootSizeBytes   int64  `json:"rootSizeBytes"`
	DataSizeBytes   int64  `json:"dataSizeBytes"`
	CreatedAt       string `json:"createdAt"`
}
type SnapshotList struct {
	Snapshots []Snapshot `json:"snapshots"`
}
type CreateSnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	RootOnly    bool   `json:"rootOnly"`
}

func (c *Client) ListPorts(ctx context.Context, id string) (v PublicPortList, err error) {
	p := "/v1/ports"
	if id != "" {
		p, err = resourcePath("machines", id)
		p += "/ports"
	}
	if err == nil {
		// GET never consumes private token fields, even if an upstream returns them.
		var listed struct {
			Enabled bool `json:"enabled"`
			Ports   []struct {
				Port     int    `json:"port"`
				Name     string `json:"name"`
				Protocol string `json:"protocol"`
				Access   string `json:"access"`
				URL      string `json:"url"`
			} `json:"ports"`
		}
		err = c.request(ctx, "GET", p, nil, nil, "", "", 200, false, &listed)
		v.Enabled = listed.Enabled
		v.Ports = make([]PublicPort, 0, len(listed.Ports))
		for _, port := range listed.Ports {
			v.Ports = append(v.Ports, PublicPort{Port: port.Port, Name: port.Name, Protocol: port.Protocol, Access: port.Access, URL: port.URL})
		}
	}

	return
}
func (c *Client) PublishPort(ctx context.Context, id string, b PublishPortRequest) (v PublicPort, err error) {
	p, e := resourcePath("machines", id)
	if e != nil {
		return v, e
	}
	err = c.sendRequest(ctx, "POST", p+"/ports", nil, b, "", "", anySuccess, false, &v)
	return
}
func (c *Client) DeletePort(ctx context.Context, id string, port int) error {
	p, e := resourcePath("machines", id)
	if e != nil {
		return e
	}
	return c.sendRequest(ctx, "DELETE", p+"/ports/"+strconv.Itoa(port), nil, nil, "", "", anySuccess, false, nil)
}
func (c *Client) ListSnapshots(ctx context.Context) (v SnapshotList, err error) {
	err = c.request(ctx, "GET", "/v1/snapshots", nil, nil, "", "", 200, false, &v)
	return
}
func (c *Client) CreateSnapshot(ctx context.Context, id string, b CreateSnapshotRequest, key string) (v Snapshot, err error) {
	p, e := resourcePath("machines", id)
	if e != nil {
		return v, e
	}
	err = c.request(ctx, "POST", p+"/snapshots", nil, b, key, "", anySuccess, false, &v)
	return
}
func (c *Client) DeleteSnapshot(ctx context.Context, id, key string) error {
	p, e := resourcePath("snapshots", id)
	if e != nil {
		return e
	}
	return c.request(ctx, "DELETE", p, nil, nil, key, id, 204, false, nil)
}
