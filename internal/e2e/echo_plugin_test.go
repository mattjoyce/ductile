package e2e

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
)

func TestEchoPlugin_DiscoveryAndProtocolOK(t *testing.T) {
	root := repoRoot(t)
	requireEntrypointInterpreter(t, root, "echo", "run.sh")

	// Validate plugin discovery against the repo's ./plugins directory.
	logger := func(level, msg string, args ...any) {}
	reg, err := plugin.DiscoverManyWithOptions([]string{filepath.Join(root, "plugins")}, logger, plugin.DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	p, ok := reg.Get("echo")
	if !ok {
		t.Fatalf("echo plugin not discovered")
	}
	if p.Protocol != 2 {
		t.Fatalf("unexpected protocol: %d", p.Protocol)
	}
	if !p.SupportsCommand("poll") {
		t.Fatalf("echo plugin should support poll")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, stderr, err := runEcho(ctx, root, map[string]any{"message": "hello"})
	if err != nil {
		t.Fatalf("runEcho: %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status=ok, got %q", resp.Status)
	}
	if len(resp.Logs) == 0 {
		t.Fatalf("expected response.logs")
	}
}

func TestEchoPlugin_ErrorMode(t *testing.T) {
	root := repoRoot(t)
	requireEntrypointInterpreter(t, root, "echo", "run.sh")
	requireAnyCommand(t, "the echo plugin parses its config with jq or python3", "jq", "python3")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, stderr, err := runEcho(ctx, root, map[string]any{"mode": "error"})
	if err != nil {
		t.Fatalf("runEcho: %v (stderr=%q)", err, stderr)
	}
	if resp.Status != "error" {
		t.Fatalf("expected status=error, got %q", resp.Status)
	}
	if resp.Error == "" {
		t.Fatalf("expected error message")
	}
}

func TestEchoPlugin_ProtocolErrorMode(t *testing.T) {
	root := repoRoot(t)
	requireEntrypointInterpreter(t, root, "echo", "run.sh")
	requireAnyCommand(t, "the echo plugin parses its config with jq or python3", "jq", "python3")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := runEcho(ctx, root, map[string]any{"mode": "protocol_error"})
	if err == nil {
		t.Fatalf("expected protocol decode failure")
	}
}

func runEcho(ctx context.Context, root string, cfg map[string]any) (*protocol.Response, string, error) {
	script := filepath.Join(root, "plugins", "echo", "run.sh")

	req := &protocol.Request{
		Protocol:   2,
		JobID:      "test-job",
		Command:    "poll",
		Config:     cfg,
		State:      map[string]any{},
		DeadlineAt: time.Now().Add(30 * time.Second).UTC(),
	}

	var stdin bytes.Buffer
	if err := protocol.EncodeRequest(&stdin, req); err != nil {
		return nil, "", err
	}

	cmd := exec.CommandContext(ctx, script)
	cmd.Stdin = &stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// The echo plugin should communicate errors via JSON (status=error), not exit status.
	_ = cmd.Run()

	resp, _, _, err := protocol.DecodeResponseLenient(&stdout)
	if err != nil {
		return nil, stderr.String(), err
	}
	return resp, stderr.String(), nil
}

func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		if t != nil {
			t.Fatal("runtime.Caller failed")
		}
		return ""
	}
	// internal/e2e -> internal -> repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
