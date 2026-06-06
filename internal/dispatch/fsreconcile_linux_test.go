//go:build linux

package dispatch

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// TestReconcileWorkerFilesystemAsRoot exercises the full #87 boot reconciliation
// on a privileged host: the secrets surface is tightened, and each worker gets a
// private 0700 dir it OWNS. The distinct ownership + 0700 is the cross-worker
// isolation mechanism (a worker uid gets EACCES on a sibling's dir). Requires root
// (chown); skips elsewhere — a skip is not a pass.
func TestReconcileWorkerFilesystemAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("worker-dir chown needs root; skipping on non-root dev host (NOT a pass)")
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
		Workers: map[string]config.WorkerConf{
			"default":   {UID: 65534, GID: 65534, StateDir: filepath.Join(base, "default")},
			"untrusted": {UID: 65533, GID: 65533, StateDir: filepath.Join(base, "untrusted")},
		},
	}

	if err := reconcileWorkerFilesystem(cfg, []string{secret}, 0); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Secret tightened in place.
	if si, _ := os.Stat(secret); si.Mode().Perm() != 0o600 {
		t.Fatalf("secret not tightened: %#o", si.Mode().Perm())
	}

	// Each worker dir exists, is 0700, and is owned by its own uid/gid — so no other
	// worker uid can traverse or read it (the cross-worker wall).
	for name, w := range cfg.Workers {
		di, err := os.Stat(w.StateDir)
		if err != nil {
			t.Fatalf("worker %q dir not created: %v", name, err)
		}
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("worker %q dir mode = %#o, want 0700", name, di.Mode().Perm())
		}
		st, ok := di.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != w.UID || int(st.Gid) != w.GID {
			t.Fatalf("worker %q dir owner = %d:%d, want %d:%d", name, st.Uid, st.Gid, w.UID, w.GID)
		}
	}
}
