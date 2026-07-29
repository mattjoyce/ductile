package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lockedFixture builds a config dir with one high-security file and a valid
// manifest, then hands back the manifest path for the caller to corrupt.
func lockedFixture(t *testing.T) (dir string, files *ConfigFiles, manifest string) {
	t.Helper()
	dir = t.TempDir()
	webhooks := filepath.Join(dir, "webhooks.yaml")
	if err := os.WriteFile(webhooks, []byte("webhooks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateChecksumsWithReport(dir, []string{"webhooks.yaml"}, false); err != nil {
		t.Fatal(err)
	}
	return dir, &ConfigFiles{Root: dir, Webhooks: webhooks}, filepath.Join(dir, ".checksums")
}

func integrityReport(t *testing.T, dir string, files *ConfigFiles) (*IntegrityResult, string) {
	t.Helper()
	res, err := VerifyIntegrity(dir, files)
	if err != nil {
		t.Fatalf("VerifyIntegrity: %v", err)
	}
	return res, strings.Join(append(append([]string{}, res.Errors...), res.Warnings...), " | ")
}

// #167: LoadChecksums must keep the not-exist cause reachable, because that is
// the signal callers use to tell "never locked" from "locked but unreadable".
func TestLoadChecksums_MissingIsErrNotExist(t *testing.T) {
	_, err := LoadChecksums(t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a missing manifest")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("errors.Is(err, fs.ErrNotExist) = false for %v", err)
	}
}

// #167, the headline defect: an unreadable manifest was reported as a missing
// one, sending the operator to `config lock` — which under sudo rewrites the same
// root-owned file and loops them straight back into the failed boot.
func TestVerifyIntegrity_UnreadableManifestIsNotReportedAsMissing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir, files, manifest := lockedFixture(t)
	if err := os.Chmod(manifest, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0600) })

	res, report := integrityReport(t, dir, files)

	if strings.Contains(report, "no .checksums manifest found") {
		t.Fatalf("EACCES reported as a missing manifest: %s", report)
	}
	if !strings.Contains(report, "permission denied") {
		t.Fatalf("expected the permission cause to survive, got: %s", report)
	}
	if strings.Contains(report, "run 'ductile config lock'") {
		t.Fatalf("permission failure must not suggest the loop-inducing remediation: %s", report)
	}
	if res.Passed {
		t.Fatalf("an unusable manifest must fail closed, got Passed=true: %s", report)
	}
}

// #167: fail closed even with nothing high-security on disk. A *missing* manifest
// only warns there, but "enabled and uncheckable" must not silently degrade to a
// warning — verify_integrity_on_boot would pass while verifying nothing.
func TestVerifyIntegrity_UnreadableManifestFailsClosedWithoutHighSecurityFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir, files, manifest := lockedFixture(t)
	files.Webhooks = "" // drop the high-security tier; the file stays on disk
	if err := os.Chmod(manifest, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(manifest, 0600) })

	res, report := integrityReport(t, dir, files)
	if res.Passed {
		t.Fatalf("expected fail-closed, got Passed=true: %s", report)
	}
}

// #167: the collapse was wider than the issue reported — VerifyIntegrity threw
// the error away entirely, so a corrupt manifest also claimed to be missing.
func TestVerifyIntegrity_CorruptManifestReportsParseFailure(t *testing.T) {
	dir, files, manifest := lockedFixture(t)
	if err := os.WriteFile(manifest, []byte("{{{ not yaml"), 0600); err != nil {
		t.Fatal(err)
	}

	res, report := integrityReport(t, dir, files)
	if strings.Contains(report, "no .checksums manifest found") {
		t.Fatalf("parse failure reported as a missing manifest: %s", report)
	}
	if !strings.Contains(report, "parse") {
		t.Fatalf("expected a parse error, got: %s", report)
	}
	if res.Passed {
		t.Fatalf("expected fail-closed, got Passed=true: %s", report)
	}
}

// #167: same collapse, third cause — a v1 manifest is unsupported, not absent.
func TestVerifyIntegrity_UnsupportedVersionReportsVersion(t *testing.T) {
	dir, files, manifest := lockedFixture(t)
	if err := os.WriteFile(manifest, []byte("version: 1\nhashes: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	res, report := integrityReport(t, dir, files)
	if strings.Contains(report, "no .checksums manifest found") {
		t.Fatalf("version mismatch reported as a missing manifest: %s", report)
	}
	if !strings.Contains(report, "unsupported checksums version") {
		t.Fatalf("expected the version error, got: %s", report)
	}
	if res.Passed {
		t.Fatalf("expected fail-closed, got Passed=true: %s", report)
	}
}

// #167 regression guard: the genuinely-missing path keeps its wording and its
// error/warning tiering. That message is correct — it was only wrong when it
// stood in for the other three causes.
func TestVerifyIntegrity_MissingManifestWordingUnchanged(t *testing.T) {
	dir, files, manifest := lockedFixture(t)
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}

	res, report := integrityReport(t, dir, files)
	if !strings.Contains(report, "no .checksums manifest found") {
		t.Fatalf("missing-manifest wording changed: %s", report)
	}
	if !strings.Contains(report, "run 'ductile config lock'") {
		t.Fatalf("missing-manifest remediation dropped: %s", report)
	}
	if res.Passed {
		t.Fatalf("high-security files with no manifest must fail: %s", report)
	}

	// …and with nothing high-security, still only a warning.
	files.Webhooks = ""
	res, report = integrityReport(t, dir, files)
	if !res.Passed || len(res.Warnings) == 0 {
		t.Fatalf("expected warn-only for a missing manifest with no high-security files: %s", report)
	}
}
