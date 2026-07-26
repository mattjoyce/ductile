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
