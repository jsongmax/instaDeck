package main

import (
	"context"
	"errors"
	"fmt"
	"instadeck/internal/account"
	"instadeck/internal/config"
	"instadeck/internal/fleet"
	"instadeck/internal/httpapi"
	"instadeck/internal/identity"
	"instadeck/internal/operation"
	"instadeck/internal/store/sqlite"
	"instadeck/internal/terminal"
	"instadeck/web"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func identityOptions() identity.Options {
	return identity.Options{ChallengeTTL: 5 * time.Minute, IdleTTL: 30 * time.Minute, AbsoluteTTL: 8 * time.Hour, RecentTTL: 5 * time.Minute, ChallengeAttempts: 5}
}

func run(ctx context.Context, arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) == 0 {
		return errors.New("usage: instadeck serve | healthcheck | admin init|generate-key|backup|rotate-key|reset-auth [username] (reset-auth requires access to the master key and database)")
	}
	if len(arguments) == 1 && arguments[0] == "healthcheck" {
		return healthcheck(ctx)
	}
	if arguments[0] == "admin" {
		return admin(ctx, arguments[1:], input, output)
	}
	if len(arguments) != 1 || arguments[0] != "serve" {
		return errors.New("unknown command")
	}
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configuration.DataDir, 0700); err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, configuration.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	ring, err := loadMasterKey(ctx, store, configuration.MasterKeyPath)
	if err != nil {
		return err
	}
	if configuration.Origin == "" {
		configuration.Origin, err = store.PanelOrigin(ctx)
		if err != nil {
			return err
		}
		// admin init creates an administrator without a browser to learn the
		// origin from, and every login would then be rejected.
		initialized, err := store.HasAdministrator(ctx)
		if err != nil {
			return err
		}
		if initialized && configuration.Origin == "" {
			return errors.New("panel origin is not set: run 'instadeck admin set-origin <url>' or set INSTADECK_ORIGIN")
		}
	}
	if err := verifyEncrypted(ctx, configuration.DatabasePath, ring); err != nil {
		return err
	}
	options := identityOptions()
	identities, err := identity.New(store, ring, nil, options, identity.NewLimiter(time.Minute, 5, 60))
	if err != nil {
		return err
	}
	accounts := account.New(store, ring, nil, nil)
	fleets, err := fleet.New(accounts, fleet.Options{})
	if err != nil {
		return err
	}
	actions, err := operation.New(store, accounts, fleets, fleets, operation.Options{})
	if err != nil {
		return err
	}
	terminals, err := terminal.New(httpapi.TerminalUpstream{Accounts: accounts}, terminal.Options{PerSessionLimit: 8, GlobalLimit: 8, Logger: slog.Default()})
	if err != nil {
		return err
	}
	defer terminals.Shutdown()
	api, err := httpapi.New(httpapi.Dependencies{Config: configuration, SetupStore: store, SetupOutput: os.Stderr, Identity: identities, Accounts: accounts, Fleet: fleets, Operations: actions, Terminal: terminals, Audit: store, Assets: web.Assets(), Logger: slog.Default(), AbsoluteSessionTTL: options.AbsoluteTTL})
	if err != nil {
		return err
	}
	if err = actions.Start(context.Background()); err != nil {
		return err
	}
	defer actions.Stop()
	listener, err := net.Listen("tcp", configuration.ListenAddress)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w; set INSTADECK_LISTEN to use another address", configuration.ListenAddress, err)
	}
	server := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384, ErrorLog: slog.NewLogLogger(slog.NewTextHandler(io.Discard, nil), slog.LevelError)}
	finished := make(chan error, 1)
	go func() { finished <- server.Serve(listener) }()
	select {
	case err = <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("HTTP server failed")
		}
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		shutdownDone := make(chan error, 1)
		go func() { shutdownDone <- server.Shutdown(shutdownContext) }()
		terminals.Shutdown()
		if <-shutdownDone != nil {
			_ = server.Close()
		}
		actions.Stop()
	}
	return nil
}
