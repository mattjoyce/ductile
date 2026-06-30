//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// newOSLookup returns the live host-probe implementation used at boot.
func newOSLookup() osLookup { return realOSLookup{} }

type realOSLookup struct{}

// sudoProbeTimeout bounds the boot-time `sudo -l` exec: a PAM stack backed by a
// network module (LDAP/SSSD) can block even with -n, and a hung probe must never
// hang the gateway boot. A timeout maps to an inconclusive result (fail-closed for
// a confined account under strict mode).
const sudoProbeTimeout = 4 * time.Second

// standardSecurePath is the conventional sudo secure_path. The writable-dir and
// writable-setuid probes scan only these directories — a privileged exec hijack
// surface — rather than the whole filesystem, keeping the audit bounded and fast.
// Non-standard secure_path entries are out of scope (a documented best-effort bound).
var standardSecurePath = []string{
	"/usr/local/sbin",
	"/usr/local/bin",
	"/usr/sbin",
	"/usr/bin",
	"/sbin",
	"/bin",
}

func (realOSLookup) UsernameForUID(uid int) (string, bool) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return "", false
	}
	return u.Username, true
}

func (realOSLookup) GroupNamesForUID(uid int) ([]string, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil, err
	}
	gids, err := u.GroupIds()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(gids))
	for _, gid := range gids {
		if g, err := user.LookupGroupId(gid); err == nil {
			names = append(names, g.Name)
		}
	}
	return names, nil
}

// SudoNoPasswd shells out to `sudo -n -l -U <user>` and looks for a NOPASSWD entry.
// sudo-not-installed is a clean negative. The exec is bounded by sudoProbeTimeout and
// pinned to LC_ALL=C so the error-prose match is deterministic. A non-zero exit is
// interpreted: an explicit "not allowed" listing is a clean negative; a timeout or
// any other failure is reported inconclusive so the audit can react (fail-closed
// under strict for a confined account).
func (realOSLookup) SudoNoPasswd(username string) (bool, error) {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return false, nil // no sudo binary -> no sudo side-door
	}
	ctx, cancel := context.WithTimeout(context.Background(), sudoProbeTimeout)
	defer cancel()

	// #nosec G204 -- username is retrieved directly from system uid via user.LookupId and bounded to standard exec flags.
	cmd := exec.CommandContext(ctx, sudo, "-n", "-l", "-U", username)
	cmd.Env = append(os.Environ(), "LC_ALL=C") // deterministic English error prose
	out, runErr := cmd.CombinedOutput()
	text := string(out)

	if strings.Contains(text, "NOPASSWD") {
		return true, nil
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false, fmt.Errorf("sudo -l for %q timed out after %s", username, sudoProbeTimeout)
	}
	if runErr == nil {
		return false, nil // listed successfully, no NOPASSWD entries
	}
	// Non-zero exit: distinguish a clean "no sudo for this user" from an
	// inconclusive query (insufficient privilege to read sudoers, etc.).
	if strings.Contains(text, "not allowed to run sudo") ||
		strings.Contains(text, "is not allowed") ||
		strings.Contains(text, "Sorry, user") {
		return false, nil
	}
	return false, fmt.Errorf("sudo -l for %q inconclusive: %v", username, runErr)
}

// WritablePathDirs returns secure_path directories the account can write. A directory
// that exists but cannot be inspected is reported as an error (inconclusive), not as
// "not a surface"; absent directories are simply not a surface.
func (realOSLookup) WritablePathDirs(uid int) ([]string, error) {
	gids := accountGIDs(uid)
	var out []string
	var scanErrs []string
	for _, dir := range standardSecurePath {
		fi, err := os.Stat(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				scanErrs = append(scanErrs, dir+": "+err.Error())
			}
			continue
		}
		if fi.IsDir() && pathWritableBy(fi, uid, gids) {
			out = append(out, dir)
		}
	}
	if len(scanErrs) > 0 {
		return out, fmt.Errorf("secure_path scan incomplete: %s", strings.Join(scanErrs, "; "))
	}
	return out, nil
}

// WritableSetuidRoot returns setuid-root binaries the account can overwrite — either
// the file itself is writable, OR its parent secure_path directory is writable (a
// writable directory lets the account rename/replace any file in it, defeating the
// file's own mode bits). A directory that exists but cannot be read is reported as an
// error (inconclusive).
func (realOSLookup) WritableSetuidRoot(uid int) ([]string, error) {
	gids := accountGIDs(uid)
	var out []string
	var scanErrs []string
	for _, dir := range standardSecurePath {
		dfi, derr := os.Stat(dir)
		if derr != nil {
			if !os.IsNotExist(derr) {
				scanErrs = append(scanErrs, dir+": "+derr.Error())
			}
			continue
		}
		dirWritable := dfi.IsDir() && pathWritableBy(dfi, uid, gids)

		entries, err := os.ReadDir(dir)
		if err != nil {
			scanErrs = append(scanErrs, dir+": "+err.Error())
			continue
		}
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil || fi.Mode()&os.ModeSetuid == 0 {
				continue
			}
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != 0 {
				continue // setuid-ROOT only
			}
			if dirWritable || pathWritableBy(fi, uid, gids) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(scanErrs) > 0 {
		return out, fmt.Errorf("setuid-root scan incomplete: %s", strings.Join(scanErrs, "; "))
	}
	return out, nil
}

// accountGIDs returns the set of gids an account holds (primary + supplementary).
// Best-effort: an unresolvable account yields an empty set.
func accountGIDs(uid int) map[int]bool {
	set := map[int]bool{}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return set
	}
	if gid, err := strconv.Atoi(u.Gid); err == nil {
		set[gid] = true
	}
	gids, err := u.GroupIds()
	if err != nil {
		return set
	}
	for _, g := range gids {
		if gid, err := strconv.Atoi(g); err == nil {
			set[gid] = true
		}
	}
	return set
}

// pathWritableBy reports whether the account (uid + its gids) can write the file:
// world-writable, owner-writable when owned by the uid, or group-writable when the
// account holds the file's gid.
func pathWritableBy(fi os.FileInfo, uid int, gids map[int]bool) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	mode := fi.Mode().Perm()
	switch {
	case mode&0o002 != 0:
		return true // world-writable
	case int(st.Uid) == uid && mode&0o200 != 0:
		return true // owner-writable by this account
	case gids[int(st.Gid)] && mode&0o020 != 0:
		return true // group-writable and the account holds the gid
	default:
		return false
	}
}
