//go:build linux

package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/protocol"
)

// TestPrivsepWorkerCannotReadAgeKey is the tracer's headline wall test (#92): a
// plugin dropped to the untrusted worker uid must get EACCES on the gateway-owned
// 0600 age key. It REQUIRES a privileged (root / CAP_SETUID) Linux host and skips
// cleanly elsewhere — a skip is explicitly NOT a pass (#90), so a missing-privilege
// CI box can never mask a breached wall.
//
// VALIDATION STATUS: authored on a macOS dev host where it can only skip; the
// privileged path (perms, dir traversal, the drop itself) is validated on the Dell
// Linux test target and may need iteration on first real run. Until then, treat a
// green here as "skipped", not "wall proven".
func TestPrivsepWorkerCannotReadAgeKey(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("privsep wall test needs root/CAP_SETUID — skipping (NOT a pass); run on the Linux test host")
	}

	// nobody:nogroup on most distros — an unprivileged uid guaranteed not to own
	// the root-created key file below.
	const workerUID, workerGID = 65534, 65534

	tmp := t.TempDir()
	// The worker must be able to traverse tmp to reach both the key (to be denied
	// by the file mode, not the dir) and its own output dir.
	if err := os.Chmod(tmp, 0o755); err != nil {
		t.Fatalf("chmod tmp: %v", err)
	}

	// Gateway-owned (root) 0600 age key — the secret the dropped worker must not read.
	keyPath := filepath.Join(tmp, "age.key")
	if err := os.WriteFile(keyPath, []byte("AGE-SECRET-KEY-TEST\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	// Worker-owned 0700 output dir the dropped plugin CAN write its probe result to
	// (proves the drop took effect and lets us read the verdict back as root).
	outDir := filepath.Join(tmp, "worker-state")
	if err := os.Mkdir(outDir, 0o700); err != nil {
		t.Fatalf("mkdir outDir: %v", err)
	}
	if err := os.Chown(outDir, workerUID, workerGID); err != nil {
		t.Fatalf("chown outDir: %v", err)
	}
	resultPath := filepath.Join(outDir, "probe")

	// Plugin code is not secret (ADR §3 Layer 1b) — the script is world-readable and
	// executable so the dropped worker can exec it. It probes the key and records the
	// verdict, then idles until the timeout terminates it.
	scriptPath := filepath.Join(tmp, "sys_exec.sh")
	script := fmt.Sprintf(`#!/bin/sh
if cat %q >/dev/null 2>&1; then echo READABLE > %q; else echo DENIED > %q; fi
while :; do sleep 1; done
`, keyPath, resultPath, resultPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := config.Defaults()
	cfg.Workers = map[string]config.WorkerConf{
		"untrusted": {UID: workerUID, GID: workerGID, StateDir: outDir},
	}
	cfg.Plugins = map[string]config.PluginConf{
		"sys_exec": {Worker: "untrusted"},
	}

	d := &Dispatcher{events: events.NewHub(16), cfg: cfg}
	req := &protocol.Request{Protocol: 2, JobID: "job-wall", Command: "poll"}
	// The script idles after probing, so the spawn will hit the timeout; we only
	// care about the verdict file it wrote as the dropped worker.
	_, _, _, _, _, _, _ = d.spawnPlugin(context.Background(), "sys_exec", scriptPath, req, 1500*time.Millisecond, slog.Default())

	var got string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(resultPath); err == nil && len(b) > 0 {
			got = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	switch got {
	case "DENIED":
		// wall holds
	case "READABLE":
		t.Fatal("WALL BREACHED: worker read the 0600 age key (the drop did not bite)")
	default:
		t.Fatalf("no worker probe verdict (got %q) — the plugin may not have spawned as the worker; iterate perms/setup on the Linux host", got)
	}
}
