package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/term"
	"instadeck/internal/identity"
	"instadeck/internal/keyring"
	"instadeck/internal/store/sqlite"
	"io"
	"os"
	"strings"
	"time"
)

func admin(ctx context.Context, arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("admin command required")
	}
	keyPath := os.Getenv("INSTADECK_MASTER_KEY_FILE")
	databasePath := os.Getenv("INSTADECK_DATABASE")
	if keyPath == "" {
		return errors.New("master key file required")
	}
	if databasePath == "" {
		databasePath = "instadeck.db"
	}
	if arguments[0] == "generate-key" {
		if len(arguments) != 1 {
			return errors.New("generate-key takes no arguments")
		}
		key, err := keyring.GenerateKey(1)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(keyring.File{Keys: []keyring.Key{key}})
		if err != nil {
			return errors.New("key encoding failed")
		}
		file, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return errors.New("key file exists or cannot be created")
		}
		defer file.Close()
		if _, err = file.Write(encoded); err != nil {
			return errors.New("key write failed")
		}
		if file.Sync() != nil {
			return errors.New("key sync failed")
		}
		return nil
	}
	ring, err := keyring.Load(keyPath)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, databasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	switch arguments[0] {
	case "init":
		if len(arguments) > 2 {
			return errors.New("usage: admin init [username]")
		}
		name := "admin"
		if len(arguments) == 2 {
			name = arguments[1]
		}
		if err = requireUninitialized(ctx, databasePath); err != nil {
			return err
		}
		password, err := readPassword(input)
		if err != nil {
			return err
		}
		secret, err := identity.GenerateTOTPSecret()
		if err != nil {
			return err
		}
		defer clear(secret)
		service, err := identity.New(store, ring, nil, identityOptions(), identity.NewLimiter(time.Minute, 5, 60))
		if err != nil {
			return err
		}
		codes, err := service.Initialize(ctx, "administrator", name, password, secret)
		if err != nil {
			return errors.New("administrator initialization failed")
		}
		fmt.Fprintln(output, identity.OTPAuthURI(secret, "InstaDeck", name))
		for _, code := range codes {
			fmt.Fprintln(output, code)
		}
		return nil
	case "backup":
		if len(arguments) != 2 {
			return errors.New("usage: admin backup destination")
		}
		return store.Backup(ctx, arguments[1])
	case "rotate-key":
		if len(arguments) != 1 {
			return errors.New("rotate-key takes no arguments")
		}
		return rotateKey(ctx, keyPath, databasePath, ring)
	default:
		return errors.New("unknown admin command")
	}
}

func readPassword(input io.Reader) (string, error) {
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		password, err := term.ReadPassword(int(file.Fd()))
		if err != nil {
			return "", errors.New("password read failed")
		}
		defer clear(password)
		if len(password) > 1024 {
			return "", errors.New("password too long")
		}
		return string(password), nil
	}
	reader := bufio.NewReader(io.LimitReader(input, 1026))
	password, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", errors.New("password read failed")
	}
	password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
	if len(password) > 1024 {
		return "", errors.New("password too long")
	}
	return password, nil
}
