// Package instabox provides a single-key InstaBox API client.
package instabox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API origin.
const DefaultBaseURL = "https://api.instabox.sh"

const anySuccess = 0

// Options overrides defaults for local testing. Production callers should pass Options{}.
type Options struct {
	BaseURL          string
	RequestTimeout   time.Duration
	PollTimeout      time.Duration
	MaxResponseBytes int64
}

// Client holds one API key and never follows redirects or retries requests.
type Client struct {
	key              string
	baseURL          string
	stream           http.Client
	normal           http.Client
	poll             http.Client
	maxResponseBytes int64
}

// String returns a credential-free description.
func (client Client) String() string { return "instabox.Client[redacted]" }

// GoString returns a credential-free Go description.
func (client Client) GoString() string { return client.String() }

// APIError describes an HTTP or protocol failure without exposing response text.
type APIError struct {
	StatusCode        int
	Code              string
	Retryable         bool
	RetryAfterSeconds *int64
	OperationID       *string
	NonJSON           bool
	Kind              string
	// Field names the schema field that failed to decode, never its value.
	Field string
	cause error
}

// Error returns a safe description without server-controlled strings.
func (failure APIError) Error() string {
	return fmt.Sprintf("instabox: request failed (HTTP %d)", failure.StatusCode)
}

// String returns a safe error description.
func (failure APIError) String() string { return failure.Error() }

// GoString returns a safe Go error description.
func (failure APIError) GoString() string { return failure.Error() }

// Unwrap preserves context cancellation and deadline identity only.
func (failure APIError) Unwrap() error { return failure.cause }

// NewClient constructs a client for one key, with fixed production defaults.
func NewClient(key string, options Options) (*Client, error) {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("instabox: invalid API key")
	}
	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("instabox: invalid base URL")
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = 15 * time.Second
	}
	if options.PollTimeout == 0 {
		options.PollTimeout = 40 * time.Second
	}
	if options.MaxResponseBytes == 0 {
		options.MaxResponseBytes = 4 << 20
	}
	if options.RequestTimeout < 0 || options.PollTimeout <= 30*time.Second || options.MaxResponseBytes < 1 || options.MaxResponseBytes > 64<<20 {
		return nil, errors.New("instabox: invalid client limits")
	}
	rejectRedirect := func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{key: key, baseURL: strings.TrimRight(baseURL, "/"), stream: http.Client{CheckRedirect: rejectRedirect}, normal: http.Client{Timeout: options.RequestTimeout, CheckRedirect: rejectRedirect}, poll: http.Client{Timeout: options.PollTimeout, CheckRedirect: rejectRedirect}, maxResponseBytes: options.MaxResponseBytes}, nil
}

func (client *Client) request(ctx context.Context, method, path string, query url.Values, body any, key, confirmation string, status int, poll bool, result any) error {
	if method != http.MethodGet && strings.TrimSpace(key) == "" {
		return errors.New("instabox: idempotency key required")
	}
	if strings.ContainsAny(key+confirmation, "\r\n") {
		return errors.New("instabox: invalid request headers")
	}
	return client.sendRequest(ctx, method, path, query, body, key, confirmation, status, poll, result)
}

// sendRequest also serves endpoints whose schema does not accept idempotency keys.
func (client *Client) sendRequest(ctx context.Context, method, path string, query url.Values, body any, key, confirmation string, status int, poll bool, result any) error {
	if strings.ContainsAny(key+confirmation, "\r\n") {
		return errors.New("instabox: invalid request headers")
	}
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return &APIError{Kind: "request_encoding"}
		}
	}
	endpoint := client.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return &APIError{Kind: "request"}
	}
	request.Header.Set("X-API-Key", client.key)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if confirmation != "" {
		request.Header.Set("X-Confirm-Delete", confirmation)
	}
	transport := &client.normal
	if poll {
		transport = &client.poll
	}
	response, err := transport.Do(request)
	if err != nil {
		var cause error
		if errors.Is(err, context.Canceled) {
			cause = context.Canceled
		} else if errors.Is(err, context.DeadlineExceeded) {
			cause = context.DeadlineExceeded
		}
		return &APIError{Kind: "transport", cause: cause}
	}
	defer response.Body.Close()
	return client.decodeResponse(ctx, response, status, result)
}

