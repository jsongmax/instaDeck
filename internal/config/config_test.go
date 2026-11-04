package config

import "testing"

func TestOrigin(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://127.0.0.1.evil.test", "https://user:pass@example.com", "https://example.com/path", "https://example.com?x=1", ""} {
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
