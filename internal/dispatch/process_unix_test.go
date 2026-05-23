//go:build darwin || linux || freebsd || openbsd || netbsd

package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/protocol"
)

func TestSpawnPluginTimeoutKillsProcessGroup(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	childPIDPath := filepath.Join(tmpDir, "child.pid")
	script := fmt.Sprintf(`#!/bin/sh
(
  trap '' TERM
  echo $$ > %q
  while :; do sleep 1; done
) &
while [ ! -s %q ]; do sleep 0.05; done
while :; do sleep 1; done
`, childPIDPath, childPIDPath)
	scriptPath := writeDispatchTestScript(t, script)

	d := &Dispatcher{events: events.NewHub(16), cfg: config.Defaults()}
	req := &protocol.Request{Protocol: 2, JobID: "job-timeout", Command: "poll"}
	_, _, _, _, _, _, err := d.spawnPlugin(context.Background(), "timeout-plugin", scriptPath, req, time.Second, slog.Default())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("spawnPlugin error = %v, want context deadline exceeded", err)
	}

	pid, err := readPID(childPIDPath)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("child process %d survived plugin timeout", pid)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
