package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteFileAtomicWritesAndSyncs proves the atomic write still produces the
// right content + perms after #38 added the post-rename directory fsync (if the
// fsync errored on this FS, this happy path would fail).
func TestWriteFileAtomicWritesAndSyncs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "blob")
	if err := writeFileAtomic(p, []byte("hello")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "hello" {
		t.Fatalf("content = %q, err = %v; want hello", b, err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", fi.Mode().Perm())
	}
}

// TestFsyncDir: a real directory syncs cleanly; a missing one errors.
func TestFsyncDir(t *testing.T) {
	if err := fsyncDir(t.TempDir()); err != nil {
		t.Errorf("fsyncDir(real dir) = %v, want nil", err)
	}
	if err := fsyncDir(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("fsyncDir(missing dir) should error")
	}
}
