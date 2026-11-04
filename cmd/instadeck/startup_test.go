package main

import (
	"bytes"
	"context"
	"instadeck/internal/account"
	"instadeck/internal/config"
	"instadeck/internal/identity"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestZeroEnvironmentServe(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	for _, name := range []string{"INSTADECK_DATA_DIR", "INSTADECK_DATABASE", "INSTADECK_MASTER_KEY_FILE", "INSTADECK_LISTEN", "INSTADECK_ORIGIN"} {
		t.Setenv(name, "")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, []string{"serve"}, strings.NewReader(""), &bytes.Buffer{}) }()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ready := false
	for !ready {
		response, err := client.Get("http://127.0.0.1:8080/api/v1/setup/status")
		if err == nil {
			response.Body.Close()
			ready = response.StatusCode == 200
		}
		if ready {
			break
		}
		select {
		case err := <-finished:
			t.Fatal("server exited", err)
		case <-deadline.C:
			t.Fatal("server startup timed out")
		case <-ticker.C:
		}
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keyring.Load(configuration.MasterKeyPath); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(configuration.DataDir); err != nil || !info.IsDir() {
		t.Fatal("data directory missing", err)
	}
}

func TestMissingMasterKeyWithDataRefusesGeneration(t *testing.T) {
	for _, kind := range []string{"administrator", "credential"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			store, err := sqlite.Open(context.Background(), filepath.Join(directory, "panel.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if kind == "administrator" {
				err = store.CreateAdministrator(context.Background(), identity.Administrator{ID: "administrator", Username: "admin", TOTP: keyring.Envelope{KeyVersion: 1}})
			} else {
				err = store.CreateCredential(context.Background(), account.Record{ID: "credential", Status: "valid", Envelope: keyring.Envelope{KeyVersion: 1}})
			}
			if err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(directory, "missing.key")
			if _, err = loadMasterKey(context.Background(), store, keyPath); err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatal("missing key accepted", err)
			}
			if _, err = os.Stat(keyPath); !os.IsNotExist(err) {
				t.Fatal("replacement key created", err)
			}
		})
	}
}

func TestSetOrigin(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("INSTADECK_DATA_DIR", directory)
	for _, name := range []string{"INSTADECK_ORIGIN", "INSTADECK_DATABASE", "INSTADECK_MASTER_KEY_FILE"} {
		t.Setenv(name, "")
	}
	for _, origin := range []string{"http://public.example", "https://user:pass@panel.example", "https://panel.example/path"} {
		if err := admin(context.Background(), []string{"set-origin", origin}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
			t.Fatal("invalid origin accepted", origin)
		}
	}
	var output bytes.Buffer
	if err := admin(context.Background(), []string{"set-origin", "https://panel.example"}, strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "restart") {
		t.Fatal("restart instruction missing")
	}
	store, err := sqlite.Open(context.Background(), filepath.Join(directory, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	origin, err := store.PanelOrigin(context.Background())
	if err != nil || origin != "https://panel.example" {
		t.Fatal("origin not saved", origin, err)
	}
}
