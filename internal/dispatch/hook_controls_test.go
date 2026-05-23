package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
)

// TestHookDepthCapRefusesBeyondMax — P2-11: when a hook chain has reached the
// configured max depth, the next would-be hook enqueue is refused. The fixture
// is a self-referencing hook (echo notify_on_complete fires the echo→echo
// pipeline) — exactly the operator footgun the finding describes — and the cap
// is set low (2) so the chain provably stops within a deterministic test run.
func TestHookDepthCapRefusesBeyondMax(t *testing.T) {
	disp, _, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	echoScript := `#!/bin/bash
read input
echo '{"status": "ok", "result": "echo-done"}'
`
	echoPlug := createTestPlugin(t, pluginsDir, "echo", echoScript)
	if err := disp.registry.Add(echoPlug); err != nil {
		t.Fatalf("registry.Add(echo): %v", err)
	}

	// Self-firing hook: echo completes → hook fires echo. Without a depth cap
	// this is an infinite chain.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "self-loop",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "again", Uses: "echo"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	disp.router = router.New(set, nil)

	trueVal := true
	disp.cfg.Plugins["echo"] = config.PluginConf{
		Enabled:          true,
		NotifyOnComplete: &trueVal,
		Timeouts:         &config.TimeoutsConfig{Poll: 5 * time.Second, Handle: 5 * time.Second},
	}
	disp.cfg.Service.HookMaxDepth = 2

	ctx := context.Background()

	// Enqueue root, execute, then drain the hook chain step-by-step.
	if _, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	}); err != nil {
		t.Fatalf("enqueue root: %v", err)
	}

	// We expect: root (depth 0) → hook 1 (depth 1) → hook 2 (depth 2) → REFUSED.
	// Total executed jobs = 3. After that the queue is empty.
	executed := 0
	for executed < 10 { // safety cap to prevent runaway loop if cap is broken
		job, err := disp.queue.Dequeue(ctx)
		if err != nil {
			t.Fatalf("dequeue at executed=%d: %v", executed, err)
		}
		if job == nil {
			break
		}
		disp.executeJob(ctx, job)
		executed++
	}

	if executed > 3 {
		t.Fatalf("hook depth cap failed to stop chain: executed %d jobs (root + hooks); want <= 3 with hook_max_depth=2", executed)
	}
	if executed < 3 {
		t.Fatalf("hook chain stopped too early: executed %d jobs; want 3 (root + 2 hook levels)", executed)
	}
}

// TestHookJobCarriesParentJobID — P2-11: a hook job's ParentJobID must point
// at the source job that fired the hook. This makes the hook chain visible in
// the job queue (operator can see "this echo job spawned that notifier job")
// and is the lineage the depth-cap walks at enqueue time.
func TestHookJobCarriesParentJobID(t *testing.T) {
	disp, _, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	echoScript := `#!/bin/bash
read input
echo '{"status": "ok", "result": "echo-done"}'
`
	echoPlug := createTestPlugin(t, pluginsDir, "echo", echoScript)
	if err := disp.registry.Add(echoPlug); err != nil {
		t.Fatalf("registry.Add(echo): %v", err)
	}
	notifierScript := `#!/bin/bash
read input
echo '{"status": "ok", "result": "notified"}'
`
	notifierPlug := createTestPlugin(t, pluginsDir, "notifier", notifierScript)
	if err := disp.registry.Add(notifierPlug); err != nil {
		t.Fatalf("registry.Add(notifier): %v", err)
	}

	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "notify-on-complete",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "notify", Uses: "notifier"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	disp.router = router.New(set, nil)

	trueVal := true
	disp.cfg.Plugins["echo"] = config.PluginConf{
		Enabled:          true,
		NotifyOnComplete: &trueVal,
		Timeouts:         &config.TimeoutsConfig{Poll: 5 * time.Second},
	}
	disp.cfg.Plugins["notifier"] = config.PluginConf{
		Enabled:  true,
		Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second},
	}

	ctx := context.Background()

	rootID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("enqueue root: %v", err)
	}

	root, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue root: %v", err)
	}
	disp.executeJob(ctx, root)

	hookJob, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue hook: %v", err)
	}
	if hookJob == nil {
		t.Fatal("expected hook job, got nil")
	}
	if hookJob.ParentJobID == nil {
		t.Fatal("hook job ParentJobID is nil; expected to be linked to source root job")
	}
	if *hookJob.ParentJobID != rootID {
		t.Errorf("hook job ParentJobID = %q, want %q (root job id)", *hookJob.ParentJobID, rootID)
	}
}
