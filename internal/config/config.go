package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ListenAddress, DatabasePath, Origin, MasterKeyPath string
	TrustLocalProxy                                    bool
}

func Load() (Config, error) { return FromEnv(os.Getenv) }
func FromEnv(get func(string) string) (Config, error) {
	c := Config{ListenAddress: get("INSTADECK_LISTEN"), DatabasePath: get("INSTADECK_DATABASE"), Origin: get("INSTADECK_ORIGIN"), MasterKeyPath: get("INSTADECK_MASTER_KEY_FILE")}
	if c.ListenAddress == "" {
		c.ListenAddress = "127.0.0.1:8080"
	}
	if c.DatabasePath == "" {
		c.DatabasePath = "instadeck.db"
	}
	host, _, err := net.SplitHostPort(c.ListenAddress)
	if err != nil {
		return Config{}, fmt.Errorf("invalid listen address")
	}
	// TLS terminates at a reverse proxy on the same host, so the application
	// must never accept plain HTTP from the network directly.
	if listenIP := net.ParseIP(host); host != "localhost" && (listenIP == nil || !listenIP.IsLoopback()) {
		return Config{}, fmt.Errorf("listen address must be loopback")
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Hostname() == "" || u.ForceQuery || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return Config{}, fmt.Errorf("invalid configured origin")
	}
	ip := net.ParseIP(u.Hostname())
	loop := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loop) {
		return Config{}, fmt.Errorf("origin requires HTTPS")
	}
	// Browsers send the Origin header with a lowercase host.
	c.Origin = u.Scheme + "://" + strings.ToLower(u.Host)
	if c.MasterKeyPath == "" {
		return Config{}, fmt.Errorf("master key file required")
	}
	if v := get("INSTADECK_TRUST_LOCAL_PROXY"); v != "" {
		c.TrustLocalProxy, err = strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid proxy setting")
		}
	}
	return c, nil
}
