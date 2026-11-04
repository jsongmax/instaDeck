package integration_test

import (
	"context"
	"instadeck/internal/store/sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureTemporaryLifecycle(t *testing.T) {
	var directory string
	var store *sqlite.Store
	t.Run("fixture", func(t *testing.T) {
		fixture := newFixture(t, false)
		store = fixture.store
		var err error
		directory, err = filepath.Abs(fixture.directory)
		if err != nil {
			t.Fatal(err)
		}
		temporaryRoot, err := filepath.Abs(os.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(temporaryRoot, directory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Error("fixture directory is outside configured temporary root", directory)
		}
	})
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Error("fixture directory remains after cleanup", err)
	}
	if _, err := store.ListCredentials(context.Background()); err == nil {
		t.Error("fixture database remains open after cleanup")
	}
}
