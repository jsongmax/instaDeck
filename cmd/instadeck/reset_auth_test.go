package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"instadeck/internal/identity"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
)

func TestAdminResetAuth(t *testing.T) {
	directory := t.TempDir()
	keyPath, databasePath := filepath.Join(directory, "master.key"), filepath.Join(directory, "panel.db")
	t.Setenv("INSTADECK_MASTER_KEY_FILE", keyPath)
	t.Setenv("INSTADECK_DATABASE", databasePath)
	ctx := context.Background()
	var output bytes.Buffer
	invoke := func(command, password string) error {
		return run(ctx, []string{"admin", command}, strings.NewReader(password+"\n"), &output)
	}
	if err := invoke("generate-key", ""); err != nil {
		t.Fatal(err)
	}
	if invoke("reset-auth", "reset-password-123") == nil || output.Len() != 0 {
		t.Fatal("reset created an administrator")
	}
	if err := invoke("init", "initial-password-123"); err != nil {
		t.Fatal(err)
	}
	oldLines := strings.Split(strings.TrimSpace(output.String()), "\n")
	output.Reset()
	ring, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := identity.New(store, ring, nil, identityOptions(), identity.NewLimiter(time.Minute, 100, 100))
	if err != nil {
		t.Fatal(err)
	}
	oldAdministrator, err := store.AdministratorByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := service.Password(ctx, "admin", "initial-password-123", "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := service.Complete(ctx, challenge, oldLines[1], true)
	if err != nil {
		t.Fatal(err)
	}
	pendingChallenge, err := service.Password(ctx, "admin", "initial-password-123", "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	if err = invoke("reset-auth", "reset-password-123"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 11 || !strings.HasPrefix(lines[0], "otpauth://") || lines[0] == oldLines[0] {
		t.Fatal("reset credentials output invalid")
	}
	if _, err = service.Session(ctx, oldToken); err == nil {
		t.Fatal("old session survived reset")
	}
	if _, err = service.Password(ctx, "admin", "initial-password-123", "203.0.113.5"); err == nil {
		t.Fatal("old password survived reset")
	}
	if _, err = service.Complete(ctx, pendingChallenge, oldLines[2], true); err == nil {
		t.Fatal("old challenge survived reset")
	}
	administrator, err := store.AdministratorByName(ctx, "admin")
	if err != nil || string(administrator.TOTP.Ciphertext) == string(oldAdministrator.TOTP.Ciphertext) || administrator.LastTOTPStep != -1 {
		t.Fatal("authenticator was not reset")
	}
	if _, err = service.Complete(ctx, mustResetChallenge(t, service, "reset-password-123"), oldLines[2], true); err == nil {
		t.Fatal("old recovery code survived reset")
	}
	if _, err = service.Complete(ctx, mustResetChallenge(t, service, "reset-password-123"), lines[1], true); err != nil {
		t.Fatal("new credentials unusable", err)
	}
	events, err := store.ReadAuditEvents(ctx, 0, 100)
	if err != nil || len(events) != 1 || events[0].Event.Type != "security.reset-auth" {
		t.Fatal("reset audit missing", err)
	}
	output.Reset()
	database, err := encryptedDatabase(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err = database.ExecContext(ctx, `CREATE TRIGGER reject_reset_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeRejectedReset, err := store.AdministratorByName(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if invoke("reset-auth", "another-reset-password-123") == nil || output.Len() != 0 {
		t.Fatal("reset proceeded without audit")
	}
	afterRejectedReset, err := store.AdministratorByName(ctx, "admin")
	if err != nil || afterRejectedReset.Revision != beforeRejectedReset.Revision || string(afterRejectedReset.Password.Digest) != string(beforeRejectedReset.Password.Digest) {
		t.Fatal("unaudited reset changed credentials")
	}
	if invoke("reset-auth", "short") == nil || output.Len() != 0 {
		t.Fatal("weak reset password accepted or secrets printed on failure")
	}
}

func mustResetChallenge(t *testing.T, service *identity.Service, password string) string {
	t.Helper()
	challenge, err := service.Password(context.Background(), "admin", password, "203.0.113.5")
	if err != nil {
		t.Fatal(err)
	}
	return challenge
}
