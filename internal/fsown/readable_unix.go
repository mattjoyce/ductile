//go:build darwin || linux || freebsd || openbsd || netbsd

package fsown

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
)

// Account is the identity a readability question is asked on behalf of (#179).
//
// The distinction that matters: this is NOT necessarily the process asking. Every
// defect in the #167 family is a file that some OTHER account has to open, and the
// whole point of the check is that `ductile config check` usually runs under sudo,
// where the asking process is root and root can open anything. Answering "can I
// open this?" would reproduce the original bug one layer up — a clean verdict on a
// box that will not boot. So the question is always "could THIS account open it",
// evaluated against the permission model rather than by trying.
type Account struct {
	UID int
	GID int
	// Groups holds supplementary gids. A file group-readable via a secondary
	// group is genuinely readable, and omitting them would produce false alarms
	// on exactly the installs that did the group setup properly.
	Groups []int
}

// String renders the account for a diagnostic message.
func (a Account) String() string { return Label(a.UID, a.GID) }

func (a Account) inGroup(gid int) bool {
	if a.GID == gid {
		return true
	}
	return slices.Contains(a.Groups, gid)
}

// CurrentAccount is the identity this process is running as, supplementary
// groups included.
func CurrentAccount() Account {
	acct := Account{UID: os.Geteuid(), GID: os.Getegid()}
	if groups, err := os.Getgroups(); err == nil {
		acct.Groups = groups
	}
	return acct
}

// AccountOwning resolves the account that owns path — the way the service account
// is identified without requiring it to be named in config.
//
// This is the same rule Desired uses on the write side: the directory states the
// intent. /etc/ductile is owned by the service user on a privsep install, so its
// owner IS the account that has to read everything inside it. Using one rule for
// both sides means the check and the writers cannot disagree about who the files
// are for.
//
// Supplementary groups are filled in best-effort from the passwd/group database.
// A static binary on a host with no entry for the service account simply gets the
// primary gid, which is a smaller group set and therefore only ever produces a
// conservative answer — never a falsely clean one.
func AccountOwning(path string) (Account, bool) {
	owner, ok := Of(path)
	if !ok {
		return Account{}, false
	}
	acct := Account{UID: owner.UID, GID: owner.GID}
	if u, err := user.LookupId(strconv.Itoa(owner.UID)); err == nil {
		if gids, err := u.GroupIds(); err == nil {
			for _, g := range gids {
				if n, err := strconv.Atoi(g); err == nil {
					acct.Groups = append(acct.Groups, n)
				}
			}
		}
	}
	return acct, true
}

// Diagnose reports whether acct could open path for reading, and when it could
// not, the sentence an operator needs: which path blocks it, who owns that path,
// what its mode is, and which account was asking.
//
// A path that does not exist reports readable with no detail. Absence is a
// different question with a different answer — reporting "unreadable" for a file
// that was never there is precisely the ENOENT/EACCES confusion #167 was about,
// pointed the other way.
//
// The test for absence is os.IsNotExist SPECIFICALLY, not "stat failed". A stat
// through a directory that denies search fails with EACCES, and treating that as
// absence would swallow the single most useful case this function has — the first
// version of this code did exactly that, and TestDiagnose_UnreadableParentBlocks…
// is what caught it. That is #167's own confusion reappearing inside its fix.
func Diagnose(path string, acct Account) (bool, string) {
	if _, err := os.Stat(path); err != nil && os.IsNotExist(err) {
		return true, ""
	}

	// Traversal first. A file can be mode 0644 and still unopenable because a
	// directory above it denies search — and the error the caller eventually sees
	// names the FILE, which sends the operator to look at the wrong thing.
	for _, dir := range ancestors(path) {
		if ok, detail := permitted(dir, acct, 0o1); !ok {
			return false, fmt.Sprintf("%s cannot search %s%s", acct, dir, detail)
		}
	}
	if ok, detail := permitted(path, acct, 0o4); !ok {
		return false, fmt.Sprintf("%s cannot read %s%s", acct, path, detail)
	}
	return true, ""
}

// ancestors lists the directories that must be searchable to reach path,
// shallowest first — so the reported blocker is the outermost one, which is the
// one worth fixing.
func ancestors(path string) []string {
	var dirs []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	// Reverse into root-first order.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// permitted evaluates one path against the Unix permission model for acct,
// where want is the bit being asked for (4 read, 1 search).
//
// The subtlety worth stating: Unix stops at the FIRST matching class. An account
// that owns a mode-0044 file cannot read it, even though the group and other bits
// say read — the owner class matched, and it denies. A naive "any class allows"
// check would call that readable and miss a real outage.
func permitted(path string, acct Account, want os.FileMode) (bool, string) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, fmt.Sprintf(" (%v)", err)
	}
	// Root bypasses discretionary access control for both read and directory
	// search, so a root-owned gateway genuinely can open anything and reporting
	// otherwise would be a false alarm on every non-privsep install.
	if acct.UID == 0 {
		return true, ""
	}
	owner, ok := ownerOf(fi)
	if !ok {
		return true, ""
	}
	perm := fi.Mode().Perm()
	var class os.FileMode
	switch {
	case acct.UID == owner.UID:
		class = (perm >> 6) & 0o7
	case acct.inGroup(owner.GID):
		class = (perm >> 3) & 0o7
	default:
		class = perm & 0o7
	}
	if class&want != 0 {
		return true, ""
	}
	return false, fmt.Sprintf(" (owned by %s, mode %04o)", Label(owner.UID, owner.GID), perm)
}
