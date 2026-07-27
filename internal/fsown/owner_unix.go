//go:build darwin || linux || freebsd || openbsd || netbsd

package fsown

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// Owner is a resolved Unix ownership pair.
type Owner struct {
	UID int
	GID int
}

// Desired resolves who a service-read artifact *should* belong to (#167, #169,
// #170, #171).
//
// The containing directory decides, always. On a privsep install /etc/ductile
// and the state dir are owned by the service user — precisely the account that
// has to read the artifact back — so the directory is the authoritative statement
// of intent.
//
// An earlier version preferred an existing file's owner, on the theory that an
// operator who had already chowned it had stated the answer. The posture harness
// refuted that: on an install already broken by #167 the existing manifest is
// root-owned *because of the bug*, so deferring to it meant `ductile config lock`
// — the operator's natural remediation — could not repair the install. It also
// let anyone able to place a file at the path choose the uid of the next write.
// One rule fixed both: never take ownership intent from the artifact being
// replaced.
//
// A false second return means "no opinion" and callers must leave ownership
// alone: the directory could not be stat'ed, or this platform's FileInfo does
// not carry a Unix owner.
func Desired(path string) (Owner, bool) {
	return Of(filepath.Dir(path))
}

// Of resolves the owner of path itself, where Desired resolves the owner of its
// containing directory. Both return false for "no opinion".
func Of(path string) (Owner, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return Owner{}, false
	}
	return ownerOf(fi)
}

// ownerOf extracts the uid/gid from a FileInfo, or reports false when the
// platform's Sys() is not a Unix stat struct.
func ownerOf(fi os.FileInfo) (Owner, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Owner{}, false
	}
	return Owner{UID: int(st.Uid), GID: int(st.Gid)}, true
}

// Hint annotates a failure with the ownership facts an operator needs, turning
// "permission denied" into a diagnosis (#167): who owns the file versus who this
// process is running as. It returns "" when ownership
// is not the story — the file cannot be stat'ed, or the owner already matches
// the caller, in which case a parse or version error stands on its own.
func Hint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	owner, ok := ownerOf(fi)
	if !ok || owner.UID == os.Geteuid() {
		return ""
	}
	return fmt.Sprintf(" (file is owned by %s, this process runs as %s)",
		Label(owner.UID, owner.GID), Label(os.Geteuid(), os.Getegid()))
}

// Label renders a uid/gid as "name:group" when both resolve, falling back
// to "uid:gid" numerics — a static binary on a host with no passwd entry for the
// service account must still produce a usable message.
func Label(uid, gid int) string {
	name := strconv.Itoa(uid)
	if u, err := user.LookupId(name); err == nil && u.Username != "" {
		name = u.Username
	}
	group := strconv.Itoa(gid)
	if g, err := user.LookupGroupId(group); err == nil && g.Name != "" {
		group = g.Name
	}
	return name + ":" + group
}

// ApplyToTemp chowns a pending temp file to the owner its final path should
// have, before the rename publishes it. Doing it pre-rename keeps the atomic
// contract: the artifact is never observable with the wrong owner.
//
// This is the one place ownership is negotiated. Every tmp+chmod+rename helper in
// the tree should route through it rather than growing its own copy — four such
// helpers existed independently before #167, and only one of them was correct.
//
// The comparison is against the temp file's *actual* owner rather than the
// process euid/egid — a setgid directory (and BSD gid inheritance generally)
// gives new files the directory's gid, so euid/egid would report a mismatch that
// isn't one and burn a syscall on every write.
//
// Failure is fatal only when we had the authority to succeed. As root, a refused
// chown is anomalous and publishing anyway recreates #167 — the CLI reports
// success and the daemon discovers the unreadable artifact at the next boot,
// which is the worst possible time to find out. Unprivileged, a refused chown is
// ordinary: NFS root_squash, userns-mapped containers, Docker bind mounts, FAT.
// ductile's own Docker deployment lives in that set, so aborting there would
// break working installs to fix a privsep bug they do not have. Those writes
// proceed exactly as they did before this fix, and the read side — which no
// longer mistakes EACCES for a missing manifest — reports it accurately if the
// ownership turns out to matter.
func ApplyToTemp(tmpPath, finalPath string) error {
	want, ok := Desired(finalPath)
	if !ok {
		return nil
	}

	fi, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to stat pending %s: %w", filepath.Base(finalPath), err)
	}
	have, ok := ownerOf(fi)
	if !ok || have == want {
		return nil
	}

	if err := os.Chown(tmpPath, want.UID, want.GID); err != nil {
		if os.Geteuid() != 0 {
			return nil
		}
		return fmt.Errorf(
			"failed to set %s ownership to %s (it would be written as %s): %w; "+
				"the service account could not read the result, so nothing was written",
			filepath.Base(finalPath), Label(want.UID, want.GID), Label(have.UID, have.GID), err)
	}
	return nil
}

// Apply best-effort corrects an existing file's ownership to what its directory
// says it should be, and tightens its mode to at most perm.
//
// Unlike ApplyToTemp this never fails the caller (#171). The state DB is opened
// by roughly a dozen CLI entry points as well as the daemon, so refusing to open
// it on a chown refusal would be the riskiest possible change for existing
// installs — NFS, userns and Docker bind mounts all refuse legitimately, and the
// pre-existing behaviour there was simply "no ownership negotiation at all".
// Where we do have the authority, the artifact lands correct; where we do not,
// nothing is worse than before.
//
// Returns true when ownership now matches the directory, so callers that want to
// warn can.
func Apply(path string, perm os.FileMode) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if fi.Mode().Perm()&^perm != 0 {
		_ = os.Chmod(path, fi.Mode().Perm()&perm)
	}
	want, ok := Desired(path)
	if !ok {
		return false
	}
	have, ok := ownerOf(fi)
	if !ok {
		return false
	}
	if have == want {
		return true
	}
	return os.Chown(path, want.UID, want.GID) == nil
}
