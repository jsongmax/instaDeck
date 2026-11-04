package instabox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "private-test-credential"

func newTestClient(t *testing.T, handler http.HandlerFunc, options Options) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	options.BaseURL = server.URL
	client, err := NewClient(testKey, options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func reply(writer http.ResponseWriter, status int, body string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	fmt.Fprint(writer, body)
}

func TestEndpointsAndHeaders(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != testKey {
			t.Error("missing authentication")
		}
		if request.Method != "GET" && request.Header.Get("Idempotency-Key") != "intent" {
			t.Error("missing idempotency key")
		}
		switch request.URL.Path {
		case "/v1/me":
			reply(writer, 200, `{"userId":42,"login":"tester"}`)
		case "/v1/usage":
			reply(writer, 200, `{"computedAt":null,"limits":{"maxMachines":0,"unlimited":["maxMachines"]}}`)
		case "/v1/templates":
			reply(writer, 200, `{"templates":[]}`)
		case "/v1/machines/box/connection-info":
			reply(writer, 200, `{"capabilities":["pty","future"]}`)
		case "/v1/machines/box/connections":
			reply(writer, 201, `{"certificate":"sensitive-certificate"}`)
		case "/v1/operations/op":
			if request.URL.Query().Get("wait") != "30s" {
				t.Error("wrong long poll")
			}
			reply(writer, 200, `{"state":"future","phase":"arbitrary future phase"}`)
		case "/v1/machines/box":
			if request.Method == "GET" {
				reply(writer, 200, `{"state":"future","desiredState":"future"}`)
				return
			}
			if request.Header.Get("X-Confirm-Delete") != "confirmed-name" {
				t.Error("missing confirmation")
			}
			reply(writer, 202, `{"id":"op"}`)
		default:
			reply(writer, 202, `{"id":"op"}`)
		}
	}, Options{})
	ctx := context.Background()
	if _, err := client.GetMe(ctx); err != nil {
		t.Fatal(err)
	}
	usage, err := client.GetUsage(ctx)
	if err != nil || usage.ComputedAt != nil || len(usage.Limits.Unlimited) != 1 {
		t.Fatal("usage mismatch", err)
	}
	if _, err = client.ListTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	machine, err := client.GetMachine(ctx, "box")
	if err != nil || machine.State != "future" || machine.DesiredState != "future" {
		t.Fatal("unknown state lost", err)
	}
	operation, err := client.GetOperation(ctx, "op", 30*time.Second)
	if err != nil || operation.State != "future" || operation.Phase != "arbitrary future phase" {
		t.Fatal("operation mismatch", err)
	}
	if _, err = client.GetOperation(ctx, "op", 31*time.Second); err == nil {
		t.Fatal("excessive wait accepted")
	}
	if _, err = client.CreateMachine(ctx, CreateMachineRequest{}, "intent"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.StartMachine(ctx, "box", StartMachineRequest{}, "intent"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.StopMachine(ctx, "box", StopMachineRequest{}, "intent"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.SuspendMachine(ctx, "box", "intent"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.ResumeMachine(ctx, "box", ResumeMachineRequest{}, "intent"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.DeleteMachine(ctx, "box", "intent", "confirmed-name"); err != nil {
		t.Fatal(err)
	}
	if _, err = client.DeleteMachine(ctx, "box", "intent", ""); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	connection, err := client.CreateConnection(ctx, "box", CreateConnectionRequest{PublicKey: "ssh-public"}, "intent")
	if err != nil || connection.Certificate != "sensitive-certificate" {
		t.Fatal("connection mismatch", err)
	}
	for _, value := range []any{client, *client, connection, &connection, Error{Message: testKey}, APIError{Code: testKey, OperationID: &connection.Certificate}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			formatted := fmt.Sprintf(format, value)
			if strings.Contains(formatted, testKey) || strings.Contains(formatted, "sensitive-certificate") {
				t.Fatal("sensitive formatting")
			}
		}
	}
	if _, err = client.GetConnectionInfo(ctx, "box"); err != nil {
		t.Fatal(err)
	}
}

func TestPagination(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			requests := 0
			client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if request.URL.Query().Get("cursor") == "" {
					reply(writer, 200, `{"machines":[{"id":"one"}],"nextCursor":"opaque +/&"}`)
					return
				}
				if request.URL.Query().Get("cursor") != "opaque +/&" {
					t.Error("cursor changed")
				}
				if fail {
					reply(writer, 503, `{"error":{"code":"no_capacity","retryable":true}}`)
					return
				}
				reply(writer, 200, `{"machines":[{"id":"two"}],"nextCursor":null}`)
			}, Options{})
			machines, err := client.ListAllMachines(context.Background())
			if requests != 2 {
				t.Fatal("wrong request count")
			}
			if fail {
				if err == nil || machines != nil {
					t.Fatal("partial results returned")
				}
			} else if err != nil || len(machines) != 2 {
				t.Fatal("pagination failed", err)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				calls++
				reply(writer, status, `{"error":{"code":"missing_scope","message":"`+testKey+`","retryable":true,"retryAfterSeconds":17,"operationId":"op"}}`)
			}, Options{})
			_, err := client.CreateMachine(context.Background(), CreateMachineRequest{}, "intent")
			var failure *APIError
			if !errors.As(err, &failure) || failure.StatusCode != status || failure.Code != "missing_scope" || !failure.Retryable || failure.RetryAfterSeconds == nil || *failure.RetryAfterSeconds != 17 || failure.OperationID == nil || *failure.OperationID != "op" {
				t.Fatal("incorrect typed error", err)
			}
			if calls != 1 || strings.Contains(err.Error(), testKey) {
				t.Fatal("retry or leaked error")
			}
		})
	}
}

