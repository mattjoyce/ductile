package vault

import (
	"os"
	"testing"
)

// preconditions_test.go makes the fast suite's host-environment assumptions
// explicit instead of folklore (card #120). A skip is never a pass: each guard
// names the assumption it is protecting so a skip in CI output is self-explaining.

// requireWritablePermsEnforced guards tests that inject a persist failure by
// making a directory unwritable (chmod 0500) and then assert the write is
// rejected. Root bypasses filesystem permission bits, so under euid 0 the
// injected failure never fires and the test would falsely report "expected a
// save failure". The privsep threat model these tests probe only holds for a
// non-root account, so skipping under root is the correct, honest outcome.
func requireWritablePermsEnforced(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("requires non-root: root bypasses chmod-0500 persist-failure injection (assumes filesystem permission enforcement)")
	}
}
