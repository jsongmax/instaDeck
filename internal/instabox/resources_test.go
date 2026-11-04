package instabox

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestResourceEndpointsAndMinimalResponseDecoding(t *testing.T) {
	for _, status := range []int{201, 202} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/ports" || strings.HasSuffix(r.URL.Path, "/ports") && r.Method == "GET":
					reply(w, 200, `{"enabled":true,"ports":[{"port":3000,"token":123,"tokenUrl":{}}],"ignored":{"unexpected":true}}`)
				case strings.HasSuffix(r.URL.Path, "/ports") && r.Method == "POST":
					if r.Header.Get("Idempotency-Key") != "" {
						t.Error("port idempotency unsupported")
					}
					reply(w, 201, `{"port":3000,"token":"once","tokenUrl":"https://test/?_token=once","expiresAt":123,"createdAt":{}}`)
				case strings.Contains(r.URL.Path, "/ports/"):
					w.WriteHeader(204)
				case r.Method == "POST":
					if r.Header.Get("Idempotency-Key") != "stable" {
						t.Error("key absent")
					}
					reply(w, status, `{"id":"sn_test","status":"pending","template":123,"defaultSpec":"unexpected","sourceSnapshotSeq":{}}`)
				case r.Method == "DELETE":
					if r.Header.Get("X-Confirm-Delete") != "sn_test" || r.Header.Get("Idempotency-Key") != "stable" {
						t.Error("delete headers")
					}
					w.WriteHeader(204)
				default:
					reply(w, 200, `{"snapshots":[{"id":"sn_test","template":123,"defaultSpec":"ignored"}]}`)
				}
			}, Options{})
			ctx := context.Background()
			if _, err := client.ListPorts(ctx, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := client.ListPorts(ctx, "machine"); err != nil {
				t.Fatal(err)
			}
			v, err := client.PublishPort(ctx, "machine", PublishPortRequest{Port: 3000, Access: "private", Protocol: "http"})
			if err != nil || v.Token != "once" {
				t.Fatal(err)
			}
			if err = client.DeletePort(ctx, "machine", 3000); err != nil {
				t.Fatal(err)
			}
			snapshot, err := client.CreateSnapshot(ctx, "machine", CreateSnapshotRequest{Name: "test"}, "stable")
			if err != nil || snapshot.ID != "sn_test" {
				t.Fatal(err)
			}
			if _, err = client.ListSnapshots(ctx); err != nil {
				t.Fatal(err)
			}
			if err = client.DeleteSnapshot(ctx, "sn_test", "stable"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
