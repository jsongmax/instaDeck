package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"instadeck/internal/audit"
	"instadeck/internal/identity"
	"instadeck/internal/keyring"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func seed(t *testing.T, store *Store) time.Time {
	t.Helper()
	err := store.CreateAdministrator(context.Background(), identity.Administrator{ID: "admin", Username: "admin", Password: identity.PasswordHash{Salt: []byte("test salt"), Digest: []byte("test digest")}, TOTP: keyring.Envelope{KeyVersion: 1, Nonce: []byte("test nonce"), Ciphertext: []byte("test encrypted value")}, LastTOTPStep: -1, RecoveryHashes: [][32]byte{identity.TokenHash("test recovery")}})
	if err != nil {
		t.Fatal(err)
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func session(name string, now time.Time) identity.Session {
	return identity.Session{Hash: identity.TokenHash(name), AdministratorID: "admin", CSRFSecret: "test csrf", CreatedAt: now, LastSeen: now, AuthenticatedAt: now}
}

func challenge(t *testing.T, store *Store, name string, now time.Time, attempts int) [32]byte {
	t.Helper()
	hash := identity.TokenHash(name)
	if err := store.CreateChallenge(context.Background(), identity.Challenge{Hash: hash, AdministratorID: "admin", ExpiresAt: now.Add(time.Minute), Attempts: attempts}); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestMigrations(t *testing.T) {
	ctx := context.Background()
	store, path := openTest(t)
	seed(t, store)
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	administrator, err := reopened.AdministratorByName(ctx, "admin")
	if err != nil || len(administrator.RecoveryHashes) != 1 {
		t.Fatal("migration lost data")
	}
	reopened.Close()
	if _, err = store.writer.ExecContext(ctx, "UPDATE schema_migrations SET checksum='changed'"); err != nil {
		t.Fatal(err)
	}
	if invalid, err := Open(ctx, path); err == nil {
		invalid.Close()
		t.Fatal("accepted checksum mismatch")
	}
	if _, err = store.writer.ExecContext(ctx, "UPDATE schema_migrations SET version=999"); err != nil {
		t.Fatal(err)
	}
	if invalid, err := Open(ctx, path); err == nil {
		invalid.Close()
		t.Fatal("accepted newer schema")
	}
}

func TestConnectionPragmas(t *testing.T) {
	store, _ := openTest(t)
	ctx := context.Background()
	for _, pool := range []*sql.DB{store.writer, store.reader} {
		connections := []*sql.Conn{}
		count := 1
		if pool == store.reader {
			count = 4
		}
		for i := 0; i < count; i++ {
			connection, err := pool.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, connection)
			var foreignKeys, timeout int
			var journal string
			if err = connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&timeout)
			connection.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal)
			if foreignKeys != 1 || timeout != 5000 || journal != "wal" {
				t.Fatal("missing connection pragmas")
			}
		}
		for _, connection := range connections {
			connection.Close()
		}
	}
}

func TestConcurrentCompletion(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "totp", true: "recovery"}[recovery], func(t *testing.T) {
			store, _ := openTest(t)
			now := seed(t, store)
			ctx := context.Background()
			var successful atomic.Int32
			var workers sync.WaitGroup
			for i := 0; i < 12; i++ {
				name := time.Unix(int64(i), 0).String()
				hash := challenge(t, store, name, now, 1)
				if _, err := store.AtomicTakeChallenge(ctx, hash, now); err != nil {
					t.Fatal(err)
				}
				workers.Add(1)
				go func() {
					defer workers.Done()
					replacement := session(name, now)
					var err error
					if recovery {
						err = store.AtomicCompleteRecovery(ctx, hash, identity.TokenHash("test recovery"), replacement, now)
					} else {
						err = store.AtomicCompleteTOTP(ctx, hash, 10, replacement, now)
					}
					if err == nil {
						successful.Add(1)
					}
				}()
			}
			workers.Wait()
			if successful.Load() != 1 {
				t.Fatalf("successful completions: %d", successful.Load())
			}
		})
	}
}

