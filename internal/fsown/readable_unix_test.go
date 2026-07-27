//go:build darwin || linux || freebsd || openbsd || netbsd

package fsown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The permission classes, one case each. These are pure evaluations against a
// synthetic account, which is the whole point of the design: the answer must not
// depend on who is running the test, so these run identically on a laptop and as
// root in CI.
func TestPermitted_Classes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	owner, ok := Of(path)
	if !ok {
		t.Skip("platform FileInfo carries no Unix owner")
	}

	cases := []struct {
		name string
		acct Account
		want bool
	}{
		{"owner reads via owner class", Account{UID: owner.UID, GID: owner.GID}, true},
		{"primary group reads via group class", Account{UID: owner.UID + 1000, GID: owner.GID}, true},
		{"supplementary group reads via group class", Account{UID: owner.UID + 1000, GID: owner.GID + 1000, Groups: []int{owner.GID}}, true},
		{"other is denied by mode 0640", Account{UID: owner.UID + 1000, GID: owner.GID + 1000}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := permitted(path, tc.acct, 0o4)
			if got != tc.want {
				t.Fatalf("permitted() = %v, want %v (detail: %q)", got, tc.want, detail)
			}
		})
	}
}

// Unix stops at the FIRST matching class. An owner of a mode-0044 file cannot
// read it, even though group and other both say read. A naive "any class allows"
// implementation passes every other test in this file and gets this one wrong,
// which is precisely the kind of quiet wrongness a readability check must not
// have.
func TestPermitted_OwnerClassDeniesEvenWhenGroupAllows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o044); err != nil {
		t.Fatal(err)
	}
	owner, ok := Of(path)
	if !ok {
		t.Skip("platform FileInfo carries no Unix owner")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode bits; this case needs an unprivileged euid")
	}

	// Premise check: the running process really cannot read its own file. If this
	// fails the test below proves nothing.
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Fatalf("fixture is vacuous: opened a mode-0044 file owned by this uid")
	}

	if got, _ := permitted(path, Account{UID: owner.UID, GID: owner.GID}, 0o4); got {
		t.Fatal("owner class denies read on mode 0044, but permitted() said yes")
	}
}

// Root bypasses discretionary access control. Reporting otherwise would raise a
// false alarm on every non-privsep install, where the gateway is root.
func TestPermitted_RootBypassesModeBits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if got, detail := permitted(path, Account{UID: 0, GID: 0}, 0o4); !got {
		t.Fatalf("root must read a mode-0000 file; got denied: %s", detail)
	}
}

// A file can be perfectly readable and still unopenable because a directory above
// it denies search — and the error the caller eventually sees names the FILE,
// sending the operator to look at the wrong thing.
func TestDiagnose_UnreadableParentBlocksAReadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root traverses regardless; this case needs an unprivileged euid")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	// Premise check: the file really is unopenable through the locked directory.
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Fatal("fixture is vacuous: the file opened through a mode-0000 directory")
	}

	ok, detail := Diagnose(path, CurrentAccount())
	if ok {
		t.Fatal("Diagnose() said readable for a file behind a mode-0000 directory")
	}
	if !strings.Contains(detail, dir) {
		t.Fatalf("the blocker should name the directory %s, got: %s", dir, detail)
	}
	if !strings.Contains(detail, "cannot search") {
		t.Fatalf("expected a traversal diagnosis, got: %s", detail)
	}
}

// Absence is a different question with a different answer. Reporting "unreadable"
// for a file that was never there is the ENOENT/EACCES confusion of #167 pointed
// the other way.
func TestDiagnose_MissingFileIsNotAReadabilityFailure(t *testing.T) {
	ok, detail := Diagnose(filepath.Join(t.TempDir(), "never-created"), CurrentAccount())
	if !ok {
		t.Fatalf("a missing file must not be reported unreadable; got: %s", detail)
	}
}

// The message is the deliverable: an operator has to learn who owns the file and
// who was asking, without going and running stat themselves.
func TestDiagnose_NamesTheOwnerAndTheAsker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads regardless; this case needs an unprivileged euid")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}

	ok, detail := Diagnose(path, CurrentAccount())
	if ok {
		t.Fatal("Diagnose() said readable for a mode-0000 file")
	}
	for _, want := range []string{path, "owned by", "mode 0000", CurrentAccount().String()} {
		if !strings.Contains(detail, want) {
			t.Fatalf("diagnosis is missing %q; got: %s", want, detail)
		}
	}
}

// AccountOwning is how the service account is identified without being named in
// config — the same "the directory decides" rule Desired uses on the write side.
func TestAccountOwning_ResolvesFromThePathItself(t *testing.T) {
	dir := t.TempDir()
	acct, ok := AccountOwning(dir)
	if !ok {
		t.Skip("platform FileInfo carries no Unix owner")
	}
	if acct.UID != os.Geteuid() {
		t.Fatalf("a temp dir this process created should resolve to uid %d, got %d", os.Geteuid(), acct.UID)
	}
}

func TestAncestors_AreRootFirstAndReachTheRoot(t *testing.T) {
	got := ancestors("/a/b/c/file")
	want := []string{"/", "/a", "/a/b", "/a/b/c"}
	if len(got) != len(want) {
		t.Fatalf("ancestors() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ancestors() = %v, want %v", got, want)
		}
	}
}
