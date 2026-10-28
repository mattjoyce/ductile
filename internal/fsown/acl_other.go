//go:build !linux

package fsown

// HasACL reports false everywhere but Linux.
//
// The reason is deliberate rather than an omission. ductile's privsep
// deployments are Linux (deploy/systemd, the Docker image, the posture fixture),
// and POSIX access ACLs are reachable there through a documented extended
// attribute. macOS and the BSDs expose ACLs through interfaces that need cgo or
// per-platform syscalls, and no ductile deployment target uses them — paying that
// cost to answer a question nobody is asking would be the wrong trade. Reporting
// false means the mode-bit model is treated as authoritative here, which is
// correct for every install that exists on these platforms.
func HasACL(path string) bool { return false }
