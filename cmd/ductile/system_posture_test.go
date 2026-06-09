package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
)

// TestSystemStatusReportsLiveManagementPosture proves #130's observability: with a
// daemon actually running in the management posture, `system status` probes the
// live /healthz and reports management-only — not a config guess.
func TestSystemStatusReportsLiveManagementPosture(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	genesisVaultForTest(t, tmp)

	sockDir, err := os.MkdirTemp("/tmp", "dtl")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "v.sock")
	publicAddr := freeLocalAddr(t)
	configPath := filepath.Join(tmp, "config.yaml")

	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
state:
  path: ` + filepath.Join(tmp, "state.db") + `
api:
  enabled: true
  listen: ` + publicAddr + `
  management_socket: ` + socket + `
`
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault: %v", err)
	}
	rt, err := buildRuntime(cfg, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{vaultOwner: owner})
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	defer rt.Stop()

	// Wait for the management socket to answer before probing via status.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, ok := probeUnixHealthz(socket); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("management socket never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	report := collectSystemStatus(configPath)
	var posture *systemStatusCheck
	for i := range report.Checks {
		if report.Checks[i].Name == "boot_posture" {
			posture = &report.Checks[i]
		}
	}
	if posture == nil {
		t.Fatal("system status has no boot_posture check")
	}
	if !strings.Contains(posture.Detail, "management-only") || !strings.Contains(posture.Detail, "live") {
		t.Fatalf("boot_posture detail = %q, want live management-only", posture.Detail)
	}
	// A management-only posture is intentional, so it must not flip overall health.
	if !report.Healthy {
		t.Errorf("management posture should not make system status unhealthy: %+v", report.Checks)
	}
}

// TestDialableAddr covers the wildcard/empty-host normalisation the probe relies on.
func TestDialableAddr(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8080": "127.0.0.1:8080",
		"0.0.0.0:8080":   "127.0.0.1:8080",
		":8080":          "127.0.0.1:8080",
		"":               "",
		"garbage":        "",
	}
	for in, want := range cases {
		if got := dialableAddr(in); got != want {
			t.Errorf("dialableAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
