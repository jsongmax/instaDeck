package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ListenAddress, DatabasePath, Origin, MasterKeyPath string
	DataDir                                            string
	TrustedProxy                                       string
}

func Load() (Config, error) { return FromEnv(os.Getenv) }
func FromEnv(get func(string) string) (Config, error) {
	c := Config{ListenAddress: get("INSTADECK_LISTEN"), DatabasePath: get("INSTADECK_DATABASE"), Origin: get("INSTADECK_ORIGIN"), MasterKeyPath: get("INSTADECK_MASTER_KEY_FILE")}
	c.TrustedProxy = get("INSTADECK_TRUSTED_PROXY")
	if c.TrustedProxy != "" && c.TrustedProxy != "private" {
		return Config{}, fmt.Errorf("invalid trusted proxy mode")
	}
	c.DataDir = get("INSTADECK_DATA_DIR")
	if c.DataDir == "" {
		c.DataDir = "instadeck-data"
	}
	if c.ListenAddress == "" {
		c.ListenAddress = "127.0.0.1:8080"
	}
	if c.DatabasePath == "" {
		c.DatabasePath = filepath.Join(c.DataDir, "panel.db")
	}
	host, _, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return Config{}, fmt.Errorf("invalid listen address")
	}
	// A network listener requires explicit private reverse proxy trust.
	if listenIP := net.ParseIP(host); c.TrustedProxy == "" && host != "localhost" && (listenIP == nil || !listenIP.IsLoopback()) {
		return Config{}, fmt.Errorf("listen address must be loopback")
	}
	if c.MasterKeyPath == "" {
		c.MasterKeyPath = filepath.Join(c.DataDir, "master.key")
	}
	if c.Origin != "" {
		c.Origin, err = ValidateOrigin(c.Origin)
		if err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// ValidateOrigin validates and normalizes a panel origin.
func ValidateOrigin(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.ForceQuery || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid configured origin")
	}
	ip := net.ParseIP(u.Hostname())
	loop := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return "", fmt.Errorf("origin requires HTTPS")
	}
	// Browsers send the Origin header with a lowercase host.
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}
