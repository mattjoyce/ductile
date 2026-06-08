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

// TestPrivsepNegativeSuite is the #90 aggregate: a single dropped account probing
// the whole secrets surface and a sibling account's dir, proving uid separation
// actually bites end to end (ADR §9). It REQUIRES root (the setup chowns dirs to
// account uids) and skips cleanly otherwise — a skip is never a pass, so a
// non-privileged CI runner cannot mask a breached wall.
//
// NOT covered here (stated, not hidden): sibling isolation *within* the shared
// `default` tier — that is the accepted same-uid residual (see 83-privsep-epic).
// This is why the cross-account probe uses two DIFFERENT accounts, never default/default.
func TestPrivsepNegativeSuite(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("privsep negative suite needs root (chown account dirs) — skipping (NOT a pass); run on the Linux test host / CI sudo step")
	}

	const (
		uidA, gidA = 65534, 65534 // the plugin's own account
		uidB, gidB = 65533, 65533 // a sibling account it must not reach
	)

	base, err := os.MkdirTemp("/tmp", "privsep-neg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil { // traversable to reach targets
		t.Fatal(err)
	}

	// Gateway-owned (root) 0600 secrets the dropped account must not read.
	mustWrite := func(name string, mode os.FileMode) string {
		p := filepath.Join(base, name)
		if err := os.WriteFile(p, []byte("secret\n"), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	keyPath := mustWrite("age.key", 0o600)
	cfgPath := mustWrite("config.yaml", 0o600)
	dbPath := mustWrite("ductile.db", 0o600)

	// Worker A's own dir (writable verdict sink + positive control).
	dirA := filepath.Join(base, "workerA")
	if err := os.Mkdir(dirA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dirA, uidA, gidA); err != nil {
		t.Fatal(err)
	}
	// Sibling account B's dir with a secret inside — A must not reach it.
	dirB := filepath.Join(base, "workerB")
	if err := os.Mkdir(dirB, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "secret"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(filepath.Join(dirB, "secret"), uidB, gidB); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dirB, uidB, gidB); err != nil {
		t.Fatal(err)
	}

	resultPath := filepath.Join(dirA, "verdicts")
	scriptPath := filepath.Join(base, "probe.sh")
	// The probe records "<label>=READABLE|DENIED" for each target into account A's
	// own dir (which it owns), then idles until the timeout terminates it.
	script := fmt.Sprintf(`#!/bin/sh
probe() { if cat "$2" >/dev/null 2>&1; then echo "$1=READABLE"; else echo "$1=DENIED"; fi; }
{
  probe key %q
  probe config %q
  statedb=%q; probe statedb "$statedb"
  probe sibling %q
  if echo own > %q.own 2>/dev/null; then echo "own=WRITABLE"; else echo "own=DENIED"; fi
} > %q
while :; do sleep 1; done
`, keyPath, cfgPath, dbPath, filepath.Join(dirB, "secret"), resultPath, resultPath)
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Accounts: map[string]config.AccountConf{
			"wa": {UID: uidA, GID: gidA, StateDir: dirA},
			"wb": {UID: uidB, GID: gidB, StateDir: dirB},
		},
		Plugins: map[string]config.PluginConf{"probe": {RunAs: "wa"}},
	}

	d := &Dispatcher{events: events.NewHub(16), cfg: cfg, enforcePrivsep: true}
	req := &protocol.Request{Protocol: 2, JobID: "job-neg", Command: "poll"}
	_, _, _, _, _, _, _ = d.spawnPlugin(context.Background(), "probe", scriptPath, req, 1500*time.Millisecond, slog.Default())

	var verdicts string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(resultPath); err == nil && len(b) > 0 {
			verdicts = string(b)
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if verdicts == "" {
		t.Fatal("no probe verdicts — the plugin may not have spawned as account A")
	}
	t.Logf("verdicts:\n%s", verdicts)

	// Every secret + the sibling dir must be DENIED; the account's own dir writable.
	for _, want := range []string{"key=DENIED", "config=DENIED", "statedb=DENIED", "sibling=DENIED", "own=WRITABLE"} {
		if !strings.Contains(verdicts, want) {
			t.Errorf("missing expected verdict %q in:\n%s", want, verdicts)
		}
	}
	for _, breach := range []string{"key=READABLE", "config=READABLE", "statedb=READABLE", "sibling=READABLE"} {
		if strings.Contains(verdicts, breach) {
			t.Errorf("WALL BREACHED: %q", breach)
		}
	}
}
