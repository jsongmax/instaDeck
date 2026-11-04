package account

import (
	"instadeck/internal/instabox"
	"strings"
	"testing"
)

func TestFailureDiagnosticIsSanitized(t *testing.T) {
	failure := FilterError(&instabox.APIError{StatusCode: 403, Kind: "http", Code: "invite_required\nibk_secret value", Field: "plans.maxMachines"})
	if failure.Code != "forbidden" {
		t.Fatal(failure.Code)
	}
	detail := failure.Diagnostic()
	if !strings.Contains(detail, "status=403") || !strings.Contains(detail, "field=plans.maxMachines") {
		t.Fatal(detail)
	}
	if strings.ContainsAny(detail, "\n") || strings.Contains(detail, "secret value") {
		t.Fatal("unsanitized detail")
	}
	if strings.Contains(failure.Error(), "invite") {
		t.Fatal("error exposes upstream text")
	}
}
