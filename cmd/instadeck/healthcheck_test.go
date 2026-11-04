package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckURL(t *testing.T) {
	for _, tt := range []struct{ address, want string }{
		{"0.0.0.0:8080", "http://127.0.0.1:8080/api/v1/healthz"},
		{"[::]:8080", "http://[::1]:8080/api/v1/healthz"},
		{"127.0.0.1:9000", "http://127.0.0.1:9000/api/v1/healthz"},
	} {
		got, err := healthcheckURL(tt.address)
		if err != nil || got != tt.want {
			t.Fatal(got, err)
		}
	}
}

func TestHealthcheckCommand(t *testing.T) {
	for _, status := range []int{200, 204, 302, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/healthz" {
					t.Error(r.URL.Path)
				}
				if r.Header.Get("Origin") != "" {
					t.Error("unexpected Origin")
				}
				w.Header().Set("Location", "/api/v1/healthz")
				w.WriteHeader(status)
			}))
			defer server.Close()
			t.Setenv("INSTADECK_LISTEN", strings.TrimPrefix(server.URL, "http://"))
			t.Setenv("INSTADECK_DATABASE", "invalid-directory/must-not-open.db")
			t.Setenv("INSTADECK_MASTER_KEY_FILE", "missing-master.key")
			t.Setenv("INSTADECK_ORIGIN", "")
			t.Setenv("INSTADECK_TRUSTED_PROXY", "")
			if err := run(context.Background(), []string{"healthcheck"}, nil, nil); (err == nil) != (status == 200) {
				t.Fatal(status, err)
			}
		})
	}
}
