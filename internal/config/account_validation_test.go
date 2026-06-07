package config

import (
	"strings"
	"testing"
)

func TestValidateAccounts(t *testing.T) {
	valid := func() map[string]AccountConf {
		return map[string]AccountConf{
			"default":   {UID: 1001, GID: 1001, StateDir: "/app/data/workers/default"},
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/workers/untrusted"},
		}
	}

	t.Run("two-tier default posture is valid", func(t *testing.T) {
		if err := validateAccounts(&Config{Accounts: valid()}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("absent/empty map is valid (resolves via the boot gate, not here)", func(t *testing.T) {
		if err := validateAccounts(&Config{}); err != nil {
			t.Fatalf("nil map: %v", err)
		}
		if err := validateAccounts(&Config{Accounts: map[string]AccountConf{}}); err != nil {
			t.Fatalf("empty map: %v", err)
		}
	})

	t.Run("a third row loads fine — open map, not capped at two", func(t *testing.T) {
		w := valid()
		w["isolated"] = AccountConf{UID: 1003, GID: 1003, StateDir: "/app/data/workers/isolated"}
		if err := validateAccounts(&Config{Accounts: w}); err != nil {
			t.Fatalf("third account rejected: %v", err)
		}
	})

	t.Run("duplicate uid is false isolation → rejected", func(t *testing.T) {
		w := valid()
		w["sneaky"] = AccountConf{UID: 1001, GID: 1009, StateDir: "/app/data/workers/sneaky"}
		err := validateAccounts(&Config{Accounts: w})
		if err == nil || !strings.Contains(err.Error(), "false isolation") {
			t.Fatalf("expected duplicate-uid rejection, got %v", err)
		}
	})

	t.Run("credentialed account (non-default, with home) is valid", func(t *testing.T) {
		w := valid()
		w["trusted"] = AccountConf{UID: 1000, GID: 1000, Home: "/home/matt"}
		if err := validateAccounts(&Config{Accounts: w}); err != nil {
			t.Fatalf("credentialed account rejected: %v", err)
		}
	})

	t.Run("home on the default fallback tier is rejected (silent-escalation guard)", func(t *testing.T) {
		w := valid()
		d := w["default"]
		d.Home = "/home/matt"
		w["default"] = d
		err := validateAccounts(&Config{Accounts: w})
		if err == nil || !strings.Contains(err.Error(), "fallback tier must not be credentialed") {
			t.Fatalf("expected default-credentialed rejection, got %v", err)
		}
	})

	cases := []struct {
		name    string
		account AccountConf
		want    string
	}{
		{"uid zero (root) rejected", AccountConf{UID: 0, GID: 1001, StateDir: "/s"}, "uid must be a positive"},
		{"relative credentialed home rejected", AccountConf{UID: 1000, GID: 1000, Home: "rel/home"}, "home must be an absolute path"},
		{"negative uid rejected", AccountConf{UID: -5, GID: 1001, StateDir: "/s"}, "uid must be a positive"},
		{"gid zero rejected", AccountConf{UID: 1001, GID: 0, StateDir: "/s"}, "gid must be a positive"},
		{"absurd uid rejected (overflow guard)", AccountConf{UID: 1 << 33, GID: 1001, StateDir: "/s"}, "uid must be a positive"},
		{"relative state_dir rejected", AccountConf{UID: 1001, GID: 1001, StateDir: "data/w"}, "must be an absolute path"},
		{"empty state_dir rejected", AccountConf{UID: 1001, GID: 1001, StateDir: ""}, "must be an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAccounts(&Config{Accounts: map[string]AccountConf{"w": tc.account}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

// TestValidateAccountGrants covers the boot-time grant-resolution pass the luminary
// review (Hickey A3/B3, Brooks F1, O×L F2) asked for: a `run_as` grant naming an
// account the `accounts` map does not define is a misconfiguration knowable at boot,
// not a per-job surprise at first spawn. Fail closed at config load.
func TestValidateAccountGrants(t *testing.T) {
	accounts := func() map[string]AccountConf {
		return map[string]AccountConf{
			"default":   {UID: 1001, GID: 1001, StateDir: "/app/data/accounts/default"},
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/accounts/untrusted"},
		}
	}

	t.Run("grant naming a defined account is valid", func(t *testing.T) {
		cfg := &Config{
			Accounts: accounts(),
			Plugins: map[string]PluginConf{
				"withings": {RunAs: "default"},
				"sys_exec": {RunAs: "untrusted"},
			},
		}
		if err := validateAccountGrants(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("ungranted plugin (empty run_as) is valid", func(t *testing.T) {
		cfg := &Config{
			Accounts: accounts(),
			Plugins:  map[string]PluginConf{"fetch": {}},
		}
		if err := validateAccountGrants(cfg); err != nil {
			t.Fatalf("ungranted plugin rejected: %v", err)
		}
	})

	t.Run("typo'd grant fails at config load, not at spawn", func(t *testing.T) {
		cfg := &Config{
			Accounts: accounts(),
			Plugins:  map[string]PluginConf{"withings": {RunAs: "defualt"}},
		}
		err := validateAccountGrants(cfg)
		if err == nil || !strings.Contains(err.Error(), "undefined account") {
			t.Fatalf("expected undefined-account rejection, got %v", err)
		}
		if !strings.Contains(err.Error(), "withings") || !strings.Contains(err.Error(), "defualt") {
			t.Fatalf("error should name the plugin and the bad grant, got %v", err)
		}
	})

	t.Run("grant with no accounts table at all fails closed", func(t *testing.T) {
		cfg := &Config{
			Plugins: map[string]PluginConf{"withings": {RunAs: "default"}},
		}
		if err := validateAccountGrants(cfg); err == nil {
			t.Fatal("a run_as grant with no accounts map must fail closed")
		}
	})
}
