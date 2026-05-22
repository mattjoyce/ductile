package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/api"
	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/dispatch"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

type queueBackedWaiter struct {
	q *queue.Queue
}

func (w *queueBackedWaiter) WaitForJobTree(ctx context.Context, rootJobID string, timeout time.Duration) ([]*queue.JobResult, error) {
	return w.q.GetJobTree(ctx, rootJobID)
}

func createAPIIntegrationPlugin(t *testing.T, pluginsDir, name, script string) {
	t.Helper()
	pluginDir := filepath.Join(pluginsDir, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pluginDir): %v", err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: ` + name + `
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: handle
    type: write
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(run.sh): %v", err)
	}
}

func allocateLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate local addr: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close temp listener: %v", err)
	}
	return addr
}

// TestAPIIntegration tests the API flow with real queue, router, waiter, and context store.
func TestAPIIntegration(t *testing.T) {
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, ":memory:")
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer func() { _ = db.Close() }()

	q := queue.New(db)
	ctxStore := state.NewContextStore(db)

	registry := plugin.NewRegistry()
	if err := registry.Add(&plugin.Plugin{
		Name: "echo",
		Commands: plugin.Commands{
			{Name: "poll", Type: plugin.CommandTypeWrite},
			{Name: "handle", Type: plugin.CommandTypeWrite},
		},
	}); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	r := router.New(&dsl.Set{
		Pipelines: map[string]*dsl.Pipeline{
			"test-pipeline": {
				Name:          "test-pipeline",
				Trigger:       "test.trigger",
				ExecutionMode: "asynchronous",
				Nodes: map[string]dsl.Node{
					"entry": {ID: "entry", Kind: dsl.NodeKindUses, Uses: "echo"},
				},
				EntryNodeIDs:    []string{"entry"},
				TerminalNodeIDs: []string{"entry"},
			},
		},
	}, slog.Default())

	listenAddr := allocateLocalAddr(t)
	server := api.New(api.Config{
		Listen: listenAddr,
		Tokens: []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
	}, q, registry, r, &queueBackedWaiter{q: q}, ctxStore, state.NewAdmitter(q, state.DefaultMaxContextBytes), events.NewHub(10), slog.Default())

	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	serverReady := make(chan error, 1)
	go func() {
		if err := server.Start(serverCtx); err != nil && err != context.Canceled {
			serverReady <- err
		}
	}()

	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-serverReady:
		t.Fatalf("server failed to start: %v", err)
	default:
	}

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := fmt.Sprintf("http://%s", listenAddr)

	t.Run("direct plugin trigger enqueues real job", func(t *testing.T) {
		triggerBody := []byte(`{"payload": {"test": "data"}}`)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/plugin/echo/poll", bytes.NewReader(triggerBody))
		req.Header.Set("Authorization", "Bearer test-key-123")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to trigger job: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected status 202, got %d", resp.StatusCode)
		}

		var triggerResp api.TriggerResponse
		if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
			t.Fatalf("failed to decode trigger response: %v", err)
		}
		if triggerResp.JobID == "" {
			t.Fatal("expected non-empty job_id")
		}

		req, _ = http.NewRequest(http.MethodGet, baseURL+"/job/"+triggerResp.JobID, nil)
		req.Header.Set("Authorization", "Bearer test-key-123")
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to get job: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var jobResp api.JobStatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
			t.Fatalf("failed to decode job response: %v", err)
		}
		if jobResp.JobID != triggerResp.JobID || jobResp.Plugin != "echo" || jobResp.Command != "poll" {
			t.Fatalf("unexpected job response: %+v", jobResp)
		}
	})

	t.Run("pipeline trigger uses real router and context store", func(t *testing.T) {
		triggerBody := []byte(`{"payload": {"test": "pipeline"}}`)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/pipeline/test-pipeline", bytes.NewReader(triggerBody))
		req.Header.Set("Authorization", "Bearer test-key-123")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to trigger pipeline: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("expected status 202, got %d", resp.StatusCode)
		}

		var triggerResp api.TriggerResponse
		if err := json.NewDecoder(resp.Body).Decode(&triggerResp); err != nil {
			t.Fatalf("failed to decode trigger response: %v", err)
		}

		job, err := q.GetJobByID(ctx, triggerResp.JobID)
		if err != nil {
			t.Fatalf("GetJobByID: %v", err)
		}
		if job.Plugin != "echo" || job.Command != "handle" {
			t.Fatalf("unexpected pipeline job: %+v", job)
		}

		rows, err := db.Query("SELECT event_context_id FROM job_queue WHERE id = ?", triggerResp.JobID)
		if err != nil {
			t.Fatalf("query event_context_id: %v", err)
		}
		var contextID string
		if !rows.Next() {
			_ = rows.Close()
			t.Fatal("expected queued pipeline job row")
		}
		if err := rows.Scan(&contextID); err != nil {
			_ = rows.Close()
			t.Fatalf("scan event_context_id: %v", err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close rows: %v", err)
		}
		if contextID == "" {
			t.Fatal("expected non-empty event_context_id")
		}

		eventCtx, err := ctxStore.Get(ctx, contextID)
		if err != nil {
			t.Fatalf("context store get: %v", err)
		}
		if eventCtx.PipelineName != "test-pipeline" || eventCtx.StepID != "entry" {
			t.Fatalf("unexpected event context: %+v", eventCtx)
		}
		if eventCtx.ParentID == nil || *eventCtx.ParentID == "" {
			t.Fatalf("expected entry context to have root parent: %+v", eventCtx)
		}
		if got := state.PipelineInstanceIDFromAccumulated(eventCtx.AccumulatedJSON); got == "" {
			t.Fatal("expected pipeline instance id in entry context")
		}
	})

	t.Run("unauthorized request is rejected", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/plugin/echo/poll", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make unauthorized request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", resp.StatusCode)
		}
	})

	cancel()
	time.Sleep(100 * time.Millisecond)
}

