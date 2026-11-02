package config

// The BUG-FIXED half of the parity harness (card 153 slice 3): each check
// states what a correct loader returns for one Problem-now bug. Every check
// fails against load() today and passes against loadAshlar(). When the cutover
// (slice 4) replaces load(), the "fails against load()" assertion flips, and
// that is the signal to delete the matching BUG-FIXED golden entries.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type parityBugCheck struct {
	caseID string
	mode   parityMode
	check  func(cfg *Config, err error) error
}

func wantLoaded(check func(cfg *Config) error) func(*Config, error) error {
	return func(cfg *Config, err error) error {
		if err != nil {
			return fmt.Errorf("load failed: %v", err)
		}
		return check(cfg)
	}
}

var parityBugChecks = []parityBugCheck{
	{
		caseID: "merge/include-turns-api-off",
		mode:   parityModes[1],
		check: wantLoaded(func(cfg *Config) error {
			if cfg.API.Enabled {
				return fmt.Errorf("api.enabled = true; the include set it false")
			}
			return nil
		}),
	},
	{
		caseID: "merge/include-false-replaces-true",
		mode:   parityModes[0],
		check: wantLoaded(func(cfg *Config) error {
			if cfg.RemoteIngress == nil || cfg.RemoteIngress.RequireKeyID {
				return fmt.Errorf("remote_ingress.require_key_id still true; the include set it false")
			}
			return nil
		}),
	},
	{
		caseID: "posture/accounts-in-include",
		mode:   parityModes[0],
		check: wantLoaded(func(cfg *Config) error {
			if len(cfg.Accounts) != 2 {
				return fmt.Errorf("accounts = %d entries; the include declares 2", len(cfg.Accounts))
			}
			return nil
		}),
	},
	{
		caseID: "posture/unconfined-in-include",
		mode:   parityModes[0],
		check: wantLoaded(func(cfg *Config) error {
			if !cfg.Service.Unconfined {
				return fmt.Errorf("service.unconfined = false; the include set it true")
			}
			return nil
		}),
	},
	{
		caseID: "merge/include-dropped-keys",
		mode:   parityModes[0],
		check: wantLoaded(func(cfg *Config) error {
			s := cfg.Service
			if s.MaxWorkers != 3 || s.HookMaxDepth != 6 || !s.StrictMode || !s.AllowSymlinks ||
				s.Admission == nil || len(s.PluginEnvPassthrough) != 1 || len(cfg.TCCPaths) != 1 ||
				cfg.Secrets.VaultFile != "other-vault.age" || cfg.Database.Path != "./other.db" {
				return fmt.Errorf("an included key was dropped: service=%+v tcc=%v secrets=%+v database=%+v",
					s, cfg.TCCPaths, cfg.Secrets, cfg.Database)
			}
			return nil
		}),
	},
	{
		caseID: "env/dotenv-from-later-file",
		mode:   parityModes[0],
		check: wantLoaded(func(cfg *Config) error {
			if len(cfg.EnvironmentVars.Include) != 1 {
				return fmt.Errorf("environment_vars.include = %v; the include declares late.env", cfg.EnvironmentVars.Include)
			}
			return nil
		}),
	},
	{
		caseID: "env/value-with-colon-space",
		mode:   parityModes[0],
		check: wantLoaded(func(cfg *Config) error {
			if len(cfg.TCCPaths) != 1 || cfg.TCCPaths[0] != "a: b" {
				return fmt.Errorf("tcc_paths = %q; want the variable's value verbatim", cfg.TCCPaths)
			}
			return nil
		}),
	},
}

func parityCaseByID(t *testing.T, id string) parityCase {
	t.Helper()
	for _, c := range parityHandBuiltCases() {
		if c.id == id {
			return c
		}
	}
	t.Fatalf("no hand-built parity case %q", id)
	return parityCase{}
}

func TestParityBugFixedFailsAgainstLegacy(t *testing.T) {
	isolateParityEnv(t)
	for _, bc := range parityBugChecks {
		t.Run(bc.caseID, func(t *testing.T) {
			c := parityCaseByID(t, bc.caseID)
			path := c.materialize(t)
			legacy := runLegacyLoad(path, bc.mode, c.env)
			if err := bc.check(legacy.cfg, legacy.err); err == nil {
				t.Errorf("load() now passes the check; the bug is fixed in load(), so retire the BUG-FIXED entries for %s", bc.caseID)
			}
			next := runAshlarLoad(path, bc.mode, c.env)
			if err := bc.check(next.cfg, next.err); err != nil {
				t.Errorf("loadAshlar() fails the check: %v", err)
			}
			bugFixed := 0
			for _, d := range diffParitySides(legacy, next) {
				if e, ok := parityGolden[bc.caseID+" "+bc.mode.name+" "+d.key]; ok && e.code == codeBugFixed {
					bugFixed++
				}
			}
			if bugFixed == 0 {
				t.Errorf("no %s golden entry for %s in %s mode; the difference this check proves is a bug fix", codeBugFixed, bc.caseID, bc.mode.name)
			}
		})
	}
}

// TestParityScopeVerifierMatchesLoad compares the scope-file integrity gate in
// the verify-only mode (verify, no validation): an included webhooks.yaml must
// match .checksums in both loaders, and both refuse when it does not.
func TestParityScopeVerifierMatchesLoad(t *testing.T) {
	isolateParityEnv(t)
	verifyOnly := parityMode{name: "verify-only", verify: true}
	c := parityCase{
		id: "scope/webhooks",
		files: map[string]string{
			"config.yaml":   parityRoot("include:\n  - webhooks.yaml\n"),
			"webhooks.yaml": "webhooks:\n  listen: 127.0.0.1:18798\n  endpoints: []\n",
		},
	}
	path := c.materialize(t)
	dir := filepath.Dir(path)
	hook := filepath.Join(dir, "webhooks.yaml")

	both := func(label string, wantOK bool) {
		t.Helper()
		legacy := runLegacyLoad(path, verifyOnly, nil)
		next := runAshlarLoad(path, verifyOnly, nil)
		if (legacy.err == nil) != wantOK || (next.err == nil) != wantOK {
			t.Fatalf("%s: want ok=%t; legacy err=%v, ashlar err=%v", label, wantOK, legacy.err, next.err)
		}
	}

	both("no .checksums", false)

	data, err := os.ReadFile(hook)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	writeChecksumManifest(t, dir, ChecksumManifest{
		Version: 3, GeneratedAt: "2026-11-03T00:00:00Z",
		Hashes: map[string]string{hook: "sha256:" + hex.EncodeToString(sum[:])},
	})
	both("locked", true)

	if err := os.WriteFile(hook, append(data, []byte("# edited after lock\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	both("edited after lock", false)
}
