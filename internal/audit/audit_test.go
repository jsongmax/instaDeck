package audit

import (
	"strings"
	"testing"
)

func TestDetail(t *testing.T) {
	var e Event
	e.SetDetail("a\n\t\x00b" + strings.Repeat("界", 300))
	if strings.ContainsAny(e.Detail(), "\n\t\x00") || len([]rune(e.Detail())) != 256 {
		t.Fatal("detail sanitation")
	}
}
