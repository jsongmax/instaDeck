package main

import (
	"context"
	"errors"
	"instadeck/internal/config"
	"net"
	"net/http"
	"time"
)

func healthcheck(ctx context.Context) error {
	configuration, err := config.Load()
	if err != nil {
		return err
	}
	target, err := healthcheckURL(configuration.ListenAddress)
	if err != nil {
		return errors.New("invalid healthcheck address")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New("healthcheck failed")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("healthcheck failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("healthcheck failed")
	}
	return nil
}

func healthcheckURL(address string) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/healthz", nil
}