func (client *Client) decodeResponse(ctx context.Context, response *http.Response, status int, result any) error {
	raw, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		return &APIError{StatusCode: response.StatusCode, Kind: "read", cause: ctx.Err()}
	}
	if int64(len(raw)) > client.maxResponseBytes {
		return &APIError{StatusCode: response.StatusCode, Kind: "response_too_large"}
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	isJSON := mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
	if response.StatusCode != status && !(status == anySuccess && response.StatusCode >= 200 && response.StatusCode < 300) {
		failure := &APIError{StatusCode: response.StatusCode, Kind: "http", NonJSON: !isJSON}
		if isJSON {
			var envelope ErrorBody
			if json.Unmarshal(raw, &envelope) == nil {
				failure.Code = envelope.Error.Code
				failure.Retryable = envelope.Error.Retryable
				failure.RetryAfterSeconds = envelope.Error.RetryAfterSeconds
				failure.OperationID = envelope.Error.OperationID
			} else {
				failure.Kind = "invalid_json"
			}
		}
		if failure.RetryAfterSeconds == nil {
			retry := response.Header.Get("Retry-After")
			if seconds, parseErr := strconv.ParseInt(retry, 10, 64); parseErr == nil && seconds >= 0 {
				failure.RetryAfterSeconds = &seconds
			} else if deadline, parseErr := http.ParseTime(retry); parseErr == nil {
				seconds := int64(time.Until(deadline).Seconds())
				if seconds < 0 {
					seconds = 0
				}
				failure.RetryAfterSeconds = &seconds
			}
		}
		return failure
	}
	if result == nil {
		return nil
	}
	if !isJSON {
		return &APIError{StatusCode: response.StatusCode, NonJSON: true, Kind: "non_json"}
	}
	if err = json.Unmarshal(raw, result); err != nil {
		failure := &APIError{StatusCode: response.StatusCode, Kind: "invalid_json"}
		var mismatch *json.UnmarshalTypeError
		if errors.As(err, &mismatch) {
			failure.Field = mismatch.Field
		}
		return failure
	}
	return nil
}

func resourcePath(collection, id string) (string, error) {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#\r\n") {
		return "", errors.New("instabox: invalid resource ID")
	}
	return "/v1/" + collection + "/" + url.PathEscape(id), nil
}

// GetMe returns the identity associated with this key.
func (client *Client) GetMe(ctx context.Context) (Me, error) {
	var result Me
	err := client.request(ctx, "GET", "/v1/me", nil, nil, "", "", 200, false, &result)
	return result, err
}

// GetUsage returns usage and limits for this key's default organization.
func (client *Client) GetUsage(ctx context.Context) (Usage, error) {
	var result Usage
	err := client.request(ctx, "GET", "/v1/usage", nil, nil, "", "", 200, false, &result)
	return result, err
}

// ListTemplates returns the available template versions.
func (client *Client) ListTemplates(ctx context.Context) (TemplateList, error) {
	var result TemplateList
	err := client.request(ctx, "GET", "/v1/templates", nil, nil, "", "", 200, false, &result)
	return result, err
}

// ListMachines returns one page; an empty cursor starts the listing.
func (client *Client) ListMachines(ctx context.Context, cursor string) (MachineList, error) {
	var result MachineList
	query := url.Values{}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	err := client.request(ctx, "GET", "/v1/machines", query, nil, "", "", 200, false, &result)
	return result, err
}

// ListAllMachines returns every page or an error with no partial result.
func (client *Client) ListAllMachines(ctx context.Context) ([]Machine, error) {
	machines := []Machine{}
	cursor := ""
	seen := map[string]bool{}
	for {
		page, err := client.ListMachines(ctx, cursor)
		if err != nil {
			return nil, err
		}
		machines = append(machines, page.Machines...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return machines, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, &APIError{Kind: "pagination_cycle"}
		}
		seen[cursor] = true
	}
}

