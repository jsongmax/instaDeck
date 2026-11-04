package config

import (
	"path/filepath"
	"testing"
)

func TestOptionalDefaults(t *testing.T) {
	configuration, err := FromEnv(func(string) string { return "" })
	if err != nil || configuration.DataDir != "instadeck-data" || configuration.DatabasePath != filepath.Join("instadeck-data", "panel.db") || configuration.MasterKeyPath != filepath.Join("instadeck-data", "master.key") || configuration.Origin != "" {
		t.Fatal(configuration, err)
	}
	configuration, err = FromEnv(envOf(map[string]string{"INSTADECK_DATA_DIR": "custom", "INSTADECK_DATABASE": "override.db", "INSTADECK_MASTER_KEY_FILE": "override.key"}))
	if err != nil || configuration.DatabasePath != "override.db" || configuration.MasterKeyPath != "override.key" {
		t.Fatal(configuration, err)
	}
}

func TestOrigin(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://127.0.0.1.evil.test", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=1"} {
		_, e := FromEnv(func(k string) string {
			if k == "INSTADECK_ORIGIN" {
				return origin
			}
			if k == "INSTADECK_MASTER_KEY_FILE" {
				return "master.key"
			}
			return ""
		})
		if e == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
	for _, origin := range []string{"https://example.com", "http://127.0.0.1:8080", "http://[::1]:8080", "http://localhost:8080"} {
		c, e := FromEnv(func(k string) string {
			if k == "INSTADECK_ORIGIN" {
				return origin
			}
			if k == "INSTADECK_MASTER_KEY_FILE" {
				return "master.key"
			}
			return ""
		})
		if e != nil || c.ListenAddress != "127.0.0.1:8080" {
			t.Fatal(c, e)
		}
	}
}

func TestTrustedProxy(t *testing.T) {
	for _, tt := range []struct {
		mode, listen string
		valid        bool
	}{
		{"", "0.0.0.0:8080", false}, {"", "[::]:8080", false},
		{"private", "0.0.0.0:8080", true}, {"private", "[::]:8080", true},
		{"public", "127.0.0.1:8080", false}, {"PRIVATE", "127.0.0.1:8080", false},
	} {
		c, err := FromEnv(envOf(map[string]string{"INSTADECK_TRUSTED_PROXY": tt.mode, "INSTADECK_LISTEN": tt.listen}))
		if (err == nil) != tt.valid {
			t.Fatalf("%+v: %v", tt, err)
		}
		if err == nil && c.TrustedProxy != tt.mode {
			t.Fatal(c)
		}
	}
}
