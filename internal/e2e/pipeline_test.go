package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/dispatch"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/log"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func TestEndToEndPipelineWithConditionalSkip(t *testing.T) {
	// 1. Setup Environment
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "ductile.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")

	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("failed to create dir %s: %v", pluginsDir, err)
	}

	log.Setup("ERROR")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	triggerScript := `#!/bin/bash
echo '{"status":"ok","result":"triggered","events":[{"type":"test.triggered","event_id":"stable-id","payload":{"origin_user":"matt","video_url":"https://yt.com/123","status":"ok"}}]}'
`
	createPlugin(t, pluginsDir, "trigger", triggerScript)

	skipScript := `#!/bin/bash
read input
echo '{"status":"ok","result":"unexpected"}'
`
	createPlugin(t, pluginsDir, "skipme", skipScript)

	notifyScript := `#!/usr/bin/env python3
import sys, json
req = json.load(sys.stdin)
print(json.dumps({"status":"ok","result":"verified"}))
`
	createPlugin(t, pluginsDir, "notifier", notifyScript)

	pipelineYAML := `pipelines:
  - name: e2e-if
    on: test.triggered
    steps:
      - id: step_skip
        uses: skipme
        if:
          path: payload.status
          op: eq
          value: error
      - id: step_notify
        uses: notifier
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0644); err != nil {
		t.Fatalf("failed to write pipeline: %v", err)
	}

	registry, _ := plugin.DiscoverManyWithOptions([]string{pluginsDir}, func(l, m string, a ...any) {}, plugin.DiscoverOptions{AllowSymlinks: true})
	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("failed to load router: %v", err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	for _, p := range []string{"trigger", "skipme", "notifier"} {
		cfg.Plugins[p] = config.PluginConf{Enabled: true}
	}

	hub := events.NewHub(128)
	disp := dispatch.New(q, st, contextStore, routerEngine, registry, hub, cfg)

	rootID, _ := q.Enqueue(ctx, queue.EnqueueRequest{Plugin: "trigger", Command: "poll", SubmittedBy: "test"})
	for range 5 {
		job, _ := q.Dequeue(ctx)
		if job == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		disp.ExecuteJob(ctx, job)
	}

	var switchStatus string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE plugin = 'core.switch' LIMIT 1").Scan(&switchStatus); err != nil {
		t.Fatalf("query core.switch status: %v", err)
	}
	if switchStatus != "succeeded" {
		t.Fatalf("core.switch status = %q, want succeeded", switchStatus)
	}

	var skipmeCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM job_queue WHERE plugin = 'skipme'").Scan(&skipmeCount); err != nil {
		t.Fatalf("count skipme jobs: %v", err)
	}
	if skipmeCount != 0 {
		t.Fatalf("skipme job count = %d, want 0", skipmeCount)
	}
	var notifyStatus string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE plugin = 'notifier' LIMIT 1").Scan(&notifyStatus); err != nil {
		t.Fatalf("query notifier status: %v", err)
	}
	if notifyStatus != "succeeded" {
		t.Fatalf("notifier status = %q, want succeeded", notifyStatus)
	}

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM job_queue WHERE parent_job_id = ?", rootID).Scan(&count); err != nil {
		t.Fatalf("count child jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("child job count = %d, want 1", count)
	}
}

func TestEndToEndPipeline(t *testing.T) {
	// 1. Setup Environment
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "ductile.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")

	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("failed to create dir %s: %v", pluginsDir, err)
	}

	log.Setup("ERROR") // Keep logs clean
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	// 2. Create Real Bash Plugins
	// Hop 1: Trigger (Emits event with origin metadata)
	triggerScript := `#!/bin/bash