func TestChallengeLimits(t *testing.T) {
	store, _ := openTest(t)
	now := seed(t, store)
	ctx := context.Background()
	hash := challenge(t, store, "attempts", now, 2)
	for i := 0; i < 2; i++ {
		if _, err := store.AtomicTakeChallenge(ctx, hash, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AtomicTakeChallenge(ctx, hash, now); err == nil {
		t.Fatal("accepted exhausted challenge")
	}
	expired := challenge(t, store, "expired", now, 1)
	if _, err := store.AtomicTakeChallenge(ctx, expired, now.Add(time.Minute)); err == nil {
		t.Fatal("accepted expired challenge")
	}
	if err := store.AtomicCompleteTOTP(ctx, expired, 1, session("expired session", now), now); err == nil {
		t.Fatal("completed untaken challenge")
	}
	if err := store.AtomicCompleteTOTP(ctx, hash, 1, session("last attempt", now), now); err != nil {
		t.Fatal("last attempt must complete", err)
	}
	if err := store.AtomicCompleteTOTP(ctx, hash, 2, session("replay", now), now); err == nil {
		t.Fatal("replayed challenge")
	}
	taken := challenge(t, store, "taken expired", now, 1)
	store.AtomicTakeChallenge(ctx, taken, now)
	if err := store.AtomicCompleteTOTP(ctx, taken, 2, session("expired completion", now), now.Add(time.Minute)); err == nil {
		t.Fatal("completed expired challenge")
	}
}

func TestSessionLifecycle(t *testing.T) {
	store, _ := openTest(t)
	now := seed(t, store)
	ctx := context.Background()
	for _, test := range []struct {
		name                    string
		elapsed, idle, absolute time.Duration
		revoked                 bool
	}{{name: "idle", elapsed: time.Minute, idle: time.Minute, absolute: time.Hour}, {name: "absolute", elapsed: time.Hour, idle: 2 * time.Hour, absolute: time.Hour}, {name: "revoked", idle: time.Hour, absolute: time.Hour, revoked: true}} {
		record := session(test.name, now)
		if err := store.CreateSession(ctx, record); err != nil {
			t.Fatal(err)
		}
		if test.revoked {
			if err := store.RevokeSession(ctx, record.Hash); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.AtomicUseSession(ctx, record.Hash, now.Add(test.elapsed), test.idle, test.absolute); err == nil {
			t.Fatal("accepted invalid session", test.name)
		}
	}
	old := session("old", now)
	store.CreateSession(ctx, old)
	used, err := store.AtomicUseSession(ctx, old.Hash, now.Add(time.Second), time.Minute, time.Hour)
	if err != nil || !used.LastSeen.Equal(now.Add(time.Second)) {
		t.Fatal("last seen not updated")
	}
	replacement := session("replacement", now.Add(2*time.Second))
	if err := store.AtomicRotateSession(ctx, old.Hash, 2, replacement, now.Add(2*time.Second), time.Minute, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AtomicUseSession(ctx, old.Hash, now.Add(3*time.Second), time.Minute, time.Hour); err == nil {
		t.Fatal("old token valid")
	}
	current, err := store.AtomicUseSession(ctx, replacement.Hash, now.Add(3*time.Second), time.Minute, time.Hour)
	if err != nil || !current.CreatedAt.Equal(now) {
		t.Fatal("rotation changed absolute lifetime")
	}
}

func TestCompletionRollback(t *testing.T) {
	store, _ := openTest(t)
	now := seed(t, store)
	ctx := context.Background()
	existing := session("duplicate", now)
	store.CreateSession(ctx, existing)
	for _, recovery := range []bool{false, true} {
		hash := challenge(t, store, fmt.Sprintf("rollback challenge %t", recovery), now, 1)
		store.AtomicTakeChallenge(ctx, hash, now)
		var err error
		if recovery {
			err = store.AtomicCompleteRecovery(ctx, hash, identity.TokenHash("test recovery"), existing, now)
		} else {
			err = store.AtomicCompleteTOTP(ctx, hash, 5, existing, now)
		}
		if err == nil {
			t.Fatal("duplicate session accepted")
		}
		replacement := session(fmt.Sprintf("rollback replacement %t", recovery), now)
		if recovery {
			err = store.AtomicCompleteRecovery(ctx, hash, identity.TokenHash("test recovery"), replacement, now)
		} else {
			err = store.AtomicCompleteTOTP(ctx, hash, 5, replacement, now)
		}
		if err != nil {
			t.Fatal("completion failed to roll back", err)
		}
	}
	old := session("rotate rollback", now)
	store.CreateSession(ctx, old)
	if err := store.AtomicRotateSession(ctx, old.Hash, 6, existing, now, time.Minute, time.Hour); err == nil {
		t.Fatal("rotation accepted duplicate")
	}
	if _, err := store.AtomicUseSession(ctx, old.Hash, now, time.Minute, time.Hour); err != nil {
		t.Fatal("rotation did not roll back")
	}
	administrator, _ := store.AdministratorByID(ctx, "admin")
	if administrator.LastTOTPStep != 5 {
		t.Fatal("rotation counter did not roll back")
	}
}

func TestRecordsAuditBackup(t *testing.T) {
	store, _ := openTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	credential := Credential{ID: "credential", Name: "example", Envelope: keyring.Envelope{KeyVersion: 1, Nonce: []byte("test nonce"), Ciphertext: []byte("test ciphertext")}, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	credential.Name = "updated"
	if err := store.UpdateCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	credentials, err := store.ListCredentials(ctx)
	if err != nil || len(credentials) != 1 || credentials[0].Name != "updated" {
		t.Fatal("credential roundtrip")
	}
	action := Action{ID: "action", CredentialID: credential.ID, ClientRequestID: "client", UpstreamIdempotencyKey: "upstream", State: "queued", RequestBody: []byte(`{"name":"example"}`), CreatedAt: now, UpdatedAt: now}
	if err := store.CreateAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	duplicate := action
	duplicate.ID = "duplicate"
	duplicate.ClientRequestID = "other"
	if err := store.CreateAction(ctx, duplicate); err == nil {
		t.Fatal("duplicate upstream key")
	}
	duplicate.UpstreamIdempotencyKey = "other"
	duplicate.ClientRequestID = action.ClientRequestID
	if err := store.CreateAction(ctx, duplicate); err == nil {
		t.Fatal("duplicate client request")
	}
	if records, err := store.ListUnfinishedActions(ctx); err != nil || len(records) != 1 {
		t.Fatal("unfinished actions")
	}
	action.State = "succeeded"
	if err := store.UpdateAction(ctx, action); err != nil {
		t.Fatal(err)
	}
	if records, err := store.ListUnfinishedActions(ctx); err != nil || len(records) != 0 {
		t.Fatal("completed action listed")
	}
	if record, err := store.ActionByClientRequestID(ctx, "client"); err != nil || string(record.RequestBody) != string(action.RequestBody) {
		t.Fatal("action roundtrip")
	}
	for i := 0; i < 3; i++ {
		event := audit.Event{Time: now, Type: "test"}
		event.SetDetail("safe\ndetail")
		if err := store.WriteEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	page, err := store.ReadAuditEvents(ctx, 0, 2)
	if err != nil || len(page) != 2 || page[0].Event.Detail() != "safedetail" {
		t.Fatal("audit first page")
	}
	next, err := store.ReadAuditEvents(ctx, page[1].Cursor, 2)
	if err != nil || len(next) != 1 || next[0].Cursor <= page[1].Cursor {
		t.Fatal("audit next page")
	}
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(ctx, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if record, err := backup.ActionByID(ctx, action.ID); err != nil || record.State != "succeeded" {
		t.Fatal("backup missing committed data")
	}
	if err := store.DeleteCredential(ctx, credential.ID); err != nil {
		t.Fatal("credential tombstone failed", err)
	}
}
