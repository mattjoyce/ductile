package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestAccountYAMLKeys pins the privsep YAML surface (accounts: + plugin run_as:)
// to the Go struct tags. A tag typo would silently drop the keys, so this guards
// the rename from worker:/workers: → run_as:/accounts: (2026-06-07).
func TestAccountYAMLKeys(t *testing.T) {
	const doc = `
accounts:
  default:   {uid: 1001, gid: 1001, state_dir: /var/lib/ductile/accounts/default}
  untrusted: {uid: 1002, gid: 1002, state_dir: /var/lib/ductile/accounts/untrusted}
plugins:
  sys_exec:
    enabled: true
    run_as: untrusted
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.Accounts) != 2 {
		t.Fatalf("accounts: got %d entries, want 2 (yaml tag binding broken?)", len(cfg.Accounts))
	}
	if a := cfg.Accounts["untrusted"]; a.UID != 1002 || a.GID != 1002 || a.StateDir != "/var/lib/ductile/accounts/untrusted" {
		t.Fatalf("accounts.untrusted parsed wrong: %+v", a)
	}
	if got := cfg.Plugins["sys_exec"].RunAs; got != "untrusted" {
		t.Fatalf("plugins.sys_exec.run_as: got %q, want untrusted (yaml tag binding broken?)", got)
	}
}
