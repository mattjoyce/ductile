//go:build !(darwin || linux || freebsd || openbsd || netbsd)

package fsown

import "os"

// Account exists on this platform only so the readability preflight (#179) can be
// written once, without build tags, in the packages that call it.
type Account struct {
	UID    int
	GID    int
	Groups []int
}

func (a Account) String() string { return "this process" }

// CurrentAccount has no uid/gid to report without a Unix ownership model.
func CurrentAccount() Account { return Account{} }

// AccountOwning cannot resolve a service account here; callers treat false as
// "no separate account exists to check against".
func AccountOwning(path string) (Account, bool) { return Account{}, false }

// Diagnose always reports readable. The #167 family is a privsep failure — a
// privileged CLI writing files a separate service account must read — and that
// separation does not exist on a platform with no Unix ownership model, so there
// is nothing here to get wrong.
func Diagnose(path string, acct Account) (bool, string) { return true, "" }

// Openable reports whether this process can open path. Unlike Diagnose there IS
// a meaningful answer here, because opening a file is not a Unix-specific idea.
func Openable(path string) (bool, string) {
	f, err := os.Open(path) // #nosec G304 -- paths come from the gateway's own config
	if err != nil {
		if os.IsNotExist(err) {
			return true, ""
		}
		return false, "cannot open " + path + ": " + err.Error()
	}
	_ = f.Close()
	return true, ""
}
