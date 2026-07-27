package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The crash-safety property #177 is actually about: at no instant does path hold
// a partially written file. The observable proxy is that the bytes arrive at the
// target by rename, never by truncate-then-fill — so a directory scan mid-write
// finds a temp file, and path itself still holds the OLD complete content.
func TestWriteFileAtomic_TargetIsNeverPartiallyWritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte("service:\n  name: before\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	// A payload large enough that a truncate-then-write implementation would be
	// observably empty for a while.
	replacement := []byte("service:\n  name: after\n" + strings.Repeat("# padding\n", 20000))
	if err := WriteFileAtomic(path, replacement, 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(replacement) {
		t.Fatal("target does not hold the replacement content")
	}

	// No temp file survives a successful publish.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}

// A failed write must not damage what is already there. This is the property that
// makes the atomic version strictly safer than os.WriteFile rather than merely
// different: os.WriteFile truncates first, so its failure mode is a destroyed
// config.
func TestWriteFileAtomic_FailureLeavesTheOriginalIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can create a temp file in an unwritable dir; needs an unprivileged euid")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte("service:\n  name: before\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// Deny creation of the temp file by making the directory unwritable.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Premise check: the write really must fail, or this test proves nothing.
	if err := WriteFileAtomic(path, []byte("service:\n  name: after\n"), 0o600); err == nil {
		t.Fatal("fixture is vacuous: the write succeeded in an unwritable directory")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("the original was damaged by a failed write: %q", got)
	}
}

// Mode survives the replace. The config file is 0600 on a real install and a
// publish that widened it to the umask default would be a quiet permission
// regression on the file that holds the most.
func TestWriteFileAtomic_PreservesTheRequestedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := WriteFileAtomic(path, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", fi.Mode().Perm())
	}
}

// #177's other half: SetPath's persist must go through the atomic writer. The
// observable consequence is that a `config set` on a target that does NOT already
// exist still lands with the right mode — the case where os.WriteFile's
// inode-reuse safety net does not apply.
func TestWriteFileAtomic_CreatesANewTargetWithTheRequestedMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.yaml")
	if err := WriteFileAtomic(path, []byte("routes: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the new file was not created: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("a newly created target got mode %04o, want 0600", fi.Mode().Perm())
	}
}
