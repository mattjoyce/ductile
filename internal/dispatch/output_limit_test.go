//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/protocol"
)

func TestBoundedBufferCapsMemoryAndReportsFullWrite(t *testing.T) {
	buf := newBoundedBuffer(3)
	n, err := buf.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 6 {
		t.Fatalf("Write count = %d, want 6", n)
	}
	if got := string(buf.Bytes()); got != "abc" {
		t.Fatalf("buffer = %q, want abc", got)
	}
	if !buf.Truncated() {
		t.Fatal("expected truncated buffer")
	}
}

func TestSpawnPluginFailsWhenStdoutExceedsLimit(t *testing.T) {
	t.Parallel()

	scriptPath := writeDispatchTestScript(t, fmt.Sprintf(`#!/bin/sh
head -c %d /dev/zero | tr '\000' x
`, maxStdoutBytes+1024))

	d := &Dispatcher{events: events.NewHub(16), cfg: config.Defaults()}
	req := &protocol.Request{Protocol: 2, JobID: "job-stdout-limit", Command: "poll"}
	_, _, rawResp, rawStdout, _, _, err := d.spawnPlugin(context.Background(), "stdout-limit", scriptPath, req, 5*time.Second, slog.Default())
	var limitErr outputLimitError
	if !errors.As(err, &limitErr) {
		t.Fatalf("spawnPlugin error = %v, want outputLimitError", err)
	}
	if limitErr.stream != "stdout" {
		t.Fatalf("limit stream = %q, want stdout", limitErr.stream)
	}
	if len(rawStdout) != maxStdoutBytes {
		t.Fatalf("raw stdout len = %d, want %d", len(rawStdout), maxStdoutBytes)
	}
	if len(rawResp) != maxStdoutBytes {
		t.Fatalf("raw response len = %d, want %d", len(rawResp), maxStdoutBytes)
	}
}

func TestSpawnPluginTruncatesStderrWithoutFailingValidResponse(t *testing.T) {
	t.Parallel()

	scriptPath := writeDispatchTestScript(t, fmt.Sprintf(`#!/bin/sh
head -c %d /dev/zero | tr '\000' e >&2
echo '{"status":"ok","result":"ok"}'
`, maxStderrBytes+1024))

	d := &Dispatcher{events: events.NewHub(16), cfg: config.Defaults()}
	req := &protocol.Request{Protocol: 2, JobID: "job-stderr-limit", Command: "poll"}
	resp, _, _, _, stderr, _, err := d.spawnPlugin(context.Background(), "stderr-limit", scriptPath, req, 5*time.Second, slog.Default())
	if err != nil {
		t.Fatalf("spawnPlugin: %v", err)
	}
	if resp == nil || resp.Status != "ok" {
		t.Fatalf("response = %#v, want ok", resp)
	}
	if len(stderr) != maxStderrBytes {
		t.Fatalf("stderr len = %d, want %d", len(stderr), maxStderrBytes)
	}
}

func writeDispatchTestScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.sh")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create script: %v", err)
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		t.Fatalf("write script: %v", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		t.Fatalf("sync script: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close script: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("chmod script: %v", err)
	}
	return path
}
