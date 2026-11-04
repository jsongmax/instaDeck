package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentNeedsNoConfigurationFile(t *testing.T) {
	environment, err := os.ReadFile(filepath.Join("..", "..", "deploy", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(environment), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "INSTADECK_MASTER_KEY_FILE=") {
			t.Error("EnvironmentFile overrides service credential path")
		}
	}
	if !strings.Contains(string(environment), "service") || !strings.Contains(string(environment), "管理命令") {
		t.Error("missing service and admin key path instructions")
	}
	unit, err := os.ReadFile(filepath.Join("..", "..", "deploy", "instadeck.service"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(unit), "ReadOnlyPaths=/etc/instadeck/master.key") {
		t.Error("redundant master key ReadOnlyPaths remains")
	}
	if strings.Contains(string(unit), "EnvironmentFile=") || strings.Contains(string(unit), "LoadCredential=") {
		t.Fatal("service requires preconfigured files")
	}
	if !strings.Contains(string(unit), "StateDirectory=instadeck") || !strings.Contains(string(unit), "Environment=INSTADECK_DATA_DIR=/var/lib/instadeck") {
		t.Fatal("missing automatic data directory")
	}
}

func TestDockerDeploymentFiles(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	docker := read("Dockerfile")
	for _, value := range []string{"USER 65532:65532", "CGO_ENABLED=0", "INSTADECK_TRUSTED_PROXY=private", "--chown=65532:65532", "healthcheck"} {
		if !strings.Contains(docker, value) {
			t.Errorf("Dockerfile missing %s", value)
		}
	}
	for _, line := range strings.Split(docker, "\n") {
		line = strings.ToUpper(strings.TrimSpace(line))
		if strings.HasPrefix(line, "ENV ") || strings.HasPrefix(line, "ARG ") {
			for _, secret := range []string{"TOKEN", "PASSWORD", "SECRET", "MASTER_KEY", "API_KEY"} {
				if strings.Contains(line, secret) {
					t.Errorf("secret image setting: %s", line)
				}
			}
		}
	}
	compose := read("docker-compose.yml")
	for _, value := range []string{`"127.0.0.1:${INSTADECK_PORT:-8080}:8080"`, "instadeck-data:/data", "profiles: [tunnel]", "condition: service_healthy", "TUNNEL_TOKEN: ${CLOUDFLARE_TUNNEL_TOKEN:-}"} {
		if !strings.Contains(compose, value) {
			t.Errorf("compose missing %s", value)
		}
	}
	parts := strings.SplitN(compose, "  cloudflared:", 2)
	if len(parts) != 2 || !strings.Contains(parts[1], "profiles: [tunnel]") {
		t.Error("cloudflared must be optional")
	}
	for _, line := range strings.Split(compose, "\n") {
		if strings.Contains(line, "TUNNEL_TOKEN:") && strings.TrimSpace(line) != "TUNNEL_TOKEN: ${CLOUDFLARE_TUNNEL_TOKEN:-}" {
			t.Error("literal tunnel token")
		}
	}
	ignore := read(".dockerignore")
	for _, value := range []string{"bin/", ".ccg/", "CLAUDE.md", "*.key", ".env", "web/dist", "*.db*", "**/node_modules", "tests/integration/http-integration-*", "cmd/instadeck/serve-test-*"} {
		if !strings.Contains(ignore, value) {
			t.Errorf("dockerignore missing %s", value)
		}
	}
}