echo '{"status":"ok","result":"triggered","events":[{"type":"test.triggered","event_id":"stable-id","payload":{"origin_user":"matt","video_url":"https://yt.com/123"}}]}'
`
	createPlugin(t, pluginsDir, "trigger", triggerScript)

	// Hop 2: Processor (emits a downstream event)
	processScript := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"processed","events":[{"type":"test.processed","payload":{"status":"complete"}}]}'
`
	createPlugin(t, pluginsDir, "processor", processScript)

	// Hop 3: Notifier (verifies merged baggage)
	notifyScript := `#!/usr/bin/env python3
import sys, json
try:
    req = json.load(sys.stdin)
    payload = req.get("event", {}).get("payload", {})

    # Check CORE MERGE (baggage should be in payload)
    if payload.get("origin_user") != "matt":
        print(json.dumps({"status":"error","error":f"baggage not merged into payload: {payload}"}))
        sys.exit(0)

    print(json.dumps({"status":"ok","result":"verified all","logs":[{"level":"info","message":"verified all"}]}))
except Exception as e:
    print(json.dumps({"status":"error","error":str(e)}))
`
	createPlugin(t, pluginsDir, "notifier", notifyScript)
	// Update createPlugin to support different shebangs if needed, or just chmod

	// 3. Define Pipeline
	pipelineYAML := `pipelines:
  - name: e2e-chain
    on: test.triggered
    steps:
      - id: step_process
        uses: processor
        baggage:
          origin_user: payload.origin_user
      - id: step_notify
        uses: notifier
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0644); err != nil {
		t.Fatalf("failed to write pipeline: %v", err)
	}

	// 4. Discover and Load
	registry, _ := plugin.DiscoverManyWithOptions([]string{pluginsDir}, func(l, m string, a ...any) {}, plugin.DiscoverOptions{AllowSymlinks: true})
	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("failed to load router: %v", err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	for _, p := range []string{"trigger", "processor", "notifier"} {
		cfg.Plugins[p] = config.PluginConf{Enabled: true}
	}

	hub := events.NewHub(128)
	disp := dispatch.New(q, st, contextStore, routerEngine, registry, hub, cfg)

	// 5. Execution Loop
	// Step A: Manually enqueue the root job
	rootID, _ := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin: "trigger", Command: "poll", SubmittedBy: "test",
	})

	var rootJob *queue.Job
	// Run until queue is empty (max 3 jobs expected)
	for range 5 {
		job, _ := q.Dequeue(ctx)
		if job == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if job.ID == rootID {
			rootJob = job
		}
		disp.ExecuteJob(ctx, job)
	}

	// 6. Assertions
	var notifierJobID string
	if err := db.QueryRow("SELECT id FROM job_queue WHERE plugin = 'notifier' LIMIT 1").Scan(&notifierJobID); err != nil {
		t.Fatalf("query notifier job id: %v", err)
	}
	if notifierJobID == "" {
		t.Fatalf("notifier job never ran")
	}

	var notifierStatus string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", notifierJobID).Scan(&notifierStatus); err != nil {
		t.Fatalf("query notifier job status: %v", err)
	}
	if notifierStatus != "succeeded" {
		t.Fatalf("notifier job failed: %s", notifierStatus)
	}

	var contextID string
	if err := db.QueryRow("SELECT event_context_id FROM job_queue WHERE id = ?", notifierJobID).Scan(&contextID); err != nil {
		t.Fatalf("query notifier context id: %v", err)
	}
	lineage, err := contextStore.Lineage(ctx, contextID)
	if err != nil {
		t.Fatalf("failed to load lineage: %v", err)
	}
	// Step 1: shared pipeline instance root (blank step, parent nil)
	// Step 2: Processor (parent = root)
	// Step 3: Notifier (parent = processor)
	if len(lineage) != 3 {
		t.Errorf("expected 3 context hops, got %d", len(lineage))
	}
	if len(lineage) == 3 {
		if lineage[0].StepID != "" {
			t.Errorf("root lineage step_id = %q, want empty", lineage[0].StepID)
		}
		if lineage[1].StepID != "step_process" {
			t.Errorf("processor lineage step_id = %q, want step_process", lineage[1].StepID)
		}
		if lineage[2].StepID != "step_notify" {
			t.Errorf("notifier lineage step_id = %q, want step_notify", lineage[2].StepID)
		}
	}

	// Verify Baggage Flow
	var finalBaggage map[string]any
	if err := json.Unmarshal(lineage[len(lineage)-1].AccumulatedJSON, &finalBaggage); err != nil {
		t.Fatalf("unmarshal final baggage: %v", err)
	}
	if finalBaggage["origin_user"] != "matt" {
		t.Errorf("origin_user baggage lost, got: %v", finalBaggage)
	}

	// 7. Verify Idempotency (Parent Retry)
	// If we run the trigger job again, it should NOT create new child jobs
	// because the (parent_job_id, source_event_id) unique constraint will trigger INSERT OR IGNORE.
	disp.ExecuteJob(ctx, rootJob)

	var childCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM job_queue WHERE parent_job_id = ?", rootID).Scan(&childCount); err != nil {
		t.Fatalf("count child jobs: %v", err)
	}
	if childCount != 1 {
		t.Errorf("expected exactly 1 child job after parent retry, got %d", childCount)
	}
}

func createPlugin(t *testing.T, dir, name, script string) {
	t.Helper()
	requireScriptInterpreter(t, script)

	pDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pDir, 0755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", pDir, err)
	}
	manifest := fmt.Sprintf("manifest_spec: ductile.plugin\nmanifest_version: 1\nname: %s\nversion: 1.0.0\nprotocol: 2\nentrypoint: ./run.sh\ncommands:\n  - name: poll\n    type: write\n  - name: handle\n    type: write", name)
	if err := os.WriteFile(filepath.Join(pDir, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", filepath.Join(pDir, "manifest.yaml"), err)
	}
	if err := os.WriteFile(filepath.Join(pDir, "run.sh"), []byte(script), 0755); err != nil {
		t.Fatalf("WriteFile(%s): %v", filepath.Join(pDir, "run.sh"), err)
	}
}
