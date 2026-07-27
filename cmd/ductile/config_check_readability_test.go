package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/doctor"
)

// #179, the load-bearing case, and the reproduction this work started from.
//
// A locked, well-formed config whose state DB is present but cannot be opened.
// Before this check, `config check` reported it clean — and
// docs/runbooks/privsep-thinkpad-enforce.md uses `config check ... # MUST be
// clean` as the pre-flight gate before a restart. So the operator was told the
// box was fine, restarted, and took the outage.
func TestConfigCheckReadability_UnopenableStateDBIsCaught(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits; this case needs an unprivileged euid")
	}
	dir, cfg := integrityFixture(t, true, true)

	// Lock it, so the integrity half is clean and readability is what is on trial.
	files, err := config.DiscoverConfigFiles(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if err := config.GenerateChecksumsFromDiscovery(files, false); err != nil {
		t.Fatalf("lock: %v", err)
	}

	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(stateDir, "d.db")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(db, 0o000); err != nil {
		t.Fatal(err)
	}

	// Premise check: the artifact really is unopenable. Without this the test
	// could pass while exercising nothing.
	if f, err := os.Open(db); err == nil {
		f.Close()
		t.Fatalf("fixture is vacuous: %s opened fine", db)
	}

	result := &doctor.Result{Valid: true}
	appendReadabilityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	if result.Valid {
		t.Fatalf("config check reported clean on an unopenable state DB:\n%s", findingsText(result))
	}
	text := findingsText(result)
	if !strings.Contains(text, "readability") {
		t.Fatalf("expected a readability finding, got:\n%s", text)
	}
	if !strings.Contains(text, db) {
		t.Fatalf("the finding must name the file; got:\n%s", text)
	}
	if !strings.Contains(text, "state database") {
		t.Fatalf("the finding must say what the file IS; got:\n%s", text)
	}
}

// The no-regression guarantee: a healthy install gains no findings. A check that
// fires on working boxes gets ignored, which is worse than not having it.
func TestConfigCheckReadability_HealthyInstallStaysClean(t *testing.T) {
	dir, cfg := integrityFixture(t, true, true)

	result := &doctor.Result{Valid: true}
	appendReadabilityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	if !result.Valid {
		t.Fatalf("a readable install must stay clean; got:\n%s", findingsText(result))
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected no readability errors; got:\n%s", findingsText(result))
	}
}

// An unreadable directory is the case that produced #167's misleading message:
// the manifest inside is reported missing when the directory simply cannot be
// searched. Readability runs before integrity precisely so this is diagnosed as
// a permission problem rather than an absent file.
func TestConfigCheckReadability_UnsearchableConfigDirIsDiagnosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses regardless; this case needs an unprivileged euid")
	}
	dir, cfg := integrityFixture(t, true, true)
	sub := filepath.Join(dir, "scopes")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	scope := filepath.Join(sub, "s.json")
	if err := os.WriteFile(scope, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	if f, err := os.Open(scope); err == nil {
		f.Close()
		t.Fatal("fixture is vacuous: opened a file through a mode-0000 directory")
	}

	result := &doctor.Result{Valid: true}
	appendReadabilityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	if result.Valid {
		t.Fatalf("an unsearchable config subdirectory must be caught:\n%s", findingsText(result))
	}
	text := findingsText(result)
	if !strings.Contains(text, sub) {
		t.Fatalf("the finding must name the unreadable directory; got:\n%s", text)
	}
	if !strings.Contains(text, "mode 0000") {
		t.Fatalf("the finding must carry the mode that explains it; got:\n%s", text)
	}
}

// The silent-pass hole, and the reason it needs saying out loud: /etc/ductile
// owned root:ductile mode 0750 is ordinary distro packaging, and an Ansible
// `file` task defaults to root:root. On those hosts the account resolved from
// the directory IS root, root bypasses the permission model, and every finding
// disappears — the check reports clean and is believed. That is the exact shape
// of failure this whole family is about, so a root evaluation must announce
// itself rather than pass quietly.
func TestConfigCheckReadability_RootEvaluationIsReportedAsInconclusive(t *testing.T) {
	dir, cfg := integrityFixture(t, true, true)

	result := &doctor.Result{Valid: true}
	appendReadabilityFindings(result, cfg, filepath.Join(dir, "config.yaml"))

	gateway := config.GatewayAccount(dir)
	warned := strings.Contains(findingsText(result), "does not prove")
	if gateway.UID == 0 && !warned {
		t.Fatalf("a root-owned config dir must warn that the result proves nothing; got:\n%s", findingsText(result))
	}
	if gateway.UID != 0 && warned {
		t.Fatalf("the inconclusive warning fired for a non-root account %s:\n%s", gateway, findingsText(result))
	}
}
