package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/queue"
)

// TestDispatcher_ExecuteJob_WritesStopwatchRow proves the supervisor's
// ledger receives a row per successful invocation. The plugin is a
// black-box echo; we don't inspect the plugin's view, we inspect the DB.
func TestDispatcher_ExecuteJob_WritesStopwatchRow(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status": "ok", "result": "ok"}'
`
	plug := createTestPlugin(t, pluginsDir, "echo", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	disp.cfg.Plugins["echo"] = config.PluginConf{
		Enabled: true,
		Config:  map[string]any{},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin: "echo", Command: "poll", SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	disp.executeJob(ctx, job)

	var (
		gotJobID, gotPlugin, gotStatus string
		gotAttempt                     int
		gotDurNs                       int64
	)
	err = db.QueryRow(`
		SELECT job_id, plugin, status, attempt, dur_ns
		FROM job_stopwatch WHERE job_id = ?
	`, jobID).Scan(&gotJobID, &gotPlugin, &gotStatus, &gotAttempt, &gotDurNs)
	if err != nil {
		t.Fatalf("query job_stopwatch: %v", err)
	}

	if gotJobID != jobID || gotPlugin != "echo" || gotStatus != "ok" {
		t.Errorf("row mismatch: jobID=%q plugin=%q status=%q (want jobID=%q plugin=echo status=ok)",
			gotJobID, gotPlugin, gotStatus, jobID)
	}
	if gotAttempt < 1 {
		t.Errorf("attempt must be >= 1, got %d", gotAttempt)
	}
	if gotDurNs <= 0 {
		t.Errorf("dur_ns must be positive, got %d", gotDurNs)
	}
}

// TestDispatcher_ExecuteJob_WritesStopwatchRowForPluginError proves that
// the supervisor still emits a row even when the plugin returns error —
// observability does not depend on success.
func TestDispatcher_ExecuteJob_WritesStopwatchRowForPluginError(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status": "error", "error": "deliberate test failure"}'
`
	plug := createTestPlugin(t, pluginsDir, "fail-echo", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	disp.cfg.Plugins["fail-echo"] = config.PluginConf{
		Enabled: true,
		Config:  map[string]any{},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin: "fail-echo", Command: "poll", SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	disp.executeJob(ctx, job)

	var status string
	err = db.QueryRow(`SELECT status FROM job_stopwatch WHERE job_id = ?`, jobID).Scan(&status)
	if err != nil {
		t.Fatalf("query job_stopwatch: %v", err)
	}
	if status != "err" {
		t.Errorf("expected status=err on plugin error, got %q", status)
	}
}
