//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/protocol"
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

// TestHasDropCapabilityAsRoot verifies the boot-gate capability probe on a
// privileged host: root must report the uid-drop capability. Asserts only as root
// (the Dell test host / a privileged container); on a non-root dev box the probe's
// true-case can't be forced, so it skips rather than pass vacuously.
func TestHasDropCapabilityAsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("capability probe true-case asserts only as root; skipping on non-root dev host")
	}
	if !hasDropCapability() {
		t.Fatal("root must report the uid-drop capability (boot gate would wrongly refuse)")
	}
}

// TestPrivsepConfinedSpawnFailsClosedWithoutPrivilege verifies the fail-closed
// half of the tracer (#92) on an UNPRIVILEGED host: a plugin granted a worker the
// gateway lacks the privilege to drop to must fail the spawn, never silently run
// at the gateway uid. The kernel rejects the setuid in the fork-child window, so
// cmd.Start() errors — exactly the "no false wall" guarantee. This runs where the
// privileged Linux wall test skips, so the two are complementary.
func TestPrivsepConfinedSpawnFailsClosedWithoutPrivilege(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this test asserts the unprivileged failure mode; as root the drop would actually succeed")
	}

	scriptPath := writeDispatchTestScript(t, "#!/bin/sh\necho '{\"protocol\":2}'\n")
	cfg := config.Defaults()
	cfg.Workers = map[string]config.WorkerConf{"untrusted": {UID: 65534, GID: 65534}}
	cfg.Plugins = map[string]config.PluginConf{"sys_exec": {Worker: "untrusted"}}

	d := &Dispatcher{events: events.NewHub(16), cfg: cfg, enforcePrivsep: true}
	req := &protocol.Request{Protocol: 2, JobID: "job-failclosed", Command: "poll"}
	_, _, _, _, _, _, err := d.spawnPlugin(context.Background(), "sys_exec", scriptPath, req, time.Second, slog.Default())
	if err == nil {
		t.Fatal("a confined spawn the gateway cannot perform must fail closed, not run at gateway privilege")
	}
	// And it must be the *typed* drop failure (so the dispatcher fails it terminal,
	// never retried), not a generic spawn error.
	if !errors.Is(err, ErrWorkerDropFailed) {
		t.Fatalf("expected ErrWorkerDropFailed, got %v", err)
	}
}
