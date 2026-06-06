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

	cases := []struct {
		name    string
		account AccountConf
		want    string
	}{
		{"uid zero (root) rejected", AccountConf{UID: 0, GID: 1001, StateDir: "/s"}, "uid must be a positive"},
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
