package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"instadeck/internal/keyring"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func encryptedDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid database path")
	}
	location := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(absolute), "/")}
	database, err := sql.Open("sqlite", location.String()+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, errors.New("database unavailable")
	}
	database.SetMaxOpenConns(1)
	return database, nil
}

func requireUninitialized(ctx context.Context, path string) error {
	database, err := encryptedDatabase(path)
	if err != nil {
		return err
	}
	defer database.Close()
	var count int
	if database.QueryRowContext(ctx, "SELECT count(*) FROM administrator").Scan(&count) != nil {
		return errors.New("administrator storage unavailable")
	}
	if count != 0 {
		return errors.New("administrator already initialized")
	}
	return nil
}

type encryptedValue struct {
	id, table string
	envelope  keyring.Envelope
}

func encryptedValues(ctx context.Context, connection *sql.Conn) ([]encryptedValue, error) {
	values := []encryptedValue{}
	for _, table := range []string{"administrator", "credentials"} {
		query := "SELECT id,key_version,nonce,ciphertext FROM " + table
		if table == "credentials" {
			query += " WHERE status<>'deleted'"
		}
		rows, err := connection.QueryContext(ctx, query)
		if err != nil {
			return nil, errors.New("encrypted records unavailable")
		}
		for rows.Next() {
			value := encryptedValue{table: table}
			if rows.Scan(&value.id, &value.envelope.KeyVersion, &value.envelope.Nonce, &value.envelope.Ciphertext) != nil {
				rows.Close()
				return nil, errors.New("invalid encrypted record")
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, errors.New("encrypted records unavailable")
		}
	}
	return values, nil
}

func valueAAD(value encryptedValue) []byte {
	if value.table == "administrator" {
		return []byte("instadeck:administrator:totp:v1")
	}
	return []byte("instadeck:credential:" + value.id + ":v1")
}

func verifyEncrypted(ctx context.Context, path string, ring *keyring.Ring) error {
	database, err := encryptedDatabase(path)
	if err != nil {
		return err
	}
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		return errors.New("database unavailable")
	}
	defer connection.Close()
	values, err := encryptedValues(ctx, connection)
	if err != nil {
		return err
	}
	for _, value := range values {
		plaintext, err := ring.Decrypt(value.envelope, valueAAD(value))
		if err != nil {
			return errors.New("stored secrets cannot be decrypted with configured master key")
		}
		clear(plaintext)
	}
	return nil
}

func rotateKey(ctx context.Context, keyPath, databasePath string, oldRing *keyring.Ring) error {
	database, err := encryptedDatabase(databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	connection, err := database.Conn(ctx)
	if err != nil {
		return errors.New("database unavailable")
	}
	defer connection.Close()
	if _, err = connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return errors.New("rotation lock unavailable")
	}
	defer connection.ExecContext(context.Background(), "ROLLBACK")
	values, err := encryptedValues(ctx, connection)
	if err != nil {
		return err
	}
	for _, value := range values {
		plaintext, err := oldRing.Decrypt(value.envelope, valueAAD(value))
		if err != nil {
			return errors.New("rotation decryption failed")
		}
		clear(plaintext)
	}
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return errors.New("key file unavailable")
	}
	defer clear(raw)
	var keys keyring.File
	if json.Unmarshal(raw, &keys) != nil {
		return errors.New("invalid key file")
	}
	version := 0
	for _, key := range keys.Keys {
		version = max(version, key.Version)
	}
	newKey, err := keyring.GenerateKey(version + 1)
	if err != nil {
		return err
	}
	keys.Keys = append(keys.Keys, newKey)
	encoded, err := json.Marshal(keys)
	if err != nil {
		return errors.New("key encoding failed")
	}
	defer clear(encoded)
	staged, err := os.CreateTemp(filepath.Dir(keyPath), ".instadeck-key-*")
	if err != nil {
		return errors.New("key staging failed")
	}
	defer os.Remove(staged.Name())
	if err = staged.Chmod(0600); err == nil {
		_, err = staged.Write(encoded)
	}
	if err == nil {
		err = staged.Sync()
	}
	closeErr := staged.Close()
	if err != nil || closeErr != nil {
		return errors.New("key staging failed")
	}
	if os.Rename(staged.Name(), keyPath) != nil {
		return errors.New("key publication failed")
	}
	if err = syncKeyDirectory(filepath.Dir(keyPath)); err != nil {
		return err
	}
	newRing, err := keyring.Load(keyPath)
	if err != nil {
		return err
	}
	for _, value := range values {
		plaintext, err := oldRing.Decrypt(value.envelope, valueAAD(value))
		if err != nil {
			return errors.New("rotation decryption failed")
		}
		envelope, err := newRing.Encrypt(plaintext, valueAAD(value))
		clear(plaintext)
		if err != nil {
			return errors.New("rotation encryption failed")
		}
		if _, err = connection.ExecContext(ctx, "UPDATE "+value.table+" SET key_version=?,nonce=?,ciphertext=? WHERE id=?", envelope.KeyVersion, envelope.Nonce, envelope.Ciphertext, value.id); err != nil {
			return errors.New("rotation update failed")
		}
	}
	if _, err = connection.ExecContext(ctx, "COMMIT"); err != nil {
		return errors.New("rotation commit failed")
	}
	return nil
}
