//go:build linux

package fsown

import "golang.org/x/sys/unix"

// HasACL reports whether path or any directory above it carries a POSIX access
// ACL, which makes the mode-bit model non-authoritative for that path.
//
// This exists because `setfacl -m u:ductile:r` on a 0640 root:root file is the
// standard answer to exactly the problem ductile has — a privileged writer and a
// limited reader — and on such a host the mode bits say "unreadable" while the
// open succeeds. Without this probe, `config check` reports a hard error on an
// install that boots perfectly, and docs/runbooks/privsep-thinkpad-enforce.md
// treats a non-clean check as a stop. Breaking a working deployment to warn about
// a problem it does not have is a worse outcome than the one being fixed.
//
// A present ACL does not mean access is granted — only that this code cannot tell
// from the mode bits, which is reported as inconclusive rather than guessed at.
// The boot path does not need any of this: it opens the file.
func HasACL(path string) bool {
	if hasACLEntry(path) {
		return true
	}
	for _, dir := range ancestors(path) {
		if hasACLEntry(dir) {
			return true
		}
	}
	return false
}

func hasACLEntry(path string) bool {
	// A zero-length read returns the value size, which is enough to know whether
	// the attribute is set without allocating for its contents.
	size, err := unix.Getxattr(path, "system.posix_acl_access", nil)
	return err == nil && size > 0
}
