package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// #134: the reload-failure RESTORE branch calls buildRuntime with NO
// opts.vaultOwner. On a box with validate_config_on_boot armed and a
// management-posture config (zero api tokens, vault present), the admission
// doctor gate must see the vault the fallback loads — otherwise the restore
// errors on api.auth.tokens, rm.runtime goes nil with the old listeners
// already stopped, and the box strands (the exact half-state #130 prevents).
func TestBuildRuntimeRestoreShapeResolvesVaultBeforeAdmission(t *testing.T) {
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

	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  unconfined: true
  admission:
    require_api_auth: true
    validate_config_on_boot: true
state:
  path: ` + filepath.Join(tmp, "state.db") + `
api:
  enabled: true
  listen: ` + publicAddr + `
  management_socket: ` + socket + `
`
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// The restore-call shape: NO vaultOwner in opts (runtime.go restore branch
	// passes none). buildRuntime must resolve the on-disk vault itself before
	// the admission doctor gate runs.
	rt, err := buildRuntime(loaded, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{})
	if err != nil {
		t.Fatalf("restore-shape buildRuntime must not fail admission with a vault on disk: %v", err)
	}
	rt.Stop()
}
