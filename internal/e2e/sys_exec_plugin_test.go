package e2e

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/protocol"
)

func TestSysExecPlugin_HandleSuccessAndPayloadEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sys_exec plugin relies on /bin/sh semantics")
	}

	resp, _, stderr, err := runSysExec(t, "handle", map[string]any{
		"command":                 []any{"printf", "%s", "$DUCTILE_PAYLOAD_NAME"},
		"include_output_in_event": true,
	}, map[string]any{
		"name": "matt",
	})
	if err != nil {
		t.Fatalf("runSysExec(handle success): %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "ok" {
		t.Fatalf("status=%q want ok (stderr=%q)", resp.Status, stderr)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("events len=%d want 1", len(resp.Events))
	}
	if resp.Events[0].Type != "sys_exec.completed" {
		t.Fatalf("event type=%q want sys_exec.completed", resp.Events[0].Type)
	}
	if got := resp.Events[0].Payload["stdout"]; got != "matt" {
		t.Fatalf("event payload stdout=%v want matt", got)
	}
	if got := resp.Events[0].Payload["exit_code"]; got != float64(0) {
		t.Fatalf("event payload exit_code=%v want 0", got)
	}
}

func TestSysExecPlugin_HandleNonZeroExitReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sys_exec plugin relies on /bin/sh semantics")
	}

	resp, compat, stderr, err := runSysExec(t, "handle", map[string]any{
		"command": []any{"/bin/sh", "-c", `echo "boom" 1>&2; exit 7`},
	}, map[string]any{
		"ignored": "value",
	})
	if err != nil {
		t.Fatalf("runSysExec(handle error): %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "error" {
		t.Fatalf("status=%q want error", resp.Status)
	}
	if resp.Error == "" {
		t.Fatalf("expected non-empty error")
	}
	if compat.Retry == nil || *compat.Retry {
		t.Fatalf("expected retry compatibility hint=false for default non-zero exit")
	}
	if len(resp.Events) != 1 {
		t.Fatalf("events len=%d want 1", len(resp.Events))
	}
	if got := resp.Events[0].Payload["exit_code"]; got != float64(7) {
		t.Fatalf("event payload exit_code=%v want 7", got)
	}
}

func TestSysExecPlugin_HandleRetryOnConfiguredExitCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sys_exec plugin relies on /bin/sh semantics")
	}

	resp, compat, stderr, err := runSysExec(t, "handle", map[string]any{
		"command":             []any{"/bin/sh", "-c", `exit 75`},
		"retry_on_exit_codes": []any{75},
	}, map[string]any{})
	if err != nil {
		t.Fatalf("runSysExec(handle retry): %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "error" {
		t.Fatalf("status=%q want error", resp.Status)
	}
	if compat.Retry == nil || !*compat.Retry {
		t.Fatalf("expected retry compatibility hint=true for configured retry exit code")
	}
}

func TestSysExecPlugin_HealthRequiresCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sys_exec plugin relies on /bin/sh semantics")
	}

	resp, _, stderr, err := runSysExec(t, "health", map[string]any{}, nil)
	if err != nil {
		t.Fatalf("runSysExec(health): %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "error" {
		t.Fatalf("status=%q want error", resp.Status)
	}
	if resp.Error == "" {
		t.Fatalf("expected health error when command missing")
	}
}

func TestSysExecPlugin_LogIncludesUpstreamPipelinePlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sys_exec plugin relies on /bin/sh semantics")
	}

	root := repoRoot(t)
	script := filepath.Join(root, "plugins", "sys_exec", "run.py")
	requireEntrypointInterpreter(t, root, "sys_exec", "run.py")
	event := &protocol.Event{Type: "test.event", Payload: map[string]any{}}

	req := &protocol.Request{
		Protocol: 2,
		JobID:    "sys-exec-test-job",
		Command:  "handle",
		Config: map[string]any{
			"command": "echo ok",
		},
		State: map[string]any{},
		Context: map[string]any{
			"ductile_pipeline": "notify-discord-on-transcript",
			"ductile_plugin":   "sys_exec",
		},
		Event:      event,
		DeadlineAt: time.Now().Add(30 * time.Second).UTC(),
	}

	var stdin bytes.Buffer
	if err := protocol.EncodeRequest(&stdin, req); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, script)
	cmd.Stdin = &stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("run sys_exec: %v (stderr=%q)", err, stderr.String())
	}

	resp, _, _, err := protocol.DecodeResponseLenient(&stdout)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Logs) == 0 {
		t.Fatalf("expected logs in response")
	}
	if !strings.Contains(resp.Logs[0].Message, "upstream notify-discord-on-transcript:sys_exec") {
		t.Fatalf("first log message = %q, want upstream pipeline:plugin", resp.Logs[0].Message)
	}
}

func TestSysExecPlugin_PollSuccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sys_exec plugin relies on /bin/sh semantics")
	}

	resp, _, stderr, err := runSysExec(t, "poll", map[string]any{
		"command": "echo 'poll-success'",
	}, nil)
	if err != nil {
		t.Fatalf("runSysExec(poll success): %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "ok" {
		t.Fatalf("status=%q want ok (stderr=%q)", resp.Status, stderr)
	}
	if !strings.Contains(resp.Result, "exited with code 0") {
		t.Fatalf("result=%q, want success message", resp.Result)
	}
}

func runSysExec(t *testing.T, command string, cfg map[string]any, payload map[string]any) (*protocol.Response, protocol.ResponseCompat, string, error) {
	t.Helper()

	root := repoRoot(t)
	script := filepath.Join(root, "plugins", "sys_exec", "run.py")
	requireEntrypointInterpreter(t, root, "sys_exec", "run.py")
	event := &protocol.Event{Type: "test.event", Payload: payload}

	req := &protocol.Request{
		Protocol:   2,
		JobID:      "sys-exec-test-job",
		Command:    command,
		Config:     cfg,
		State:      map[string]any{},
		Event:      event,
		DeadlineAt: time.Now().Add(30 * time.Second).UTC(),
	}

	var stdin bytes.Buffer
	if err := protocol.EncodeRequest(&stdin, req); err != nil {
		return nil, protocol.ResponseCompat{}, "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, script)
	cmd.Stdin = &stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run()

	resp, compat, _, err := protocol.DecodeResponseLenient(&stdout)
	if err != nil {
		return nil, protocol.ResponseCompat{}, stderr.String(), err
	}
	return resp, compat, stderr.String(), nil
}
