//go:build darwin || linux || freebsd || openbsd || netbsd

package fsown

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// statOwner reads a path's uid/gid straight from the syscall, so the assertions
// below compare the helper against the kernel rather than against itself.
func statOwner(t *testing.T, path string) (int, int) {
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

// requireRoot gates the tests that need to hand a file to another account. They
// skip rather than fail off-root so the suite stays green on a developer laptop;
// CI's privileged step is what actually runs them (#175).
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to change file ownership")
	}
}

// #167: with no file yet, the intended owner comes from the containing directory
// — on a privsep install that is the service account that has to read it back.
func TestDesired_FallsBackToDirectory(t *testing.T) {
	dir := t.TempDir()
	wantUID, wantGID := statOwner(t, dir)

	got, ok := Desired(filepath.Join(dir, ".checksums"))
	if !ok {
		t.Fatal("expected an ownership opinion for an existing directory")
	}
	if got.UID != wantUID || got.GID != wantGID {
		t.Fatalf("owner = %d:%d, want directory owner %d:%d", got.UID, got.GID, wantUID, wantGID)
	}
}

// #167: an existing file states the answer — an operator who already chowned it
// must not have that undone by re-deriving from the directory.
func TestDesired_ExistingFileWinsOverDirectory(t *testing.T) {
	requireRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".checksums")
	if err := os.WriteFile(path, []byte("version: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 12345, 12345); err != nil {
		t.Skipf("host refuses chown (user namespace or ownership-less fs): %v", err)
	}
	dirUID, _ := statOwner(t, dir)
	if dirUID == 12345 {
		t.Skip("fixture uid collides with the directory owner")
	}

	got, ok := Desired(path)
	if !ok {
		t.Fatal("expected an ownership opinion for an existing file")
	}
	if got.UID != 12345 || got.GID != 12345 {
		t.Fatalf("owner = %d:%d, want the existing file's 12345:12345", got.UID, got.GID)
	}
}

// #167 (found by postmortem of the fix itself): an existing-but-unreadable file
// must produce no opinion rather than falling back to the directory — silently
// re-deriving would overrule an operator who had already chowned it.
func TestDesired_UnreadableExistingFileYieldsNoOpinion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".checksums")
	if err := os.WriteFile(path, []byte("version: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Remove traverse permission so stat on the file fails while the file exists.
	if err := os.Chmod(dir, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })

	if _, ok := Desired(path); ok {
		t.Fatal("expected no ownership opinion for an unreadable existing file")
	}
}

// #167: the common developer case — writing into a directory you already own —
// must be untouched by the ownership work.
func TestApplyToTemp_NoOpWhenOwnerAlreadyMatches(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, ".checksums.tmp-noop")
	if err := os.WriteFile(tmp, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ApplyToTemp(tmp, filepath.Join(dir, ".checksums")); err != nil {
		t.Fatalf("expected no-op, got: %v", err)
	}
	uid, gid := statOwner(t, tmp)
	if uid != os.Geteuid() {
		t.Fatalf("temp owner changed to %d:%d, expected it left alone", uid, gid)
	}
}

// #167: proves the correction actually fires and actually lands, without needing
// root. A uid change needs privilege; a gid change to a group the caller already
// belongs to does not, and it exercises the identical os.Chown path. The temp
// file is deliberately given a gid the target directory does not have, which is
// the mismatch `sudo config lock` produces on a privsep host — so this asserts
// the mechanism rather than trusting the API surface.
func TestApplyToTemp_CorrectsMismatchedOwnership(t *testing.T) {
	dir := t.TempDir()
	_, dirGID := statOwner(t, dir)

	otherGID := -1
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatalf("getgroups: %v", err)
	}
	for _, g := range groups {
		if g != dirGID {
			otherGID = g
			break
		}
	}
	if otherGID == -1 {
		t.Skip("caller belongs to only one group; no second gid to mismatch against")
	}

	tmp := filepath.Join(dir, ".checksums.tmp-mismatch")
	if err := os.WriteFile(tmp, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(tmp, -1, otherGID); err != nil {
		t.Skipf("cannot set up the gid mismatch: %v", err)
	}
	if _, gid := statOwner(t, tmp); gid != otherGID {
		t.Fatalf("fixture did not take: gid = %d, want %d", gid, otherGID)
	}

	if err := ApplyToTemp(tmp, filepath.Join(dir, ".checksums")); err != nil {
		t.Fatalf("ApplyToTemp: %v", err)
	}

	if _, gid := statOwner(t, tmp); gid != dirGID {
		t.Fatalf("ownership not corrected: gid = %d, want the directory's %d", gid, dirGID)
	}
}

// #167: unprivileged writes must not start failing. Docker bind mounts, NFS
// root_squash and userns-mapped containers all refuse chown legitimately, and
// ductile ships a Docker deployment — aborting there would break working installs
// to fix a privsep bug they do not have. The write proceeds; the read side
// reports the ownership accurately if it turns out to matter.
func TestApplyToTemp_UnprivilegedChownFailureIsNotFatal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root is expected to fail hard; see the requireRoot sibling")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".checksums")
	if err := os.WriteFile(path, []byte("version: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// An owner we have no authority to assign, reached via the existing-file rule.
	if err := os.Chown(path, 12345, 12345); err == nil {
		t.Skip("caller can chown freely; cannot stage the refusal")
	}

	tmp := filepath.Join(dir, ".checksums.tmp-unpriv")
	if err := os.WriteFile(tmp, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyToTemp(tmp, path); err != nil {
		t.Fatalf("unprivileged chown refusal must not abort the write, got: %v", err)
	}
}

// #167: no ownership opinion (unstattable directory) means leave ownership alone
// rather than guess — the write must still succeed.
func TestApplyToTemp_NoOpinionIsNotAnError(t *testing.T) {
	if err := ApplyToTemp(filepath.Join(t.TempDir(), "x"), "/nonexistent-dir-167/.checksums"); err != nil {
		t.Fatalf("expected nil for an unresolvable target, got: %v", err)
	}
}

// Hint is the operator-facing diagnosis. It must stay silent when ownership is
// not the story, so a parse or version error is not drowned in irrelevant uids.
func TestHint_SilentWhenOwnershipIsNotTheStory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".checksums")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := Hint(path); got != "" {
		t.Fatalf("expected no hint for a self-owned file, got: %q", got)
	}
	if got := Hint(filepath.Join(dir, "does-not-exist")); got != "" {
		t.Fatalf("expected no hint for a missing file, got: %q", got)
	}
}

// #167: when ownership IS the story the hint must name both sides — that is the
// whole diagnostic value over a bare "permission denied".
func TestHint_NamesBothAccounts(t *testing.T) {
	requireRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".checksums")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 12345, 12345); err != nil {
		t.Skipf("host refuses chown: %v", err)
	}

	got := Hint(path)
	if got == "" {
		t.Fatal("expected a hint when the owner differs from the caller")
	}
	for _, want := range []string{"12345", "owned by", "runs as"} {
		if !contains(got, want) {
			t.Fatalf("hint %q missing %q", got, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
