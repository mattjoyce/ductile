package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/doctor"
)

// integrityFixture builds a config dir with one high-security file (webhooks.yaml)
// and returns the dir plus a loaded config with the given admission policy.
func integrityFixture(t *testing.T, verifyOnBoot, failOnDrift bool) (string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "webhooks.yaml"), []byte("webhooks: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	yaml := "service:\n  strict_mode: false\n  unconfined: true\n  admission:\n" +
		"    verify_integrity_on_boot: " + ynStr(verifyOnBoot) + "\n" +
		"    fail_on_drift: " + ynStr(failOnDrift) + "\n" +
		"    validate_config_on_boot: true\n" +
		"state:\n  path: \"" + filepath.Join(dir, "state", "d.db") + "\"\n" +
		"plugin_roots:\n  - \"" + filepath.Join(dir, "plugins") + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load fixture config: %v", err)
	}
	return dir, cfg
}

func ynStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func findingsText(r *doctor.Result) string {
	var b strings.Builder
	for _, e := range r.Errors {
		b.WriteString("ERR[" + e.Category + "] " + e.Message + "\n")
	}
	for _, w := range r.Warnings {
		b.WriteString("WARN[" + w.Category + "] " + w.Message + "\n")
	}
	return b.String()
}

// #174: the load-bearing case. An unlocked config with high-security files is a
// box that will not boot under verify_integrity_on_boot, and `config check` used
// to call it clean — which is why the enforce runbook's pre-flight gate passed
// on installs that then took an outage.
func TestConfigCheckIntegrity_UnlockedHighSecurityIsCaught(t *testing.T) {
	dir, cfg := integrityFixture(t, true, true)
	result := &doctor.Result{Valid: true}

	appendIntegrityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	if result.Valid {
		t.Fatalf("expected config check to fail for an unlocked high-security config; got clean:\n%s", findingsText(result))
	}
	if !strings.Contains(findingsText(result), "checksums") {
		t.Fatalf("expected a .checksums finding, got:\n%s", findingsText(result))
	}
}

// #174: a locked, intact config must stay clean — the check gains a dimension,
// it does not become noisy.
func TestConfigCheckIntegrity_LockedConfigStaysClean(t *testing.T) {
	dir, cfg := integrityFixture(t, true, true)
	files, err := config.DiscoverConfigFiles(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("lock: %v", err)
	}

	result := &doctor.Result{Valid: true}
	appendIntegrityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	if !result.Valid {
		t.Fatalf("a locked, intact config must pass; got:\n%s", findingsText(result))
	}
}

// #174, the no-regression guarantee. verify_integrity_on_boot: false means the
// daemon does not verify at boot, so neither may this check. Installs that never
// enabled integrity must see no verdict change — that is what keeps this from
// breaking existing deployments.
func TestConfigCheckIntegrity_SkippedWhenPolicyDisabled(t *testing.T) {
	dir, cfg := integrityFixture(t, false, true)
	result := &doctor.Result{Valid: true}

	appendIntegrityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	if !result.Valid {
		t.Fatalf("policy is off; check must not fail. got:\n%s", findingsText(result))
	}
	if len(result.Errors) != 0 {
		t.Fatalf("policy is off; expected no errors, got:\n%s", findingsText(result))
	}
	// Visible rather than silent: an operator must be able to tell that the
	// integrity dimension was not exercised.
	if !strings.Contains(findingsText(result), "verify_integrity_on_boot is false") {
		t.Fatalf("skip must be reported, not silent; got:\n%s", findingsText(result))
	}
}

// #174: fail_on_drift is mirrored from the daemon. Operational drift is fatal
// here exactly where verifyReloadIntegrity makes it fatal at boot, and only there.
func TestConfigCheckIntegrity_DriftFollowsFailOnDriftPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failOnDrift bool
		wantValid   bool
	}{
		{"drift-fatal", true, false},
		{"drift-warns", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, cfg := integrityFixture(t, true, tc.failOnDrift)
			files, err := config.DiscoverConfigFiles(dir)
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
				t.Fatalf("lock: %v", err)
			}
			// Mutate an operational-tier file so its hash no longer matches.
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
				[]byte("service:\n  strict_mode: false\n  unconfined: true\n"+
					"  admission:\n    verify_integrity_on_boot: true\n"+
					"    fail_on_drift: "+ynStr(tc.failOnDrift)+"\n"+
					"    validate_config_on_boot: true\n"+
					"state:\n  path: \""+filepath.Join(dir, "state", "d.db")+"\"\n"+
					"plugin_roots:\n  - \""+filepath.Join(dir, "plugins")+"\"\n"+
					"# drift\n"), 0600); err != nil {
				t.Fatal(err)
			}

			result := &doctor.Result{Valid: true}
			appendIntegrityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

			if result.Valid != tc.wantValid {
				t.Fatalf("valid = %v, want %v (fail_on_drift=%v); findings:\n%s",
					result.Valid, tc.wantValid, tc.failOnDrift, findingsText(result))
			}
			if tc.failOnDrift && !strings.Contains(findingsText(result), "fail_on_drift") {
				t.Fatalf("fatal drift should name the policy that promoted it:\n%s", findingsText(result))
			}
		})
	}
}

// #174: an unreadable manifest must surface here with the real cause, so the
// pre-flight gate reports what the boot would. Pairs with #167's read-side fix.
func TestConfigCheckIntegrity_UnreadableManifestReportsPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir, cfg := integrityFixture(t, true, true)
	files, err := config.DiscoverConfigFiles(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("lock: %v", err)
	}
	manifest := filepath.Join(dir, ".checksums")
	if err := os.Chmod(manifest, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0600) })

	result := &doctor.Result{Valid: true}
	appendIntegrityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	text := findingsText(result)
	if result.Valid {
		t.Fatalf("unreadable manifest must fail the check; got clean:\n%s", text)
	}
	if strings.Contains(text, "no .checksums manifest found") {
		t.Fatalf("EACCES misreported as missing:\n%s", text)
	}
	if !strings.Contains(text, "permission denied") {
		t.Fatalf("expected the permission cause:\n%s", text)
	}
}
