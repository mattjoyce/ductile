//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/protocol"
)

// runawayPluginEntry runs from init() when this test binary is re-execed as a
// plugin (see writeRunawayPlugin). Installing the SIGTERM ignore in init()
// — before the testing framework parses flags — closes the startup race where
// SIGTERM could arrive before the handler is in place. It models a misbehaving
// plugin that exceeds its configured timeout: ignore SIGTERM, run well past
// the deadline, then emit a valid response during the grace period. A Go
// helper is used because a /bin/sh trap does not reliably survive SIGTERM on
// macOS.
func init() {
	if os.Getenv("DUCTILE_RUNAWAY_PLUGIN_HELPER") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	// Sleep past the parent's timeout but inside the 5s grace period, then
	// emit valid JSON. This output is produced AFTER the deadline and must
	// not reclassify the timeout as success.
	time.Sleep(3 * time.Second)
	fmt.Println(`{"status":"ok","result":"slow-but-done"}`)
	os.Exit(0)
}

// TestExecutorWithholdsSecretEnvFromChild locks the documented sys_exec
// containment claim: a secret in the gateway's environment must not reach a
// plugin child. The script echoes the variable the way sys_exec's $VAR
// expansion would; under the spawn-hygiene allowlist it expands to empty.
func TestExecutorWithholdsSecretEnvFromChild(t *testing.T) {
	const secret = "s3cr3t-token-value-should-not-leak"
	t.Setenv("DUCTILE_TEST_GATEWAY_SECRET", secret)

	script := writeDispatchTestScript(t, `#!/bin/sh
echo "probe=[$DUCTILE_TEST_GATEWAY_SECRET]"
`)
	executor := newSubprocessExecutor(nil, nil, ResolvedAccount{})
	req := &protocol.Request{Protocol: 2, JobID: "job-env", Command: "poll"}
	// The script emits non-protocol output, so execute returns a decode error;
	// only the raw stdout bytes matter here.
	_, _, _, rawStdout, _, _, _ := executor.execute(
		context.Background(), "env-probe", script, req, 5*time.Second, slog.Default())

	if bytes.Contains(rawStdout, []byte(secret)) {
		t.Fatalf("gateway secret leaked into plugin stdout: %q", rawStdout)
	}
	if !bytes.Contains(rawStdout, []byte("probe=[]")) {
		t.Fatalf("expected the secret var to expand empty in the child; got %q", rawStdout)
	}
}

// writeRunawayPlugin writes a shell shim that re-execs the test binary as a
// plugin that genuinely ignores SIGTERM and overruns its timeout.
func writeRunawayPlugin(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	path := filepath.Join(t.TempDir(), "graceful-plugin.sh")
	shim := fmt.Sprintf(
		"#!/bin/sh\nexec env DUCTILE_RUNAWAY_PLUGIN_HELPER=1 %q -test.run=^$ -test.v=false\n",
		self)
	if err := os.WriteFile(path, []byte(shim), 0o700); err != nil {
		t.Fatalf("write plugin shim: %v", err)
	}
	return path
}

// TestSubprocessExecutorTimeoutNotReclassifiedByLateOutput pins the corrected
// C-FRO-5 scope. C-FRO-5 is strictly the deadline-edge race: a result that is
// ALREADY available when the timer fires must be preferred over a timeout
// (handled by the non-blocking waitErr pre-check). It must NOT change timeout
// semantics: a plugin that ignores SIGTERM, overruns its configured timeout,
// and only emits valid JSON during the grace period is still a timeout — late
// output must never reclassify it as success.
func TestSubprocessExecutorTimeoutNotReclassifiedByLateOutput(t *testing.T) {
	// Not parallel: re-execs this test binary as a child, whose startup
	// latency under -race must stay below the parent timeout. Suite-wide
	// parallel contention inflates that latency.
	scriptPath := writeRunawayPlugin(t)

	executor := newSubprocessExecutor(nil, nil, ResolvedAccount{})
	req := &protocol.Request{Protocol: 2, JobID: "job-runaway", Command: "poll"}
	resp, _, _, _, _, _, err := executor.execute(
		context.Background(), "runaway", scriptPath, req, 1500*time.Millisecond, slog.Default())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("execute error = %v, want context.DeadlineExceeded (timeout must not be reclassified by post-deadline output)", err)
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil (a timed-out plugin's late output must be discarded)", resp)
	}
}

func TestSubprocessExecutorReturnsMalformedProtocolResponse(t *testing.T) {
	t.Parallel()

	scriptPath := writeDispatchTestScript(t, `#!/bin/sh
echo '{not-json'
`)

	executor := newSubprocessExecutor(nil, nil, ResolvedAccount{})
	req := &protocol.Request{Protocol: 2, JobID: "job-malformed", Command: "poll"}
	resp, _, rawResp, rawStdout, _, _, err := executor.execute(context.Background(), "malformed", scriptPath, req, 5*time.Second, slog.Default())
	if err == nil {
		t.Fatal("execute error = nil, want decode response error")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("execute error = %v, want decode response error", err)
	}
	if resp != nil {
		t.Fatalf("response = %#v, want nil", resp)
	}
	if got := string(rawResp); got != "{not-json\n" {
		t.Fatalf("raw response = %q, want malformed stdout", got)
	}
	if got := string(rawStdout); got != "{not-json\n" {
		t.Fatalf("raw stdout = %q, want malformed stdout", got)
	}
}