// GetMachine returns a machine by ID or exact name.
func (client *Client) GetMachine(ctx context.Context, id string) (Machine, error) {
	var result Machine
	path, err := resourcePath("machines", id)
	if err == nil {
		err = client.request(ctx, "GET", path, nil, nil, "", "", 200, false, &result)
	}
	return result, err
}

// CreateMachine submits a create with the caller's persisted idempotency key.
func (client *Client) CreateMachine(ctx context.Context, body CreateMachineRequest, key string) (Operation, error) {
	var result Operation
	err := client.request(ctx, "POST", "/v1/machines", nil, body, key, "", 202, false, &result)
	return result, err
}

// DeleteMachine requires explicit caller confirmation and an idempotency key.
func (client *Client) DeleteMachine(ctx context.Context, id, key, confirmation string) (Operation, error) {
	if strings.TrimSpace(confirmation) == "" {
		return Operation{}, errors.New("instabox: delete confirmation required")
	}
	return client.machineAction(ctx, id, "DELETE", "", nil, key, confirmation)
}

func (client *Client) machineAction(ctx context.Context, id, method, action string, body any, key, confirmation string) (Operation, error) {
	var result Operation
	path, err := resourcePath("machines", id)
	if err == nil {
		if action != "" {
			path += "/" + action
		}
		err = client.request(ctx, method, path, nil, body, key, confirmation, 202, false, &result)
	}
	return result, err
}

// StartMachine starts a machine using caller-provided options and key.
func (client *Client) StartMachine(ctx context.Context, id string, body StartMachineRequest, key string) (Operation, error) {
	return client.machineAction(ctx, id, "POST", "start", body, key, "")
}

// StopMachine stops a machine using caller-provided options and key.
func (client *Client) StopMachine(ctx context.Context, id string, body StopMachineRequest, key string) (Operation, error) {
	return client.machineAction(ctx, id, "POST", "stop", body, key, "")
}

// SuspendMachine pauses a machine using the caller's key.
func (client *Client) SuspendMachine(ctx context.Context, id, key string) (Operation, error) {
	return client.machineAction(ctx, id, "POST", "suspend", SuspendMachineRequest{}, key, "")
}

// ResumeMachine resumes a machine using caller-provided options and key.
func (client *Client) ResumeMachine(ctx context.Context, id string, body ResumeMachineRequest, key string) (Operation, error) {
	return client.machineAction(ctx, id, "POST", "resume", body, key, "")
}

// GetOperation optionally long-polls for up to 30 seconds.
func (client *Client) GetOperation(ctx context.Context, id string, wait time.Duration) (Operation, error) {
	var result Operation
	if wait < 0 || wait > 30*time.Second {
		return result, errors.New("instabox: wait must be between zero and 30 seconds")
	}
	path, err := resourcePath("operations", id)
	query := url.Values{}
	if wait > 0 {
		query.Set("wait", wait.String())
	}
	if err == nil {
		err = client.request(ctx, "GET", path, query, nil, "", "", 200, wait > 0, &result)
	}
	return result, err
}

// CreateConnection issues an SSH certificate using the caller's key.
func (client *Client) CreateConnection(ctx context.Context, id string, body CreateConnectionRequest, key string) (Connection, error) {
	var result Connection
	if body.TTLSeconds != nil && (*body.TTLSeconds < 1 || *body.TTLSeconds > 86400) {
		return result, errors.New("instabox: invalid connection TTL")
	}
	path, err := resourcePath("machines", id)
	if err == nil {
		err = client.request(ctx, "POST", path+"/connections", nil, body, key, "", 201, false, &result)
	}
	return result, err
}

// GetConnectionInfo returns SSH connection details and capabilities.
func (client *Client) GetConnectionInfo(ctx context.Context, id string) (ConnectionInfo, error) {
	var result ConnectionInfo
	path, err := resourcePath("machines", id)
	if err == nil {
		err = client.request(ctx, "GET", path+"/connection-info", nil, nil, "", "", 200, false, &result)
	}
	return result, err
}
