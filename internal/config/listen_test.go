package config

import "testing"

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestListenAddressMustBeLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
		"0.0.0.0:8080":   false,
		"[::]:8080":      false,
		":8080":          false,
		"192.0.2.1:8080": false,
	}
	for address, want := range cases {
		_, err := FromEnv(envOf(map[string]string{
			"INSTADECK_LISTEN":          address,
			"INSTADECK_ORIGIN":          "https://deck.example.com",
			"INSTADECK_MASTER_KEY_FILE": "master.key",
		}))
		if got := err == nil; got != want {
			t.Errorf("listen %q: accepted = %v, want %v (err: %v)", address, got, want, err)
		}
	}
}

func TestOriginHostIsLowercased(t *testing.T) {
	c, err := FromEnv(envOf(map[string]string{
		"INSTADECK_ORIGIN":          "https://Deck.Example.COM",
		"INSTADECK_MASTER_KEY_FILE": "master.key",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://deck.example.com"; c.Origin != want {
		t.Errorf("origin = %q, want %q", c.Origin, want)
	}
}
