//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"os/exec"
	"testing"
)

// TestApplyWorkerCredential covers the pure credential builder in isolation: it
// only constructs SysProcAttr (no exec), so it runs on any Unix dev host. The
// actual uid drop + EACCES wall is the privileged integration test (#92), which
// needs a CAP_SETUID Linux host and skips elsewhere.
func TestApplyWorkerCredential(t *testing.T) {
	t.Run("unconfined is a no-op", func(t *testing.T) {
		cmd := exec.Command("true")
		configurePluginProcess(cmd)
		if err := applyWorkerCredential(cmd, ResolvedWorker{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cmd.SysProcAttr.Credential != nil {
			t.Fatal("unconfined must not set a credential (runs at gateway uid)")
		}
	})

	t.Run("confined sets uid/gid and resets supplementary groups", func(t *testing.T) {
		cmd := exec.Command("true")
		configurePluginProcess(cmd)
		w := ResolvedWorker{Name: "untrusted", UID: 1002, GID: 1002, Confined: true}
		if err := applyWorkerCredential(cmd, w); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		cred := cmd.SysProcAttr.Credential
		if cred == nil {
			t.Fatal("confined must set a credential")
		}
		if cred.Uid != 1002 || cred.Gid != 1002 {
			t.Fatalf("uid/gid = %d/%d, want 1002/1002", cred.Uid, cred.Gid)
		}
		// Groups reset to just the worker's own gid — no inherited gateway group can
		// silently re-grant access (ADR §8 botched-drop guard).
		if len(cred.Groups) != 1 || cred.Groups[0] != 1002 {
			t.Fatalf("supplementary groups = %v, want [1002]", cred.Groups)
		}
	})

	t.Run("privilege does not clobber the Setpgid lifecycle (A1 separation)", func(t *testing.T) {
		cmd := exec.Command("true")
		configurePluginProcess(cmd)
		if err := applyWorkerCredential(cmd, ResolvedWorker{UID: 1, GID: 1, Confined: true}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cmd.SysProcAttr.Setpgid {
			t.Fatal("applying the worker credential must preserve Setpgid (lifecycle and privilege are separate concerns)")
		}
	})
}
