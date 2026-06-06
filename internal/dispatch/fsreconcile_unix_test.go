//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReconcileSecretPath covers the secrets-surface tightening on files the
// current user owns — runnable on a non-root dev host. The foreign-owned refuse
// path and worker-dir chown need root and are covered on the Linux test host.
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
