//go:build linux

package dispatch

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestPrivsepDropUnderCapabilityOnly proves the headline #88 claim that the full-
// root container could not: a NON-root gateway holding only CAP_SETUID+CAP_SETGID
// (the ADR §5 "two caps, nothing more") can still drop a child to a account uid.
// It also exercises the CapEff detection path (hasDropCapability must see the caps
// despite euid != 0).
//
// Run it via systemd-run with AmbientCapabilities, e.g.:
//
//	systemd-run --uid=<nonroot> --pipe --wait \
//	  -p AmbientCapabilities="CAP_SETUID CAP_SETGID" \
//	  -p CapabilityBoundingSet="CAP_SETUID CAP_SETGID" \
//	  <dispatch.test> -test.run TestPrivsepDropUnderCapabilityOnly -test.v
//
// As root it skips (that's the container case, covered elsewhere); with no caps it
// skips too — a skip is never a pass.
func TestPrivsepDropUnderCapabilityOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the drop as NON-root with caps; run via systemd-run with AmbientCapabilities")
	}
	if !hasDropCapability() {
		t.Skip("no CAP_SETUID/SETGID — run via systemd-run -p AmbientCapabilities=...; skipping (NOT a pass)")
	}

	const workerUID, workerGID = 65534, 65534

	// /tmp is world-writable (1777), so the dropped child (65534) can create its
	// output file and exec the script — no chown needed (which is the point: a
	// cap-only gateway has no CAP_CHOWN).
	outPath := fmt.Sprintf("/tmp/privsep-capdrop-%d.out", os.Getpid())
	scriptPath := fmt.Sprintf("/tmp/privsep-capdrop-%d.sh", os.Getpid())
	_ = os.Remove(outPath)
	script := fmt.Sprintf("#!/bin/sh\nid -u > %q\n", outPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(scriptPath) })

	cmd := exec.Command(scriptPath)
	configurePluginProcess(cmd)
	if err := applyAccountCredential(cmd, ResolvedAccount{Name: "untrusted", UID: workerUID, GID: workerGID, Mode: ModeConfined}); err != nil {
		t.Fatalf("apply credential: %v", err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("dropped spawn failed under cap-only (the two caps were not enough?): %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read drop output: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "65534" {
		t.Fatalf("child ran as uid %q, want 65534 — the drop did not take under cap-only", got)
	}
}