func TestResponseFailures(t *testing.T) {
	cases := []struct {
		name                    string
		status                  int
		contentType, body, kind string
		nonJSON                 bool
	}{
		{name: "html", status: 401, contentType: "text/html", body: testKey, kind: "http", nonJSON: true},
		{name: "success html", status: 200, contentType: "text/html", body: testKey, kind: "non_json", nonJSON: true},
		{name: "invalid JSON", status: 200, contentType: "application/json", body: `{"login":`, kind: "invalid_json"},
		{name: "large", status: 200, contentType: "application/json", body: strings.Repeat("x", 129), kind: "response_too_large"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}, Options{MaxResponseBytes: 128})
			_, err := client.GetMe(context.Background())
			var failure *APIError
			if !errors.As(err, &failure) || failure.Kind != test.kind || failure.NonJSON != test.nonJSON || strings.Contains(err.Error(), testKey) {
				t.Fatal("incorrect response failure", err)
			}
		})
	}
}

func TestRedirectRejected(t *testing.T) {
	visits := 0
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		visits++
		if request.Header.Get("X-API-Key") != "" {
			t.Error("credential leaked")
		}
	}))
	defer target.Close()
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, 307)
	}, Options{})
	_, err := client.GetMe(context.Background())
	var failure *APIError
	if !errors.As(err, &failure) || failure.StatusCode != 307 || visits != 0 {
		t.Fatal("redirect followed", err)
	}
}

func TestCancellationAndTimeouts(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(80 * time.Millisecond):
			reply(writer, 200, `{}`)
		}
	}, Options{RequestTimeout: 15 * time.Millisecond})
	if client.poll.Timeout <= 30*time.Second || client.normal.Timeout == client.poll.Timeout {
		t.Fatal("invalid timeout separation")
	}
	_, err := client.GetMe(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("missing deadline", err)
	}
	if _, err = client.GetOperation(context.Background(), "op", time.Second); err != nil {
		t.Fatal("poll used normal timeout", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.GetMe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestRetryHeaderAndPaginationCycle(t *testing.T) {
	client := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "12")
		reply(writer, 429, `{"error":{"code":"rate_limited","retryable":true}}`)
	}, Options{})
	_, err := client.GetMe(context.Background())
	var failure *APIError
	if !errors.As(err, &failure) || failure.RetryAfterSeconds == nil || *failure.RetryAfterSeconds != 12 {
		t.Fatal("missing retry header")
	}
	cycle := newTestClient(t, func(writer http.ResponseWriter, request *http.Request) {
		reply(writer, 200, `{"machines":[],"nextCursor":"same"}`)
	}, Options{})
	machines, err := cycle.ListAllMachines(context.Background())
	if err == nil || machines != nil {
		t.Fatal("pagination cycle accepted")
	}
}
