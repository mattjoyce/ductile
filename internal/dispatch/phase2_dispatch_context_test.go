package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

// TestPhase2DispatchDedupeDoesNotCreateContextForDroppedReplay characterizes
// P2-09: a routed local replay (same event re-dispatched after the first job
// has succeeded) must NOT create additional event_context rows when the queue
// would dedupe the resulting child job. Before the admission boundary fix the
// dispatcher wrote child + parent context rows BEFORE the queue's dedupe
// check, leaking orphan rows under replay pressure.
func TestPhase2DispatchDedupeDoesNotCreateContextForDroppedReplay(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(plugins): %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := queue.New(db, queue.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), queue.WithDedupeTTL(24*time.Hour))
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	admitter := state.NewAdmitter(q, state.DefaultMaxContextBytes)
	registry := plugin.NewRegistry()
	hub := events.NewHub(64)

	for _, name := range []string{"source", "sink"} {
		plug := createTestPlugin(t, pluginsDir, name, `#!/bin/bash
read input
echo '{"status":"ok","events":[]}'
`)
		if err := registry.Add(plug); err != nil {
			t.Fatalf("registry.Add(%s): %v", name, err)
		}
	}

	pipelineYAML := `pipelines:
  - name: target
    on: replay.local
    steps:
      - id: consume
        uses: sink
        baggage:
          target.trace: payload.trace
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}
	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	cfg := config.Defaults()
	cfg.Plugins["source"] = config.PluginConf{Enabled: true}
	cfg.Plugins["sink"] = config.PluginConf{Enabled: true}
	disp := New(q, st, contextStore, routerEngine, registry, hub, cfg, WithAdmitter(admitter))
	ctx := context.Background()

	parentJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "source",
		Command:     "handle",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue(parent): %v", err)
	}
	parentJob, err := q.Dequeue(ctx)
	if err != nil || parentJob == nil {
		t.Fatalf("Dequeue(parent): job=%v err=%v", parentJob, err)
	}

	event := protocol.Event{
		Type:      "replay.local",
		EventID:   "evt-local-replay-context",
		DedupeKey: "local-replay-context-key",
		Payload:   map[string]any{"trace": "trace-1"},
	}
	if err := disp.routeEvents(ctx, parentJob, []protocol.Event{event}, slog.Default()); err != nil {
		t.Fatalf("routeEvents(first): %v", err)
	}

	child, err := q.Dequeue(ctx)
	if err != nil || child == nil {
		t.Fatalf("Dequeue(child): job=%v err=%v", child, err)
	}
	if child.ParentJobID == nil || *child.ParentJobID != parentJobID {
		t.Fatalf("child parent = %v want %s", child.ParentJobID, parentJobID)
	}
	if err := q.CompleteWithResult(ctx, child.ID, queue.StatusSucceeded, json.RawMessage(`{"status":"ok"}`), nil, nil); err != nil {
		t.Fatalf("CompleteWithResult(child): %v", err)
	}

	before := countDispatchContexts(t, db)
	if err := disp.routeEvents(ctx, parentJob, []protocol.Event{event}, slog.Default()); err != nil {
		t.Fatalf("routeEvents(replay): %v", err)
	}
	after := countDispatchContexts(t, db)

	var childJobs int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_queue WHERE plugin = 'sink'`).Scan(&childJobs); err != nil {
		t.Fatalf("count sink jobs: %v", err)
	}
	if childJobs != 1 {
		t.Fatalf("sink job count = %d, want 1", childJobs)
	}
	if after != before {
		t.Fatalf("deduped local replay created additional contexts; before=%d after=%d", before, after)
	}
}

func countDispatchContexts(t *testing.T, db queryRower) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event_context`).Scan(&count); err != nil {
		t.Fatalf("count event_context: %v", err)
	}
	return count
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}
