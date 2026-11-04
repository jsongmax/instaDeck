package account_test

import (
	"context"
	"instadeck/internal/account"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestWriteClientVersionBindingAndDeleteHeader(t *testing.T) {
	var writes atomic.Int32
	service, _, _ := serviceFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/me" {
			identityHandler(writer, request)
			return
		}
		writes.Add(1)
		if request.Header.Get("X-API-Key") != "first-key" || request.Header.Get("X-Confirm-Delete") != "machine" {
			t.Error("wrong bound key or delete confirmation")
		}
		respond(writer, 202, `{"id":"operation","state":"queued"}`)
	})
	credential := add(t, service, "first-key", account.Declarations{Delete: true})
	ctx := context.Background()
	_, client, err := service.WriteClientForCredential(ctx, credential.CredentialID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.DeleteMachine(ctx, "machine", "idempotency", "wrong-input"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Replace(ctx, credential.CredentialID, account.Input{Key: "second-key", Declarations: account.Declarations{Delete: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.DeleteMachine(ctx, "machine", "idempotency", "machine"); err == nil {
		t.Fatal("stale client sent request")
	}
	if _, _, err = service.WriteClientForCredential(ctx, credential.CredentialID, 1); err == nil {
		t.Fatal("replacement silently selected")
	}
	if writes.Load() != 1 {
		t.Fatal("request sent with replacement key")
	}
}

func TestWriteClientObservesMissingScope(t *testing.T) {
	service, _, _ := serviceFixture(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/me" {
			identityHandler(writer, request)
			return
		}
		respond(writer, 403, `{"error":{"code":"missing_scope","message":"private upstream text"}}`)
	})
	credential := add(t, service, "test-key", account.Declarations{Delete: true})
	_, client, err := service.WriteClientForCredential(context.Background(), credential.CredentialID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.DeleteMachine(context.Background(), "machine", "key", "machine"); err == nil {
		t.Fatal("missing rejection")
	}
	credentials, err := service.List(context.Background())
	if err != nil || credentials[0].Capabilities.Delete != account.Denied {
		t.Fatal("denial not recorded")
	}
}
