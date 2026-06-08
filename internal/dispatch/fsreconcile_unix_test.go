//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// TestVerifyCredentialedHome covers the verify-don't-mutate boot check for the
// trusted tier (grill: Armstrong) — runnable non-root by owning the dir as the test
// user and asserting against the current uid.
func TestVerifyCredentialedHome(t *testing.T) {
	uid := os.Getuid()
	home := t.TempDir() // owned by the test user, a real dir

	t.Run("valid home (exists, dir, owned by uid) passes", func(t *testing.T) {
		if err := verifyCredentialedHome("trusted", config.AccountConf{UID: uid, GID: os.Getgid(), Home: home}); err != nil {
			t.Fatalf("valid home rejected: %v", err)
		}
	})
	t.Run("missing home fails closed", func(t *testing.T) {
		if err := verifyCredentialedHome("trusted", config.AccountConf{UID: uid, Home: filepath.Join(home, "nope")}); err == nil {
			t.Fatal("missing home must fail closed")
		}
	})
	t.Run("symlink home fails closed (swappable target)", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(home, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		if err := verifyCredentialedHome("trusted", config.AccountConf{UID: uid, Home: link}); err == nil {
			t.Fatal("symlink home must fail closed")
		}
	})
	t.Run("file (not dir) fails closed", func(t *testing.T) {
		f := filepath.Join(home, "afile")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyCredentialedHome("trusted", config.AccountConf{UID: uid, Home: f}); err == nil {
			t.Fatal("non-dir home must fail closed")
		}
	})
	t.Run("home owned by a different uid fails closed", func(t *testing.T) {
		if err := verifyCredentialedHome("trusted", config.AccountConf{UID: uid + 1, Home: home}); err == nil {
			t.Fatal("wrong-owner home must fail closed")
		}
	})
}

// TestReconcileSecretPath covers the secrets-surface tightening on files the
// current user owns — runnable on a non-root dev host. The foreign-owned refuse
// path and account-dir chown need root and are covered on the Linux test host.
func TestReconcileSecretPath(t *testing.T) {
	euid := os.Geteuid()

	t.Run("a gateway-owned loose file is tightened to 0600", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := reconcileSecretPath(p, euid); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("expected 0600 after reconcile, got %#o", info.Mode().Perm())
		}
	})

	t.Run("an already-0600 file passes unchanged", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "state.db")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := reconcileSecretPath(p, euid); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("a loose owned directory is tightened to 0700", func(t *testing.T) {
		d := filepath.Join(t.TempDir(), "configdir")
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := reconcileSecretPath(d, euid); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		info, _ := os.Stat(d)
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("expected dir 0700 after reconcile, got %#o", info.Mode().Perm())
		}
	})

	t.Run("a non-existent path is skipped", func(t *testing.T) {
		if err := reconcileSecretPath(filepath.Join(t.TempDir(), "nope"), euid); err != nil {
			t.Fatalf("absent path should be skipped, got %v", err)
		}
		if err := reconcileSecretPath("", euid); err != nil {
			t.Fatalf("empty path should be skipped, got %v", err)
		}
	})

	t.Run("a foreign-owned path fails closed", func(t *testing.T) {
		// Simulate a foreign owner by passing a euid that is not the file's owner.
		p := filepath.Join(t.TempDir(), "foreign.key")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := reconcileSecretPath(p, euid+1); err == nil {
			t.Fatal("expected fail-closed when the gateway does not own the secret")
		}
	})
}

// TestReconcileAccountFilesystemAsRoot exercises the full #87 boot reconciliation
// on a privileged host: the secrets surface is tightened, and each account gets a
// private 0700 dir it OWNS. The distinct ownership + 0700 is the cross-account
// isolation mechanism (a account uid gets EACCES on a sibling's dir). Requires root
// (chown); skips elsewhere — a skip is not a pass.
//
// Portable across Unix (was Linux-only): reconcileAccountFilesystem + syscall.Stat_t
// Uid/Gid are available on Darwin, so the root chown/own checks run on macOS too —
// the #95 launchd story is exactly this drop-as-root path.
func TestReconcileAccountFilesystemAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("account-dir chown needs root; skipping on non-root dev host (NOT a pass)")
	}

	base, err := os.MkdirTemp("/tmp", "privsep-fs-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	// A gateway-owned (root) but loose secret — must be tightened to 0600.
	secret := filepath.Join(base, "state.db")
	if err := os.WriteFile(secret, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Accounts: map[string]config.AccountConf{
			"default":   {UID: 65534, GID: 65534, StateDir: filepath.Join(base, "default")},
			"untrusted": {UID: 65533, GID: 65533, StateDir: filepath.Join(base, "untrusted")},
		},
	}

	if err := reconcileAccountFilesystem(cfg, []string{secret}, 0); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Secret tightened in place.
	if si, _ := os.Stat(secret); si.Mode().Perm() != 0o600 {
		t.Fatalf("secret not tightened: %#o", si.Mode().Perm())
	}

	// Each account dir exists, is 0700, and is owned by its own uid/gid — so no other
	// account uid can traverse or read it (the cross-account wall).
	for name, w := range cfg.Accounts {
		di, err := os.Stat(w.StateDir)
		if err != nil {
			t.Fatalf("account %q dir not created: %v", name, err)
		}
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("account %q dir mode = %#o, want 0700", name, di.Mode().Perm())
		}
		st, ok := di.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != w.UID || int(st.Gid) != w.GID {
			t.Fatalf("account %q dir owner = %d:%d, want %d:%d", name, st.Uid, st.Gid, w.UID, w.GID)
		}
	}
}
