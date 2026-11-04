package operation

import (
	"encoding/json"
	"testing"
)

func TestFixedCreatePreservesSnapshotID(t *testing.T) {
	method, path, body, err := fixedRequest(Intent{Kind: "create", Body: []byte(`{"name":"new-box","snapshotId":"sn_source","orgId":"org"}`)})
	if err != nil || method != "POST" || path != "/v1/machines" {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(body, &value) != nil || value["snapshotId"] != "sn_source" {
		t.Fatal("snapshot source dropped")
	}
	if _, ok := value["templateId"]; ok {
		t.Fatal("unexpected template")
	}
}
