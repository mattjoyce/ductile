package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePolicyMarginConfig builds a minimal config dir: one discovered plugin,
// and a vault holding the single api token's secret. It reuses seedVaultSecrets
// (plugin_fingerprint_wiring_test.go) for the vault and the api.auth.tokens
// shape from writeBootConfig (boot_failclosed_test.go). Returns the config dir.
func writePolicyMarginConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	pluginDir := filepath.Join(dir, "plugins", "gmail")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: gmail
version: 0.1.0
protocol: 2
entrypoint: gmail
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "gmail"), []byte("#!/bin/sh\necho gmail\n"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	// The vault is the sole secret source; seed the token's secret so
	// LoadWithVault resolves its secret_ref (the policy itself needs only the
	// name and scopes).
	seedVaultSecrets(t, dir, map[string]string{"ductile-api-admin": "policy-margin-secret"})

	cfg := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
api:
  enabled: true
  listen: 127.0.0.1:0
  auth:
    tokens:
      - secret_ref: ductile-api-admin
        scopes: ["*"]
plugins:
  gmail:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
	return dir
}

// TestRunConfigCheckReportsPolicyMargin is D5: `config check` on a config with
// the API enabled and one token exits 0 and prints the token policy's cost
// margin line, so an operator sees the margin at load time without re-deciding.
func TestRunConfigCheckReportsPolicyMargin(t *testing.T) {
	dir := writePolicyMarginConfig(t)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigCheck([]string{"--config-dir", dir})
	})
	if code != 0 {
		t.Fatalf("runConfigCheck() code = %d, stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "token policy: worst decision") {
		t.Fatalf("stdout missing the token policy margin line:\n%s", stdout)
	}
}
