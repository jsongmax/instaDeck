package instabox

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"net/http"
)

type DesktopRequest struct {
	Action string `json:"action"`
	Width  *int   `json:"width,omitempty"`
	Height *int   `json:"height,omitempty"`
}
type Desktop struct {
	Installed  bool `json:"installed"`
	Running    bool `json:"running"`
	Installing bool `json:"installing"`
	Failed     bool `json:"failed"`
	Width      *int `json:"width,omitempty"`
	Height     *int `json:"height,omitempty"`
}

func (c *Client) Desktop(ctx context.Context, id string, input DesktopRequest) (v Desktop, err error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return v, err
	}
	// Not the 15 second client: start returns only once the VNC server answers.
	_, _, err = c.fileRequest(ctx, "POST", id, "/desktop", nil, bytes.NewReader(raw), int64(len(raw)), &v)
	return
}
func (c *Client) DialDesktop(ctx context.Context, id string) (*websocket.Conn, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, &APIError{Kind: "deadline_required"}
	}
	p, err := resourcePath("machines", id)
	if err != nil {
		return nil, err
	}
	conn, response, err := websocket.Dial(ctx, c.baseURL+p+"/vnc", &websocket.DialOptions{HTTPClient: &c.stream, HTTPHeader: http.Header{"X-Api-Key": {c.key}}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			failure := c.decodeResponse(ctx, response, http.StatusSwitchingProtocols, nil)
			if failure == nil {
				failure = &APIError{Kind: "websocket_handshake"}
			}
			return nil, failure
		}
		return nil, &APIError{Kind: "transport"}
	}
	return conn, nil
}
