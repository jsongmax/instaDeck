// Package sqlite implements persistent panel repositories without handling plaintext keys.
package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	driver "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrRejected means a record is absent, expired, consumed, or conflicts with stored state.
var ErrRejected = errors.New("storage operation rejected")

// ErrConflict identifies a constraint violation.
var ErrConflict = errors.New("storage conflict")

// ErrNotFound identifies an absent record.
var ErrNotFound = errors.New("storage record not found")

func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var failure *driver.Error
	if errors.As(err, &failure) && failure.Code()&255 == 19 {
		return ErrConflict
	}
	if errors.Is(err, ErrRejected) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
		return err
	}
	return errors.New("storage unavailable")
}

// Store owns a serialized writer and a small reader pool.
type Store struct{ writer, reader *sql.DB }

// Open opens a database and verifies all embedded migrations.
func Open(ctx context.Context, path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid database path")
	}
	location := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/")}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	writer, err := sql.Open("sqlite", location.String()+"?"+query.Encode())
	if err != nil {
		return nil, errors.New("database open failed")
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	store := &Store{writer: writer}
	if err = store.migrate(ctx); err != nil {
		writer.Close()
		return nil, err
	}
	query.Add("_pragma", "query_only(1)")
	reader, err := sql.Open("sqlite", location.String()+"?"+query.Encode())
	if err != nil {
		writer.Close()
		return nil, errors.New("database reader open failed")
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	store.reader = reader
	if err = reader.PingContext(ctx); err != nil {
		store.Close()
		return nil, errors.New("database reader unavailable")
	}
	return store, nil
}

// Close releases both database pools.
func (s *Store) Close() error { return errors.Join(s.reader.Close(), s.writer.Close()) }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, checksum TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return errors.New("migration initialization failed")
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return errors.New("migration files unavailable")
	}
	expected := map[int]string{}
	for _, entry := range entries {
		version, parseErr := strconv.Atoi(strings.Split(entry.Name(), "_")[0])
		if parseErr != nil {
			return errors.New("invalid migration number")
		}
		body, readErr := migrations.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return errors.New("migration unavailable")
		}
		expected[version] = fmt.Sprintf("%x", sha256.Sum256(body))
	}
	rows, err := s.writer.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return errors.New("migration history unavailable")
	}
	applied := map[int]bool{}
	for rows.Next() {
		var version int
		var checksum string
		if err = rows.Scan(&version, &checksum); err != nil {
			rows.Close()
			return errors.New("invalid migration history")
		}
		if expected[version] == "" || expected[version] != checksum {
			rows.Close()
			return errors.New("unsupported migration version or checksum mismatch")
		}
		applied[version] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return errors.New("migration history unavailable")
	}
	for _, entry := range entries {
		version, _ := strconv.Atoi(strings.Split(entry.Name(), "_")[0])
		if applied[version] {
			continue
		}
		body, _ := migrations.ReadFile("migrations/" + entry.Name())
		err = s.transaction(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version,checksum,applied_at) VALUES (?, ?, ?)", version, expected[version], time.Now().UTC().UnixMilli())
			return err
		})
		if err != nil {
			return errors.New("migration failed")
		}
	}
	return nil
}

func (s *Store) transaction(ctx context.Context, operation func(*sql.Tx) error) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	if err = operation(tx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return storageError(err)
	}
	if err = tx.Commit(); err != nil {
		return storageError(err)
	}
	return nil
}

func requireOne(result sql.Result, err error) error {
	if err != nil {
		return storageError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return storageError(err)
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// Backup writes a consistent SQLite snapshot to a new destination file.
func (s *Store) Backup(ctx context.Context, destination string) error {
	_, err := s.writer.ExecContext(ctx, "VACUUM INTO ?", destination)
	if err != nil {
		return errors.New("database backup failed")
	}
	return nil
}
