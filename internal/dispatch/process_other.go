//go:build !(darwin || linux || freebsd || openbsd || netbsd)

package dispatch

import (
	"errors"
	"os"
	"os/exec"
)

func configurePluginProcess(cmd *exec.Cmd) {}

// errWorkerDropUnsupported fails a confined spawn closed on a platform that
// cannot drop privilege (no Unix credential model). The full boot gate (#86)
// refuses such a host at startup; the tracer's minimal guarantee is that a
// confined plugin never silently runs at gateway privilege here.
var errWorkerDropUnsupported = errors.New("privsep: uid drop unsupported on this platform")

// applyWorkerCredential mirrors the unix builder: an unconfined resolution is a
// no-op; a confined one fails closed because this platform has no uid-drop.
func applyWorkerCredential(cmd *exec.Cmd, w ResolvedWorker) error {
	if w.Confined {
		return errWorkerDropUnsupported
	}
	return nil
}

func terminatePluginProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err == nil || errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func killPluginProcess(cmd *exec.Cmd) error {
	return terminatePluginProcess(cmd)
}
