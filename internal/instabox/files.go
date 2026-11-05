package instabox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type FileEntry struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime string `json:"modTime"`
	Target  string `json:"target,omitempty"`
}
type Files struct {
	Path      string      `json:"path"`
	Type      string      `json:"type"`
	Size      int64       `json:"size"`
	Mode      string      `json:"mode"`
	ModTime   string      `json:"modTime"`
	Entries   []FileEntry `json:"entries,omitempty"`
	Content   string      `json:"content,omitempty"`
	Binary    bool        `json:"binary,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
}
type FileWritten struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Created bool   `json:"created"`
}
type ExecRequest struct {
	Argv           []string          `json:"argv"`
	Envs           map[string]string `json:"envs"`
	TimeoutSeconds int               `json:"timeoutSeconds"`
	NoHistory      bool              `json:"noHistory"`
}
type ExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func (c *Client) fileRequest(ctx context.Context, method, id, suffix string, q url.Values, body io.Reader, length int64, result any) (io.ReadCloser, int64, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, 0, &APIError{Kind: "deadline_required"}
	}
	p, err := resourcePath("machines", id)
	if err != nil {
		return nil, 0, err
	}
	endpoint := c.baseURL + p + suffix
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, 0, &APIError{Kind: "request"}
	}
	// A non-nil body with length 0 would otherwise be sent as unknown length.
	if length == 0 && body != nil {
		r.Body = http.NoBody
	}
	r.ContentLength = length
	r.Header.Set("X-API-Key", c.key)
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	} else if method == "PUT" {
		r.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := c.stream.Do(r)
	if err != nil {
		return nil, 0, &APIError{Kind: "transport", cause: ctx.Err()}
	}
	if result == nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		return response.Body, response.ContentLength, nil
	}
	defer response.Body.Close()
	return nil, 0, c.decodeResponse(ctx, response, anySuccess, result)
}
func (c *Client) ListFiles(ctx context.Context, id, path string) (v Files, err error) {
	_, _, err = c.fileRequest(ctx, "GET", id, "/files", url.Values{"path": {path}}, nil, 0, &v)
	return
}
func (c *Client) DownloadFile(ctx context.Context, id, path string) (io.ReadCloser, int64, error) {
	return c.fileRequest(ctx, "GET", id, "/files/content", url.Values{"path": {path}}, nil, 0, nil)
}
func (c *Client) WriteFile(ctx context.Context, id, path string, parents bool, body io.Reader, length int64) (v FileWritten, err error) {
	_, _, err = c.fileRequest(ctx, "PUT", id, "/files", url.Values{"path": {path}, "parents": {strconv.FormatBool(parents)}}, body, length, &v)
	return
}
func (c *Client) Exec(ctx context.Context, id string, input ExecRequest) (v ExecResult, err error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return
	}
	_, _, err = c.fileRequest(ctx, "POST", id, "/exec", nil, bytes.NewReader(raw), int64(len(raw)), &v)
	return
}
