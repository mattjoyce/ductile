package dispatch

import (
	"errors"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

func TestResolveAccount(t *testing.T) {
	cfg := &config.Config{
		Accounts: map[string]config.AccountConf{
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/accounts/untrusted"},
		},
		Plugins: map[string]config.PluginConf{
			"sys_exec": {RunAs: "untrusted"},
			"fetch":    {},               // no grant
			"broken":   {RunAs: "ghost"}, // grants a account that doesn't exist
		},
	}

	t.Run("granted plugin resolves to its account (confined)", func(t *testing.T) {
		got, err := resolveAccount(cfg, "sys_exec")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Confined {
			t.Fatal("expected Confined=true for a granted plugin")
		}
		want := ResolvedAccount{Name: "untrusted", UID: 1002, GID: 1002, StateDir: "/app/data/accounts/untrusted", Confined: true, Source: AccountGranted}
		if got != want {
			t.Fatalf("resolved account mismatch:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("ungranted plugin with no default tier is unconfined", func(t *testing.T) {
		// cfg defines only `untrusted`, no `default` — so an ungranted plugin has no
		// tier to fall back to and runs unconfined (the boot gate decides if that's allowed).
		got, err := resolveAccount(cfg, "fetch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Confined || got.Source != AccountUnconfined {
			t.Fatalf("expected unconfined for an ungranted plugin with no default tier, got %+v", got)
		}
	})

	t.Run("unknown plugin with no default tier is unconfined", func(t *testing.T) {
		got, err := resolveAccount(cfg, "does-not-exist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Confined || got.Source != AccountUnconfined {
			t.Fatalf("expected unconfined for an unknown plugin, got %+v", got)
		}
	})

	t.Run("grant to an undefined account fails closed", func(t *testing.T) {
		_, err := resolveAccount(cfg, "broken")
		if !errors.Is(err, ErrAccountGrantUndefined) {
			t.Fatalf("expected ErrAccountGrantUndefined, got %v", err)
		}
	})

	t.Run("nil config is unconfined", func(t *testing.T) {
		got, err := resolveAccount(nil, "sys_exec")
		if err != nil || got.Confined {
			t.Fatalf("nil config must be unconfined with no error, got %+v err=%v", got, err)
		}
	})
}

// TestResolveAccountDefault covers the #85 Q2 switch: when a `default` tier is
// configured, an ungranted plugin falls back to it (confined), while an explicit
// grant still wins.
func TestResolveAccountDefault(t *testing.T) {
	cfg := &config.Config{
		Accounts: map[string]config.AccountConf{
			"default":   {UID: 1001, GID: 1001, StateDir: "/app/data/accounts/default"},
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/accounts/untrusted"},
		},
		Plugins: map[string]config.PluginConf{
			"sys_exec": {RunAs: "untrusted"},
			"fetch":    {}, // no grant
		},
	}

	t.Run("ungranted plugin falls back to the shared default tier (confined)", func(t *testing.T) {
		got, err := resolveAccount(cfg, "fetch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := ResolvedAccount{Name: "default", UID: 1001, GID: 1001, StateDir: "/app/data/accounts/default", Confined: true, Source: AccountDefault}
		if got != want {
			t.Fatalf("ungranted plugin should resolve to the default tier:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("an unknown plugin also falls back to default when the tier exists", func(t *testing.T) {
		got, err := resolveAccount(cfg, "never-heard-of-it")
		if err != nil || got.Source != AccountDefault {
			t.Fatalf("expected default tier for unknown plugin, got %+v err=%v", got, err)
		}
	})

	t.Run("an explicit grant wins over the default fallback", func(t *testing.T) {
		got, err := resolveAccount(cfg, "sys_exec")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "untrusted" || got.Source != AccountGranted {
			t.Fatalf("explicit grant must win over default, got %+v", got)
		}
	})
}
