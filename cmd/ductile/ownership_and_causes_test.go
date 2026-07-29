//go:build darwin || linux || freebsd || openbsd || netbsd

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func ownerOfPath(t *testing.T, path string) (int, int) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no Unix stat on this platform")
	}
	return int(st.Uid), int(st.Gid)
}

func requireRootForOwnership(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to change file ownership")
	}
}

// #169: a config edit under sudo must leave the file owned by the service
// account, not by the caller. This is the same defect as #167 in a second helper,
// and it is worse — these are the files the daemon needs in order to boot.
func TestWriteFileAtomicWithBackup_InheritsDirectoryOwner(t *testing.T) {
	requireRootForOwnership(t)
	dir := t.TempDir()
	if err := os.Chown(dir, 12345, 12345); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}
	path := filepath.Join(dir, "config.yaml")

	if err := writeFileAtomicWithBackup(path, []byte("service:\n  name: x\n"), 0600); err != nil {
		t.Fatalf("writeFileAtomicWithBackup: %v", err)
	}
	if uid, gid := ownerOfPath(t, path); uid != 12345 || gid != 12345 {
		t.Fatalf("config owner = %d:%d, want the directory's 12345:12345", uid, gid)
	}
	if os.Geteuid() == 12345 {
		t.Fatal("test is vacuous: the writer already owned the target uid")
	}
}

// #169: the .bak sidecar takes the config directory's owner, same as the file it
// backs up. A root-owned backup beside a service-owned config is the same outage
// one restore away.
//
// The stale config.yaml here is deliberately root-owned: after 8030ab6 the
// directory decides, so an artifact already mis-owned by the bug must not dictate
// the owner of its replacement or its backup.
func TestWriteFileAtomicWithBackup_SidecarTakesDirectoryOwner(t *testing.T) {
	requireRootForOwnership(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("service:\n  name: first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, 12345, 12345); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}
	if err := os.Chown(path, 0, 0); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}

	// Second write produces the .bak from the existing file.
	if err := writeFileAtomicWithBackup(path, []byte("service:\n  name: second\n"), 0600); err != nil {
		t.Fatalf("writeFileAtomicWithBackup: %v", err)
	}

	if uid, _ := ownerOfPath(t, path+".bak"); uid != 12345 {
		t.Fatalf(".bak owner = %d, want the directory's 12345", uid)
	}
	if uid, _ := ownerOfPath(t, path); uid != 12345 {
		t.Fatalf("rewritten config owner = %d, want the directory's 12345 — "+
			"a stale root-owned artifact must not dictate its replacement", uid)
	}
}

// #169: the unprivileged path is unchanged — a developer editing their own
// config must see exactly the previous behaviour, with no new failure mode.
func TestWriteFileAtomicWithBackup_UnprivilegedUnchanged(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("covered by the root-gated siblings")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeFileAtomicWithBackup(path, []byte("a: 1\n"), 0600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileAtomicWithBackup(path, []byte("a: 2\n"), 0600); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "a: 2\n" {
		t.Fatalf("content = %q err = %v", got, err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != "a: 1\n" {
		t.Fatalf("backup = %q err = %v", bak, err)
	}
}

// #172: a config directory that cannot be read must be reported as such. The old
// code asserted "unlocked changes detected" for this and four other causes,
// pointing the operator at `config lock` — the wrong subsystem, and under sudo a
// remediation that recreates the ownership defect (#167's loop, one frame up).
func TestVerifyReloadIntegrity_ReportsTheActualCause(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "config.yaml")

	err := verifyReloadIntegrity(missing, false, nil)
	if err == nil {
		t.Fatal("expected an error for an unreadable config directory")
	}
	if strings.Contains(err.Error(), "unlocked changes detected") {
		t.Fatalf("cause erased — still reporting the catch-all: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot read config directory") {
		t.Fatalf("expected the real cause, got: %v", err)
	}
}

// #173: a *missing* manifest stays downgrade-safe (nil), because that genuinely
// means "never locked" and predates plugin attestation.
func TestVerifyPluginFingerprints_MissingManifestStillPasses(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	if err := os.Remove(filepath.Join(tmp, ".checksums")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err != nil {
		t.Fatalf("missing manifest must remain downgrade-safe, got: %v", err)
	}
}

// #173: an unreadable or corrupt manifest must NOT pass. Returning nil here
// skipped plugin attestation entirely and admitted the boot — fail-open on a
// security gate.
func TestVerifyPluginFingerprints_CorruptManifestFailsClosed(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	if err := os.WriteFile(filepath.Join(tmp, ".checksums"), []byte("{{{ not yaml"), 0600); err != nil {
		t.Fatal(err)
	}

	err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil)
	if err == nil {
		t.Fatal("corrupt manifest must fail closed, not skip attestation")
	}
	if !strings.Contains(err.Error(), "attestation") {
		t.Fatalf("expected an attestation error, got: %v", err)
	}
}

// #173: an unsupported manifest version is the same class — enabled but
// uncheckable, so it must not silently skip the gate.
func TestVerifyPluginFingerprints_UnsupportedVersionFailsClosed(t *testing.T) {
	tmp := buildFingerprintFixture(t, true)
	lockConfigAndPlugins(t, tmp, "gmail")
	if err := os.WriteFile(filepath.Join(tmp, ".checksums"), []byte("version: 1\nhashes: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := verifyPluginFingerprintsForConfig(filepath.Join(tmp, "config.yaml"), nil); err == nil {
		t.Fatal("unsupported manifest version must fail closed")
	}
}
