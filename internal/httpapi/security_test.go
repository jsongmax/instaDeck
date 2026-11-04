package httpapi

import (
	"bytes"
	"encoding/json"
	"instadeck/internal/operation"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	for _, test := range []struct {
		remote, forwarded string
		trust             bool
		expected          string
	}{
		{remote: "203.0.113.1:1234", forwarded: "1.1.1.1", trust: true, expected: "203.0.113.1"},
		{remote: "127.0.0.1:1234", forwarded: "1.1.1.1", expected: "1.1.1.1"},
		{remote: "127.0.0.1:1234", forwarded: "1.1.1.1, 203.0.113.1", trust: true, expected: "203.0.113.1"},
		{remote: "[::1]:1234", forwarded: "invalid", trust: true, expected: "invalid"},
	} {
		request := httptest.NewRequest("POST", "/", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", test.forwarded)
		if got := ClientIP(request); got != test.expected {
			t.Fatalf("got %s, expected %s", got, test.expected)
		}
	}
}

func TestActionProjectionSuppressesUntrustedFields(t *testing.T) {
	value := publicAction(operation.Action{ID: "action", UpstreamIdempotencyKey: "secret-key", RequestBody: []byte("secret-body"), ErrorCode: "no_capacity", Phase: "allocating resources", UpstreamState: "secret_state"})
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("unsafe projection: %s", encoded)
	}
}

func TestActionFailureReasonAndBoundedPhase(t *testing.T) {
	value := publicAction(operation.Action{ErrorCode: "no_capacity", Phase: "allocating\n\x00" + strings.Repeat("机", 140)})
	if value.ErrorCode != "no_capacity" {
		t.Errorf("error code = %q, want no_capacity", value.ErrorCode)
	}
	if value.Phase != "allocating"+strings.Repeat("机", 118) {
		t.Errorf("phase = %q, want original phase without controls, limited to 128 runes", value.Phase)
	}
}

func TestDecodeRejectsOversizeAndTrailingJSON(t *testing.T) {
	for _, payload := range []string{strings.Repeat("x", 100), `{} {}`, `{"unknown":true}`} {
		request := httptest.NewRequest("POST", "/", strings.NewReader(payload))
		response := httptest.NewRecorder()
		if decode(response, request, &struct{}{}, 20) || response.Code != 400 {
			t.Fatal("invalid body accepted")
		}
	}
}

func TestDeviceSummaryRetainsOnlyCoarseMetadata(t *testing.T) {
	for _, test := range []struct{ userAgent, expected string }{
		{userAgent: "Mozilla/5.0 (Windows NT 10.0) Chrome/120.0 private-device-id", expected: "Chrome · Windows"},
		{userAgent: "Mozilla/5.0 (Macintosh) Chrome/120.0 Edg/120.0", expected: "Edge · macOS"},
		{userAgent: "Mozilla/5.0 (iPhone) Version/17 Safari/605", expected: "Safari · iOS"},
		{userAgent: "Mozilla/5.0 (Linux) Firefox/120", expected: "Firefox · Linux"},
		{userAgent: "private-device-id", expected: "未知设备"},
	} {
		if actual := deviceSummary(test.userAgent); actual != test.expected {
			t.Fatalf("device = %q, want %q", actual, test.expected)
		}
	}
}
