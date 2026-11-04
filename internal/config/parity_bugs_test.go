package config

// The BUG-FIXED half of the parity harness (card 153 slice 3): each check
// asserts the exact values a correct loader returns for one Problem-now bug.
// Every check fails against load() today and passes against loadAshlar().
// When the cutover (slice 4) replaces load(), the "fails against load()"
// assertion flips, and that is the signal to delete the matching BUG-FIXED
// golden entries.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mattjoyce/ashlar"
)

type parityBugCheck struct {
	caseID string
	mode   parityMode
	check  func(cfg *Config, dir string) error
}

// wantEqual compares got and want with reflect.DeepEqual and names the field.
func wantEqual(field string, got, want any) error {
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s = %#v, want %#v", field, got, want)
	}
	return nil
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

var parityBugChecks = []parityBugCheck{
	{
		caseID: "merge/include-turns-api-off",
		mode:   parityModes[1],
		check: func(cfg *Config, _ string) error {
			return firstError(
				wantEqual("api.enabled", cfg.API.Enabled, false),
				wantEqual("api.listen", cfg.API.Listen, "127.0.0.1:18799"),
			)
		},
	},
	{
		caseID: "merge/include-false-replaces-true",
		mode:   parityModes[0],
		check: func(cfg *Config, _ string) error {
			if cfg.RemoteIngress == nil {
				return fmt.Errorf("remote_ingress is nil")
			}
			return firstError(
				wantEqual("remote_ingress.require_key_id", cfg.RemoteIngress.RequireKeyID, false),
				wantEqual("remote_ingress.listen_path", cfg.RemoteIngress.ListenPath, "/relay"),
			)
		},
	},
	{
		caseID: "posture/accounts-in-include",
		mode:   parityModes[0],
		check: func(cfg *Config, dir string) error {
			return wantEqual("accounts", cfg.Accounts, map[string]AccountConf{
				"default":   {UID: 4181, GID: 4181, StateDir: filepath.Join(dir, "accounts/default")},
				"untrusted": {UID: 4182, GID: 4182, StateDir: filepath.Join(dir, "accounts/untrusted")},
			})
		},
	},
	{
		caseID: "posture/unconfined-in-include",
		mode:   parityModes[0],
		check: func(cfg *Config, _ string) error {
			return wantEqual("service.unconfined", cfg.Service.Unconfined, true)
		},
	},
	{
		caseID: "merge/include-dropped-keys",
		mode:   parityModes[0],
		check: func(cfg *Config, dir string) error {
			s := cfg.Service
			return firstError(
				wantEqual("service.max_workers", s.MaxWorkers, 3),
				wantEqual("service.hook_max_depth", s.HookMaxDepth, 6),
				wantEqual("service.strict_mode", s.StrictMode, true),
				wantEqual("service.allow_symlinks", s.AllowSymlinks, true),
				wantEqual("service.unconfined", s.Unconfined, true),
				wantEqual("service.plugin_env_passthrough", s.PluginEnvPassthrough, []string{"PARITY_PASS"}),
				wantEqual("service.admission", s.Admission, &AdmissionConfig{VerifyIntegrityOnBoot: true, FailOnDrift: true}),
				wantEqual("accounts", cfg.Accounts, map[string]AccountConf{
					"default": {UID: 4181, GID: 4181, StateDir: filepath.Join(dir, "accounts/default")},
				}),
				wantEqual("tcc_paths", cfg.TCCPaths, []string{filepath.Join(dir, "tcc")}),
				wantEqual("secrets.vault_file", cfg.Secrets.VaultFile, "other-vault.age"),
				wantEqual("database.path", cfg.Database.Path, "./other.db"),
			)
		},
	},
	{
		caseID: "env/dotenv-from-later-file",
		mode:   parityModes[0],
		check: func(cfg *Config, _ string) error {
			return wantEqual("environment_vars.include", cfg.EnvironmentVars.Include, []string{"late.env"})
		},
	},
	{
		caseID: "env/value-with-colon-space",
		mode:   parityModes[0],
		check: func(cfg *Config, _ string) error {
			return wantEqual("tcc_paths", cfg.TCCPaths, []string{"a: b"})
		},
	},
}

func parityCaseByID(t *testing.T, id string) parityCase {
	t.Helper()
	for _, c := range append(parityHandBuiltCases(), parityDirectoryTestCases()...) {
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
			r := newParityRun(t, parityCaseByID(t, bc.caseID))
			legacy := runLegacyLoad(r, bc.mode)
			if legacy.err == nil {
				if err := bc.check(legacy.cfg, r.dir); err == nil {
					t.Errorf("load() now passes the check; the bug is fixed in load(), so retire the BUG-FIXED entries for %s", bc.caseID)
				}
			}
			next := runAshlarLoad(r, bc.mode)
			if next.err != nil {
				t.Fatalf("loadAshlar() failed: %v", next.err)
			}
			if err := bc.check(next.cfg, r.dir); err != nil {
				t.Errorf("loadAshlar() fails the check: %v", err)
			}
			bugFixed := 0
			for _, d := range diffParitySides(r, legacy, next) {
				if e, ok := parityGolden[bc.caseID+" "+bc.mode.name+" "+d.key]; ok && e.class.code == codeBugFixed {
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
// match .checksums in both loaders, and both refuse, for the same cause, when
// it does not.
func TestParityScopeVerifierMatchesLoad(t *testing.T) {
	isolateParityEnv(t)
	verifyOnly := parityMode{name: "verify-only", verify: true}
	r := newParityRun(t, parityCase{
		id: "scope/webhooks",
		files: map[string]string{
			"config.yaml":   parityRoot("include:\n  - webhooks.yaml\n"),
			"webhooks.yaml": "webhooks:\n  listen: 127.0.0.1:18798\n  endpoints: []\n",
		},
	})
	hook := filepath.Join(r.dir, "webhooks.yaml")

	both := func(label string, wantOK bool) {
		t.Helper()
		legacy := runLegacyLoad(r, verifyOnly)
		next := runAshlarLoad(r, verifyOnly)
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
	writeChecksumManifest(t, r.dir, ChecksumManifest{
		Version: 3, GeneratedAt: "2026-11-03T00:00:00Z",
		Hashes: map[string]string{hook: "sha256:" + hex.EncodeToString(sum[:])},
	})
	both("locked", true)

	if err := os.WriteFile(hook, append(data, []byte("# edited after lock\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	both("edited after lock", false)
}

// TestParityNullOverMappingNamesBothFiles pins Matt's 2026-11-04 ruling on
// decision I: a bare `plugins:` in an include, composed over the root's
// plugins mapping, is refused as a composition error, and the error tells the
// operator where both sides are, the root's mapping and the include's null,
// by file:line:column.
func TestParityNullOverMappingNamesBothFiles(t *testing.T) {
	isolateParityEnv(t)
	r := newParityRun(t, parityCaseByID(t, "merge/null-plugins-in-include"))
	for _, m := range parityModes {
		next := runAshlarLoad(r, m)
		if next.err == nil {
			t.Fatalf("%s: loadAshlar accepted a null over the plugins mapping", m.name)
		}
		if !errors.Is(next.err, ashlar.ErrComposition) {
			t.Errorf("%s: error %v is not ashlar.ErrComposition", m.name, next.err)
		}
		msg := caseNormaliser(r.dir)(next.err.Error())
		for _, want := range []string{"$CASE/config.yaml:10:3", "$CASE/null.yaml:1:9", "(/plugins)"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: error does not name %s:\n  %s", m.name, want, msg)
			}
		}
	}
}
