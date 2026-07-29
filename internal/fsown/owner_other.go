//go:build !(darwin || linux || freebsd || openbsd || netbsd)

package fsown

import "os"

// ApplyToTemp is a no-op on platforms with no Unix ownership model.
//
// The signature mirrors the unix build-tag sibling for substitutability; there is
// no uid/gid to inherit here, so the atomic write proceeds unchanged and #167's
// privsep failure mode — which only exists where a service runs under a separate
// account — cannot arise.
func ApplyToTemp(tmpPath, finalPath string) error {
	return nil
}

// Hint has nothing to report without a Unix ownership model; manifest
// failures on this platform are described by the underlying error alone.
func Hint(path string) string {
	return ""
}

// Apply reports that ownership could not be reconciled, because on a platform
// with no Unix ownership model there is nothing to reconcile it to.
//
// The mode half is still enforced: `perm` is the ceiling the caller is asking
// for, and tightening it does not depend on uid/gid existing. secureSQLiteFiles
// (#171) calls this to keep the state DB and its WAL/SHM sidecars off group and
// other, which matters wherever the FileMode bits are honoured at all.
//
// A false return means "ownership does not match the directory", which here is
// permanently true — callers treat it as best-effort and never as fatal, exactly
// as they do for a chown refusal on Unix.
func Apply(path string, perm os.FileMode) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if fi.Mode().Perm()&^perm != 0 {
		_ = os.Chmod(path, fi.Mode().Perm()&perm)
	}
	return false
}
