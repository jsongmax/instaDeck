package httpapi

import (
	"encoding/json"
	"instadeck/internal/audit"
	"instadeck/internal/instabox"
	"testing"
)

func TestResourceKeyAndPorts(t *testing.T) {
	if resourceKey("a", "b") != resourceKey("a", "b") || resourceKey("a", "b") == resourceKey("b", "a") || resourceKey("a", "bc") == resourceKey("ab", "c") {
		t.Fatal("unstable key")
	}
	for _, p := range []int{0, -1, 5901, 6768, 65536} {
		if validPort(p) {
			t.Fatal(p)
		}
	}
	for _, p := range []int{1024, 65535} {
		if !validPort(p) {
			t.Fatal(p)
		}
	}
}
func TestCSVInjectionAndDetail(t *testing.T) {
	for _, v := range []string{"=1+1", "+cmd", "-cmd", "@sum", "\tformula", "\rformula"} {
		if csvSafe(v) != "'"+v {
			t.Fatal("unsafe CSV")
		}
	}
	if csvSafe("ordinary") != "ordinary" {
		t.Fatal("changed text")
	}
	var e audit.Event
	e.SetDetail("no_capacity")
	raw, _ := json.Marshal(e)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	if decoded["detail"] != "no_capacity" {
		t.Fatal("detail omitted")
	}
}

func TestUpstreamErrorsKeepTheirMeaning(t *testing.T) {
	for _, test := range []struct {
		err      error
		snapshot bool
		want     string
	}{
		{&instabox.APIError{StatusCode: 429, Code: "quota_exceeded"}, true, "quota_exceeded"},
		{&instabox.APIError{StatusCode: 429, Code: "rate_limited"}, true, "rate_limited"},
		{&instabox.APIError{StatusCode: 429}, false, "port_limit_reached"},
		{&instabox.APIError{StatusCode: 403}, true, "snapshot_scope_missing"},
		{&instabox.APIError{StatusCode: 409, Code: "machine_not_running"}, true, "machine_not_running"},
		{&instabox.APIError{StatusCode: 409, Code: "conflict"}, true, "request_conflict"},
		{&instabox.APIError{StatusCode: 503, Code: "private_ports_unavailable"}, false, "private_ports_unavailable"},
	} {
		if got := resourceCode(test.err, test.snapshot); got != test.want {
			t.Errorf("%+v: got %s, want %s", test.err, got, test.want)
		}
	}
	for port, want := range map[int]bool{80: false, 1023: false, 1024: true, 5901: false, 6768: false, 65535: true, 65536: false} {
		if validPort(port) != want {
			t.Errorf("port %d", port)
		}
	}
}
