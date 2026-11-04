package main

import (
	"bytes"
	"context"
	"instadeck/internal/account"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdminLifecycleAndInterruptedRotation(t *testing.T) {
	directory, err := os.MkdirTemp(".", "admin-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	keyPath, databasePath := filepath.Join(directory, "master.key"), filepath.Join(directory, "panel.db")
	t.Setenv("INSTADECK_MASTER_KEY_FILE", keyPath)
	t.Setenv("INSTADECK_DATABASE", databasePath)
	ctx := context.Background()
	var output bytes.Buffer
	invoke := func(arguments ...string) error {
		return run(ctx, append([]string{"admin"}, arguments...), strings.NewReader("administrator-password-123\n"), &output)
	}
	if err = invoke("generate-key"); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(keyPath)
	if invoke("generate-key") == nil {
		t.Fatal("key overwritten")
	}
	current, _ := os.ReadFile(keyPath)
	if !bytes.Equal(original, current) {
		t.Fatal("existing key changed")
	}
	if err = invoke("init"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 11 || !strings.HasPrefix(lines[0], "otpauth://") {
		t.Fatal("initial credentials missing")
	}
	output.Reset()
	if invoke("init", "another-admin") == nil || output.Len() != 0 {
		t.Fatal("initialization repeated")
	}
	oldRing, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	administrator, err := store.AdministratorByID(ctx, "administrator")
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, err := oldRing.Decrypt(administrator.TOTP, []byte("instadeck:administrator:totp:v1"))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := oldRing.Encrypt([]byte("private-key-for-rotation"), []byte("instadeck:credential:credential:v1"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateCredential(ctx, account.Record{ID: "credential", Name: "test", Status: "valid", Enabled: true, Envelope: envelope, Version: 1, Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	database, err := encryptedDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.ExecContext(ctx, `CREATE TRIGGER reject_rotation BEFORE UPDATE ON credentials BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	if invoke("rotate-key") == nil {
		t.Fatal("injected failure ignored")
	}
	interruptedRing, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _ := store.CredentialByID(ctx, "credential")
	if _, err = interruptedRing.Decrypt(unchanged.Envelope, []byte("instadeck:credential:credential:v1")); err != nil {
		t.Fatal("interrupted rotation lost data", err)
	}
	if _, err = database.ExecContext(ctx, "DROP TRIGGER reject_rotation"); err != nil {
		t.Fatal(err)
	}
	if err = invoke("rotate-key"); err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := store.CredentialByID(ctx, "credential")
	if err != nil || rotated.Envelope.KeyVersion != 3 {
		t.Fatal("credential not rotated", err)
	}
	plain, err := ring.Decrypt(rotated.Envelope, []byte("instadeck:credential:credential:v1"))
	if err != nil || string(plain) != "private-key-for-rotation" {
		t.Fatal("credential lost", err)
	}
	administrator, err = store.AdministratorByID(ctx, "administrator")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ring.Decrypt(administrator.TOTP, []byte("instadeck:administrator:totp:v1"))
	if err != nil || !bytes.Equal(secret, oldSecret) {
		t.Fatal("TOTP lost", err)
	}
	if err = verifyEncrypted(ctx, databasePath, ring); err != nil {
		t.Fatal(err)
	}
	if verifyEncrypted(ctx, databasePath, oldRing) == nil {
		t.Fatal("old-only master key accepted")
	}
	backup := filepath.Join(directory, "backup.db")
	if err = invoke("backup", backup); err != nil {
		t.Fatal(err)
	}
	if invoke("backup", backup) == nil {
		t.Fatal("backup overwritten")
	}
	restored, err := sqlite.Open(ctx, backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err = restored.AdministratorByName(ctx, "admin"); err != nil {
		t.Fatal("backup unreadable", err)
	}
	if output.Len() != 0 {
		t.Fatal("admin command printed private state")
	}
}

func TestServeListensAndShutsDown(t *testing.T) {
	directory, err := os.MkdirTemp(".", "server-lifecycle-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	t.Setenv("INSTADECK_LISTEN", address)
	t.Setenv("INSTADECK_ORIGIN", "http://"+address)
	t.Setenv("INSTADECK_DATABASE", filepath.Join(directory, "panel.db"))
	t.Setenv("INSTADECK_MASTER_KEY_FILE", filepath.Join(directory, "master.key"))
	if err = run(context.Background(), []string{"admin", "generate-key"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, []string{"serve"}, strings.NewReader(""), &bytes.Buffer{}) }()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		response, err := client.Get("http://" + address + "/api/v1/healthz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-finished:
			t.Fatal("server exited early", err)
		case <-deadline.C:
			t.Fatal("server failed to listen")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err = <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
}

func TestServeFailsForMissingMasterKeyAndUndecryptableRecords(t *testing.T) {
	directory, err := os.MkdirTemp(".", "serve-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	t.Setenv("INSTADECK_ORIGIN", "http://127.0.0.1:8080")
	t.Setenv("INSTADECK_DATABASE", filepath.Join(directory, "panel.db"))
	t.Setenv("INSTADECK_MASTER_KEY_FILE", filepath.Join(directory, "missing.key"))
	if run(context.Background(), []string{"serve"}, strings.NewReader(""), &bytes.Buffer{}) == nil {
		t.Fatal("missing master key accepted")
	}
	if _, err := readPassword(strings.NewReader(strings.Repeat("x", 1025))); err == nil {
		t.Fatal("unbounded CLI password")
	}
	if err = run(context.Background(), []string{"admin", "generate-key"}, strings.NewReader(""), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), os.Getenv("INSTADECK_DATABASE"))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateCredential(context.Background(), account.Record{ID: "invalid", Status: "valid", Envelope: keyring.Envelope{KeyVersion: 99}}); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	if err = run(context.Background(), []string{"serve"}, strings.NewReader(""), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "cannot be decrypted") {
		t.Fatal("invalid ciphertext accepted", err)
	}
}