func TestSynchronousPipelineSkippedEntryResponseVsDB(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")

	db, err := storage.OpenSQLite(ctx, dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = db.Close() }()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	createAPIIntegrationPlugin(t, pluginsDir, "step-a", `#!/bin/bash
read input
echo '{"status":"ok","result":"A"}'
`)
	createAPIIntegrationPlugin(t, pluginsDir, "step-b", `#!/bin/bash
read input
echo '{"status":"ok","result":"B"}'
`)

	registry, err := plugin.DiscoverManyWithOptions([]string{pluginsDir}, func(level, msg string, args ...any) {}, plugin.DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	pipelineYAML := `pipelines:
  - name: if-sync
    on: demo.start
    execution_mode: synchronous
    timeout: 5s
    steps:
      - id: first
        uses: step-a
        if:
          path: payload.run_first
          op: eq
          value: true
      - id: second
        uses: step-b
`
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	cfg := config.Defaults()
	cfg.Plugins["step-a"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}
	cfg.Plugins["step-b"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}
	hub := events.NewHub(128)
	disp := dispatch.New(q, st, contextStore, routerEngine, registry, hub, cfg)

	dispatchCtx, dispatchCancel := context.WithCancel(ctx)
	defer dispatchCancel()
	go func() {
		_ = disp.Start(dispatchCtx)
	}()

	testPort := "localhost:18081"
	server := api.New(api.Config{
		Listen: testPort,
		Tokens: []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
	}, q, registry, routerEngine, disp, contextStore, state.NewAdmitter(q, state.DefaultMaxContextBytes), hub, slog.Default())

	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = server.Start(serverCtx)
	}()
	time.Sleep(200 * time.Millisecond)

	baseURL := "http://" + testPort
	body := []byte(`{"payload":{"run_first":false}}`)
	req, err := http.NewRequest(http.MethodPost, baseURL+"/pipeline/if-sync", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-key-123")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var syncResp api.SyncResponse
	if err := json.NewDecoder(resp.Body).Decode(&syncResp); err != nil {
		t.Fatalf("Decode(sync response): %v", err)
	}
	if len(syncResp.Tree) < 2 {
		t.Fatalf("response tree len = %d, want >= 2 once settled-tree waiting is applied", len(syncResp.Tree))
	}
	if syncResp.Tree[0].Status != string(queue.StatusSucceeded) {
		t.Fatalf("response root status = %q, want %q", syncResp.Tree[0].Status, queue.StatusSucceeded)
	}
	var finalResult map[string]any
	if err := json.Unmarshal(syncResp.Result, &finalResult); err != nil {
		t.Fatalf("unmarshal sync result: %v", err)
	}
	if finalResult["result"] != "B" {
		t.Fatalf("sync response result payload = %#v, want result=B", finalResult)
	}

	var immediateChildCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_queue WHERE parent_job_id = ?`, syncResp.JobID).Scan(&immediateChildCount); err != nil {
		t.Fatalf("child count immediate: %v", err)
	}
	if immediateChildCount == 0 {
		t.Fatalf("immediate child count = 0, want >0")
	}

	results, err := q.GetJobTree(ctx, syncResp.JobID)
	if err != nil {
		t.Fatalf("GetJobTree: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("eventual job tree len = %d, want >= 2", len(results))
	}

	cancel()
	_ = serverCtx
}
