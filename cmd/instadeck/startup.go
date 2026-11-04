package main

import (
	"context"
	"encoding/json"
	"errors"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
	"os"
)

func loadMasterKey(ctx context.Context, store *sqlite.Store, path string) (*keyring.Ring, error) {
	if _, err := os.Lstat(path); err == nil {
		return keyring.Load(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	protected, err := store.HasProtectedData(ctx)
	if err != nil {
		return nil, err
	}
	if protected {
		return nil, errors.New("master key missing: database contains protected data; restore the original master key; refusing to generate a replacement")
	}
	key, err := keyring.GenerateKey(1)
	if err != nil {
		return nil, err
	}
	defer clear(key.Material)
	encoded, err := json.Marshal(keyring.File{Keys: []keyring.Key{key}})
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(encoded)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return nil, err
	}
	return keyring.Load(path)
}
