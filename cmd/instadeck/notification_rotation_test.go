package main

import (
	"context"
	"encoding/json"
	"instadeck/internal/keyring"
	"instadeck/internal/notify"
	"instadeck/internal/store/sqlite"
	"os"
	"path/filepath"
	"testing"
)

func TestNotificationRotationAndStartupVerification(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "master.key")
	database := filepath.Join(directory, "panel.db")
	key, _ := keyring.GenerateKey(1)
	raw, _ := json.Marshal(keyring.File{Keys: []keyring.Key{key}})
	if err := os.WriteFile(keyPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ring, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := notify.New(store, ring, nil)
	channel, err := service.Save(ctx, notify.Channel{Type: "telegram", Name: "test", Enabled: true, Events: notify.Events}, notify.Config{BotToken: "123456:private", ChatID: "123"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyEncrypted(ctx, database, ring); err != nil {
		t.Fatal(err)
	}
	protected, err := store.HasProtectedData(ctx)
	if err != nil || !protected {
		t.Fatal("notification data unprotected")
	}
	if err = rotateKey(ctx, keyPath, database, ring); err != nil {
		t.Fatal(err)
	}
	newRing, err := keyring.Load(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyEncrypted(ctx, database, newRing); err != nil {
		t.Fatal(err)
	}
	if err = verifyEncrypted(ctx, database, ring); err == nil {
		t.Fatal("old key decrypted rotated notification")
	}
	channels, err := store.ListChannels(ctx)
	if err != nil || len(channels) != 1 || channels[0].Envelope.KeyVersion != 2 {
		t.Fatal("notification not rotated", err)
	}
	plain, err := newRing.Decrypt(channels[0].Envelope, notify.AAD(channel.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	var config notify.Config
	if json.Unmarshal(plain, &config) != nil || config.BotToken != "123456:private" {
		t.Fatal("rotation changed config")
	}
}
