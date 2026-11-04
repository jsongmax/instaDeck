package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentCredentialPathCannotBeOverriddenByExample(t *testing.T) {
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
	if !strings.Contains(string(unit), "LoadCredential=master.key:") || !strings.Contains(string(unit), "INSTADECK_MASTER_KEY_FILE=%d/master.key") {
		t.Fatal("service does not use credential directory")
	}
}
