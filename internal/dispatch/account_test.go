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
		if !got.Drops() {
			t.Fatal("expected Confined=true for a granted plugin")
		}
		want := ResolvedAccount{Name: "untrusted", UID: 1002, GID: 1002, StateDir: "/app/data/accounts/untrusted", Mode: ModeConfined, Source: AccountGranted}
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
		if got.Drops() || got.Source != AccountUnconfined {
			t.Fatalf("expected unconfined for an ungranted plugin with no default tier, got %+v", got)
		}
	})

	t.Run("unknown plugin with no default tier is unconfined", func(t *testing.T) {
		got, err := resolveAccount(cfg, "does-not-exist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Drops() || got.Source != AccountUnconfined {
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
		if err != nil || got.Drops() {
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
		want := ResolvedAccount{Name: "default", UID: 1001, GID: 1001, StateDir: "/app/data/accounts/default", Mode: ModeConfined, Source: AccountDefault}
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

// #100: ResolvedAccount.Validate is the resolve→enforce seam guard. A malformed
// value (especially confined-but-uid-0) must fail CLOSED, never become a root drop.
func TestResolvedAccountValidate(t *testing.T) {
	cases := []struct {
		name string
		r    ResolvedAccount
		ok   bool
	}{
		{"valid confined granted", ResolvedAccount{Name: "default", UID: 1001, GID: 1001, StateDir: "/w", Mode: ModeConfined, Source: AccountGranted}, true},
		{"valid downgraded", ResolvedAccount{Name: "untrusted", UID: 1002, GID: 1002, Mode: ModeConfined, Source: AccountDowngraded}, true},
		{"valid unconfined", ResolvedAccount{Source: AccountUnconfined}, true},
		{"zero value = consistent unconfined", ResolvedAccount{}, true},
		{"confined uid 0 (root) rejected", ResolvedAccount{Name: "x", UID: 0, GID: 1001, Mode: ModeConfined, Source: AccountGranted}, false},
		{"confined gid 0 rejected", ResolvedAccount{Name: "x", UID: 1001, GID: 0, Mode: ModeConfined, Source: AccountGranted}, false},
		{"confined negative uid rejected", ResolvedAccount{Name: "x", UID: -1, GID: 1, Mode: ModeConfined, Source: AccountGranted}, false},
		{"confined valid w/o name/source (ids govern, not metadata)", ResolvedAccount{UID: 1001, GID: 1001, Mode: ModeConfined}, true},
		{"unconfined carrying identity rejected", ResolvedAccount{UID: 1001, Source: AccountUnconfined}, false},
		{"valid credentialed (confined + home)", ResolvedAccount{Name: "trusted", UID: 1000, GID: 1000, Mode: ModeCredentialed, Home: "/home/matt", Source: AccountGranted}, true},
		{"credentialed to root (uid 0) rejected — drop gate is identical", ResolvedAccount{Name: "trusted", UID: 0, GID: 0, Mode: ModeCredentialed, Home: "/root", Source: AccountGranted}, false},
		{"unconfined carrying a home rejected", ResolvedAccount{Home: "/home/matt", Source: AccountUnconfined}, false},
		{"credentialed with relative home rejected (seam, not just config)", ResolvedAccount{Name: "trusted", UID: 1000, GID: 1000, Mode: ModeCredentialed, Home: "rel/home"}, false},
		{"credentialed with root home rejected", ResolvedAccount{Name: "trusted", UID: 1000, GID: 1000, Mode: ModeCredentialed, Home: "/"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want valid, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("want error, got nil")
			}
		})
	}
}

// TestResolvedAccountCredentialed pins the tier predicate: credentialed == drops
// (Confined) AND carries a real home. Confined-without-home stays walled;
// unconfined is never credentialed.
func TestResolvedAccountCredentialed(t *testing.T) {
	cred := ResolvedAccount{UID: 1000, GID: 1000, Mode: ModeCredentialed, Home: "/home/matt"}
	if !cred.Credentialed() {
		t.Error("confined account with a home should be credentialed")
	}
	conf := ResolvedAccount{UID: 1001, GID: 1001, StateDir: "/w", Mode: ModeConfined}
	if conf.Credentialed() {
		t.Error("confined account without a home must not be credentialed")
	}
	if (ResolvedAccount{Source: AccountUnconfined}).Credentialed() {
		t.Error("unconfined account must not be credentialed")
	}
}
