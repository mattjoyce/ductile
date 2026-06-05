package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverConfigFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mandatory config.yaml
	writeTestFile(t, filepath.Join(tmpDir, "config.yaml"), "service:\n  name: test\n")

	// Create optional named files
	writeTestFile(t, filepath.Join(tmpDir, "tokens.yaml"), "tokens: []\n")
	writeTestFile(t, filepath.Join(tmpDir, "webhooks.yaml"), "webhooks: []\n")
	writeTestFile(t, filepath.Join(tmpDir, "routes.yaml"), "routes: []\n")
	writeTestFile(t, filepath.Join(tmpDir, "relay-instances.yaml"), "instances: []\n")
	writeTestFile(t, filepath.Join(tmpDir, "relay-ingress.yaml"), "remote_ingress:\n  listen_path: /ingest/peer\n  peers: []\n")

	// Create scopes directory
	if err := os.MkdirAll(filepath.Join(tmpDir, "scopes"), 0o755); err != nil {
		t.Fatalf("mkdir scopes dir: %v", err)
	}
	writeTestFile(t, filepath.Join(tmpDir, "scopes", "admin.json"), `{"scopes":["*"]}`)

	cf, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverConfigFiles() failed: %v", err)
	}

	if cf.Config != filepath.Join(tmpDir, "config.yaml") {
		t.Errorf("Config = %q", cf.Config)
	}
	if cf.Webhooks != filepath.Join(tmpDir, "webhooks.yaml") {
		t.Errorf("Webhooks = %q", cf.Webhooks)
	}
	if cf.Routes != filepath.Join(tmpDir, "routes.yaml") {
		t.Errorf("Routes = %q", cf.Routes)
	}
	if cf.RelayInstances != filepath.Join(tmpDir, "relay-instances.yaml") {
		t.Errorf("RelayInstances = %q", cf.RelayInstances)
	}
	if cf.RelayIngress != filepath.Join(tmpDir, "relay-ingress.yaml") {
		t.Errorf("RelayIngress = %q", cf.RelayIngress)
	}
	if len(cf.Plugins) != 0 {
		t.Fatalf("len(Plugins) = %d, want 0", len(cf.Plugins))
	}
	if len(cf.Pipelines) != 0 {
		t.Fatalf("len(Pipelines) = %d, want 0", len(cf.Pipelines))
	}
	if len(cf.Scopes) != 1 {
		t.Fatalf("len(Scopes) = %d, want 1", len(cf.Scopes))
	}
}

func TestDiscoverConfigFilesMissingConfigYAML(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := DiscoverConfigFiles(tmpDir)
	if err == nil {
		t.Fatal("DiscoverConfigFiles() should fail when config.yaml is missing")
	}
}

func TestDiscoverConfigFilesMinimal(t *testing.T) {
	tmpDir := t.TempDir()
	writeTestFile(t, filepath.Join(tmpDir, "config.yaml"), "service:\n  name: test\n")

	cf, err := DiscoverConfigFiles(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverConfigFiles() failed: %v", err)
	}

	if len(cf.Plugins) != 0 {
		t.Errorf("Plugins should be empty, got %d", len(cf.Plugins))
	}
}

func TestConfigFilesFileTier(t *testing.T) {
	cf := &ConfigFiles{
		Config:   "/etc/ductile/config.yaml",
		Webhooks: "/etc/ductile/webhooks.yaml",
		Scopes:   []string{"/etc/ductile/scopes/admin.json"},
	}

	if cf.FileTier(cf.Webhooks) != TierHighSecurity {
		t.Error("webhooks.yaml should be high security")
	}
	if cf.FileTier(cf.Scopes[0]) != TierHighSecurity {
		t.Error("scopes/*.json should be high security")
	}
	if cf.FileTier(cf.Config) != TierOperational {
		t.Error("config.yaml should be operational")
	}
}

func TestConfigFilesAllFiles(t *testing.T) {
	cf := &ConfigFiles{
		Config:         "/etc/ductile/config.yaml",
		Webhooks:       "/etc/ductile/webhooks.yaml",
		Routes:         "/etc/ductile/routes.yaml",
		RelayInstances: "/etc/ductile/relay-instances.yaml",
		RelayIngress:   "/etc/ductile/relay-ingress.yaml",
		Scopes:         []string{"/etc/ductile/scopes/admin.json"},
	}

	all := cf.AllFiles()
	if len(all) != 6 {
		t.Errorf("AllFiles() returned %d files, want 6", len(all))
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
