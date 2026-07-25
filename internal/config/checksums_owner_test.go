//go:build darwin || linux || freebsd || openbsd || netbsd

package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// checksumsOwner reads a path's uid/gid straight from the syscall, so the
// assertions below compare writeChecksumsAtomic's result against the kernel.
func checksumsOwner(t *testing.T, path string) (int, int) {
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

func requireRootForChecksums(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to change file ownership")
	}
}

// #167 — the load-bearing proof. Writing into a directory owned by another
// account must produce a manifest owned by that account, not by the writer. This
// is the exact `sudo config lock` shape: root writes, the service user must read.
func TestWriteChecksumsAtomic_InheritsDirectoryOwner(t *testing.T) {
	requireRootForChecksums(t)
	dir := t.TempDir()
	if err := os.Chown(dir, 12345, 12345); err != nil {
		t.Skipf("host refuses chown (user namespace or ownership-less fs): %v", err)
	}
	path := filepath.Join(dir, ".checksums")

	if err := writeChecksumsAtomic(path, ChecksumManifest{Version: 2, Hashes: map[string]string{}}); err != nil {
		t.Fatalf("writeChecksumsAtomic: %v", err)
	}

	uid, gid := checksumsOwner(t, path)
	if uid != 12345 || gid != 12345 {
		t.Fatalf("manifest owner = %d:%d, want the directory's 12345:12345 "+
			"(this is #167: a root-owned manifest the service account cannot read)", uid, gid)
	}
	if os.Geteuid() == 12345 {
		t.Fatal("test is vacuous: the writer already owned the target uid")
	}
}

// #167: ownership is inherited, mode is not. A pre-existing world-readable
// manifest must be tightened back to 0600 rather than have its mode preserved.
func TestWriteChecksumsAtomic_DoesNotWidenMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".checksums")
	if err := os.WriteFile(path, []byte("version: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeChecksumsAtomic(path, ChecksumManifest{Version: 2, Hashes: map[string]string{}}); err != nil {
		t.Fatalf("writeChecksumsAtomic: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("mode = %04o, want 0600", perm)
	}
}

// #167: a failed correction must abort the write, not publish a manifest the
// service account cannot read. The temp file is discarded and the existing
// manifest is left exactly as it was.
//
// Root can chown to anything on an ordinary filesystem, so the refusal has to be
// staged by the harness. Point DUCTILE_TEST_UNCHOWNABLE_DIR at a writable
// directory that already contains a .checksums owned by an account this process
// cannot chown to — a user namespace (`unshare -U -r`, where unmapped uids appear
// as 65534 and chown to them returns EINVAL) is the cheapest way to arrange that,
// and it works unprivileged in CI. Without the fixture the test says so rather
// than passing vacuously.
func TestWriteChecksumsAtomic_FailedOwnershipLeavesManifestIntact(t *testing.T) {
	requireRootForChecksums(t)
	dir := os.Getenv("DUCTILE_TEST_UNCHOWNABLE_DIR")
	if dir == "" {
		t.Skip("set DUCTILE_TEST_UNCHOWNABLE_DIR to a pre-staged unchownable fixture dir")
	}

	path := filepath.Join(dir, ".checksums")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture %s is not readable: %v", path, err)
	}
	uid, _ := checksumsOwner(t, path)
	if uid == os.Geteuid() {
		t.Skipf("fixture owner %d matches euid; no mismatch to refuse", uid)
	}

	err = writeChecksumsAtomic(path, ChecksumManifest{Version: 2, Hashes: map[string]string{"a": "b"}})
	if err == nil {
		t.Fatal("expected the write to abort on a refused chown")
	}
	if !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected an ownership error, got: %v", err)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("original manifest disturbed: %v", readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("original manifest was modified despite the failed write:\n%s", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".checksums.tmp-") {
			t.Fatalf("temp file %s left behind after a failed write", e.Name())
		}
	}
}
