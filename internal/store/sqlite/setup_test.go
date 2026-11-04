package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestSettingsMigrationFrom001(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,applied_at INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "INSERT INTO schema_migrations VALUES(1,?,0)", fmt.Sprintf("%x", sha256.Sum256(body))); err != nil {
		t.Fatal(err)
	}
	database.Close()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.SetPanelOrigin(ctx, "https://panel.example"); err != nil {
		t.Fatal(err)
	}
	origin, err := store.PanelOrigin(ctx)
	if err != nil || origin != "https://panel.example" {
		t.Fatal("settings migration failed", err)
	}
}
