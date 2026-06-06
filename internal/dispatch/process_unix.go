//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// hasDropCapability reports whether this process can drop a child to another
// uid/gid — root holds it implicitly, and a non-root process can hold it via
// CAP_SETUID+CAP_SETGID (e.g. systemd AmbientCapabilities, the #88 deploy). It is
// the privsep boot gate's capability probe (ADR §5); evaluated once at startup.
func hasDropCapability() bool {
	if os.Geteuid() == 0 {
		return true
	}
	return hasLinuxSetuidCaps()
}

// hasLinuxSetuidCaps inspects the effective capability set for CAP_SETUID (7) and
// CAP_SETGID (6) via /proc/self/status. On non-Linux Unix the file is absent, so
// it returns false — there, only root (handled above) can drop.
func hasLinuxSetuidCaps() bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		hex, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
		if err != nil {
			return false
		}
		const capSetgid, capSetuid = 6, 7
		return mask&(1<<capSetgid) != 0 && mask&(1<<capSetuid) != 0
	}
	return false
}

func configurePluginProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// applyAccountCredential composes the privsep uid/gid drop onto an already-
// configured command (PrivSec ADR §3 Layer 1b; tracer #92). It is deliberately
// separate from configurePluginProcess: that sets process-group lifecycle (*how*
// the child is terminated); this sets privilege identity (*who* it runs as). The
// kernel applies the credential in the fork-child window before execve as
// setgroups → setgid → setuid, so the parent must hold CAP_SETUID/SETGID; a
// failure surfaces from cmd.Start() and must fail the spawn closed.
//
// Unconfined resolutions are a no-op (run at the gateway uid, as today).
// Supplementary groups are reset to the account's own gid so an inherited gateway
// group cannot silently re-grant access (the ADR §8 botched-drop guard).
func applyAccountCredential(cmd *exec.Cmd, w ResolvedAccount) error {
	if !w.Confined {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// uid/gid are validated positive and within range at config load (validateAccounts,
	// #84), so these conversions cannot overflow or go negative.
	uid := uint32(w.UID) // #nosec G115 -- account uid validated positive + bounded at config load
	gid := uint32(w.GID) // #nosec G115 -- account gid validated positive + bounded at config load
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    uid,
		Gid:    gid,
		Groups: []uint32{gid},
	}
	return nil
}

func terminatePluginProcess(cmd *exec.Cmd) error {
	return signalPluginProcessGroup(cmd, syscall.SIGTERM)
}

func killPluginProcess(cmd *exec.Cmd) error {
	return signalPluginProcessGroup(cmd, syscall.SIGKILL)
}

func signalPluginProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// configurePluginProcess starts the plugin with Setpgid=true, which makes
	// the child's process group ID equal to its PID. Use that stable value
	// directly: after SIGTERM the parent process may already be gone, while
	// children in the group still need SIGKILL during the grace-period fallback.
	err := syscall.Kill(-cmd.Process.Pid, signal)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	err = cmd.Process.Signal(signal)
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
