//go:build darwin || linux || freebsd || openbsd || netbsd

package vault

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func vaultFileOwner(t *testing.T, path string) (int, int) {
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

// #170: the vault blob must land owned by the service account. `sudo ductile
// vault set ...` on a privsep install previously produced a root-owned vault.age
// that the daemon could not read, and every secret_ref resolution failed far from
// the cause.
func TestWriteFileAtomic_VaultBlobInheritsDirectoryOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change file ownership")
	}
	dir := t.TempDir()
	if err := os.Chown(dir, 12345, 12345); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}
	path := filepath.Join(dir, "vault.age")

	if err := writeFileAtomic(path, []byte("age-encrypted-blob")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	uid, gid := vaultFileOwner(t, path)
	if uid != 12345 || gid != 12345 {
		t.Fatalf("vault owner = %d:%d, want the directory's 12345:12345", uid, gid)
	}
	if os.Geteuid() == 12345 {
		t.Fatal("test is vacuous: the writer already owned the target uid")
	}
}

// #170: rotate-key renames a staged file over the age key's inode. The replacement
// must land owned by the key directory's account, which on a privsep install is
// the service user that has to decrypt with it — otherwise rotation is the step
// that hands secret-zero to the wrong account.
//
// Note the stale key here is deliberately root-owned: after 8030ab6 the directory
// decides, so a previously-mis-owned key must NOT dictate the owner of its
// replacement. An earlier version of this test asserted the opposite (the old
// file-wins rule) and, being root-gated, skipped locally while failing in CI's
// privileged gate — the exact blind spot #175 exists to close.
func TestWriteFileAtomic_RotatedKeyTakesDirectoryOwnerNotStaleKey(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to change file ownership")
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "age.key")
	if err := os.WriteFile(keyPath, []byte("# existing key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, 12345, 12345); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}
	if err := os.Chown(keyPath, 0, 0); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}

	if err := writeFileAtomic(keyPath, []byte("# rotated key\n")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	uid, gid := vaultFileOwner(t, keyPath)
	if uid != 12345 || gid != 12345 {
		t.Fatalf("rotated key owner = %d:%d, want the directory's 12345:12345 — "+
			"a stale root-owned key must not dictate its replacement", uid, gid)
	}
}

// #170: mode stays 0600 through the ownership work — the age key must never
// widen, whatever it looked like before.
func TestWriteFileAtomic_KeepsKeyMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.age")
	if err := os.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("mode = %04o, want 0600", perm)
	}
}
