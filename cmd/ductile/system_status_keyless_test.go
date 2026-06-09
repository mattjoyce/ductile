package main

import (
	"os"
	"path/filepath"
	"testing"
)

// #135: on a credentialed host (#112) the operator CLI is keyless — age.key is
// 0600 service-owned — so the strict load refuses a zero-token bootstrap
// config (vault blind). system status must STILL run the posture probe: it
// needs only the socket path and listen address, and it is the #130
// anti-strand signal built precisely for that operator.
func TestKeylessSystemStatusStillProbesPosture(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	genesisVaultForTest(t, tmp)

	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  unconfined: true
state:
  path: ` + filepath.Join(tmp, "state.db") + `
api:
  enabled: true
  listen: 127.0.0.1:0
`
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Keyless operator: the vault blob stays, the age key goes.
	if err := os.Remove(filepath.Join(tmp, "age.key")); err != nil {
		t.Fatalf("remove age key: %v", err)
	}

	report := collectSystemStatus(configPath)

	var sawLoadFail, sawPosture bool
	for _, c := range report.Checks {
		if c.Name == "config_load" && !c.OK {
			sawLoadFail = true
		}
		if c.Name == "boot_posture" {
			sawPosture = true
		}
	}
	if !sawLoadFail {
		t.Fatal("expected the strict config_load to fail for a keyless caller (precondition)")
	}
	if !sawPosture {
		t.Fatal("keyless system status must still carry the boot_posture probe (#135)")
	}
}
