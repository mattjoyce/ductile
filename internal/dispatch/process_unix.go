//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configurePluginProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// applyWorkerCredential composes the privsep uid/gid drop onto an already-
// configured command (PrivSec ADR §3 Layer 1b; tracer #92). It is deliberately
// separate from configurePluginProcess: that sets process-group lifecycle (*how*
// the child is terminated); this sets privilege identity (*who* it runs as). The
// kernel applies the credential in the fork-child window before execve as
// setgroups → setgid → setuid, so the parent must hold CAP_SETUID/SETGID; a
// failure surfaces from cmd.Start() and must fail the spawn closed.
//
// Unconfined resolutions are a no-op (run at the gateway uid, as today).
// Supplementary groups are reset to the worker's own gid so an inherited gateway
// group cannot silently re-grant access (the ADR §8 botched-drop guard).
func applyWorkerCredential(cmd *exec.Cmd, w ResolvedWorker) error {
	if !w.Confined {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    uint32(w.UID),
		Gid:    uint32(w.GID),
		Groups: []uint32{uint32(w.GID)},
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
