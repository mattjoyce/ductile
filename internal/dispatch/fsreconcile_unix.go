//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"github.com/mattjoyce/ductile/internal/config"
)

// reconcileWorkerFilesystem enforces the privsep filesystem floor at boot (#87),
// called ONLY when the boot gate decided to enforce. It is all-or-refuse: any
// failure returns an error that must abort startup (Armstrong B3 — never run
// half-confined). Two parts:
//
//  1. The secrets surface must be gateway-owned and unreadable by any worker uid.
//     A file the gateway owns is tightened in place (0644 → 0600 / dir → 0700);
//     a foreign-owned or still-loose path fails closed. The age key is already
//     enforced fail-closed at load (keyring rejects mode&0077 != 0), so it need
//     not be re-resolved here.
//  2. Each configured worker gets a private 0700 state_dir it owns — created and
//     chowned here, never widened.
func reconcileWorkerFilesystem(cfg *config.Config, secretPaths []string, euid int) error {
	for _, p := range secretPaths {
		if err := reconcileSecretPath(p, euid); err != nil {
			return err
		}
	}
	for name, w := range cfg.Workers {
		if err := reconcileWorkerDir(name, w); err != nil {
			return err
		}
	}
	return nil
}

// reconcileSecretPath ensures one secrets-surface path is gateway-owned and not
// readable by group/other. A gateway-owned but loose path is tightened in place;
// anything else fails closed. A non-existent path is skipped (e.g. no vault yet).
func reconcileSecretPath(path string, euid int) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("privsep: stat secrets surface %q: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("privsep: cannot determine owner of %q", path)
	}
	if int(st.Uid) != euid {
		return fmt.Errorf("privsep: %q is owned by uid %d, not the gateway (uid %d) — the secrets surface must be gateway-owned", path, st.Uid, euid)
	}

	// Owned by the gateway: tighten in place if loose. Dirs need owner-execute to
	// traverse, so their floor is 0700; files are 0600. Tightening never widens.
	if info.Mode().Perm()&0o077 != 0 {
		target := os.FileMode(0o600)
		if info.IsDir() {
			target = 0o700
		}
		if err := os.Chmod(path, target); err != nil {
			return fmt.Errorf("privsep: tighten %q to %#o: %w", path, target, err)
		}
		if info, err = os.Stat(path); err != nil {
			return fmt.Errorf("privsep: re-stat %q after chmod: %w", path, err)
		}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("privsep: %q has insecure permissions %#o — a worker uid could read it", path, info.Mode().Perm())
	}
	return nil
}

// reconcileWorkerDir ensures one worker has a private 0700 state_dir it owns.
//
// It is deliberately provision-best-effort, verify-fail-closed. A privileged
// gateway (root / dev) self-heals: create, chown to the worker, chmod 0700. But a
// cap-only gateway (CAP_SETUID+CAP_SETGID, the ADR §5 "two caps, nothing more")
// holds NEITHER CAP_CHOWN nor ownership of a worker-owned dir, so its chown/chmod
// EPERM — and that is fine *iff* the init layer already provisioned the dir
// (sysusers.d + tmpfiles.d, #88). The final stat is the real wall: 0700, owned by
// the worker, or fail closed. So the same code is correct under both root and the
// minimal two-cap deploy.
func reconcileWorkerDir(name string, w config.WorkerConf) error {
	if _, err := os.Stat(w.StateDir); errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(w.StateDir, 0o700); mkErr != nil {
			return fmt.Errorf("privsep: worker %q state_dir %q is missing and cannot be created (provision it via tmpfiles.d, #88): %w", name, w.StateDir, mkErr)
		}
	}
	// Best-effort ownership/mode — succeeds as root/dev, EPERMs (ignored) under a
	// cap-only gateway where the init layer is expected to have provisioned the dir.
	_ = os.Chown(w.StateDir, w.UID, w.GID)
	_ = os.Chmod(w.StateDir, 0o700)

	// Verify — fail-closed regardless of who provisioned the dir.
	info, err := os.Stat(w.StateDir)
	if err != nil {
		return fmt.Errorf("privsep: stat worker %q state_dir %q: %w", name, w.StateDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("privsep: worker %q state_dir %q exists but is not a directory", name, w.StateDir)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("privsep: worker %q state_dir %q has mode %#o, want 0700", name, w.StateDir, info.Mode().Perm())
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != w.UID || int(st.Gid) != w.GID {
		return fmt.Errorf("privsep: worker %q state_dir %q owned by %d:%d, want %d:%d (provision it owned by the worker via sysusers.d/tmpfiles.d, #88)", name, w.StateDir, st.Uid, st.Gid, w.UID, w.GID)
	}
	return nil
}
