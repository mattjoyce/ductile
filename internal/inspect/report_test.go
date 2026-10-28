package inspect

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/configsnapshot"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func TestBuildReportRendersLineage(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	ctxStore := state.NewContextStore(db)
	q := queue.New(db)

	rootCtx, err := ctxStore.Create(ctx, nil, "chain", "step_a", json.RawMessage(`{"origin_channel_id":"chan-1","message":"hello"}`))
	if err != nil {
		t.Fatalf("Create(root context): %v", err)
	}
	rootJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "plugin-a",
		Command:        "poll",
		SubmittedBy:    "test",
		EventContextID: &rootCtx.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue(root): %v", err)
	}

	childCtx, err := ctxStore.Create(ctx, &rootCtx.ID, "chain", "step_b", json.RawMessage(`{"farewell":"goodbye"}`))
	if err != nil {
		t.Fatalf("Create(child context): %v", err)
	}
	childJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "plugin-b",
		Command:        "handle",
		SubmittedBy:    "route",
		ParentJobID:    &rootJobID,
		EventContextID: &childCtx.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue(child): %v", err)
	}

	out, err := BuildReport(ctx, db, dbPath, childJobID)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	for _, needle := range []string{
		"Lineage Report",
		"chain :: step_a",
		"chain :: step_b",
		"origin_channel_id",
		"chan-1",
		childJobID,
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("output missing %q:\n%s", needle, out)
		}
	}
	// Sprint 18: report no longer renders workspace_path or artifacts.
	if strings.Contains(out, "workspace  :") {
		t.Fatalf("report still renders workspace line:\n%s", out)
	}
	if strings.Contains(out, "artifacts  :") {
		t.Fatalf("report still renders artifacts line:\n%s", out)
	}
}

func TestBuildReportRendersConfigSnapshots(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	enqueued := testSnapshot("cfg_enqueued", "startup", time.Date(2026, 4, 18, 1, 0, 0, 0, time.UTC))
	started := testSnapshot("cfg_started", "reload", time.Date(2026, 4, 18, 2, 0, 0, 0, time.UTC))
	started.SecretFingerprints = json.RawMessage(`[{"purpose":"webhooks.github.secret_ref","ref":"github_webhook_secret","source":"tokens.yaml","present":true,"fingerprint":"secretfp_sha256:test"}]`)
	if err := configsnapshot.Insert(ctx, db, enqueued); err != nil {
		t.Fatalf("Insert(enqueued): %v", err)
	}
	if err := configsnapshot.Insert(ctx, db, started); err != nil {
		t.Fatalf("Insert(started): %v", err)
	}

	activeSnapshotID := "cfg_enqueued"
	q := queue.New(db, queue.WithConfigSnapshotIDProvider(func() string { return activeSnapshotID }))
	jobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	activeSnapshotID = "cfg_started"
	if _, err := q.Dequeue(ctx); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}

	out, err := BuildReport(ctx, db, dbPath, jobID)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	for _, needle := range []string{
		"Config",
		"enqueued under: cfg_enqueued startup",
		"started under : cfg_started reload",
		"enqueued semantics: baggage_durability=author_explicit_claims",
		"retry_policy_owner=core",
		"started semantics : baggage_durability=author_explicit_claims",
		"crossed reload boundary: true",
		"github_webhook_secret used by webhooks.github.secret_ref",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("BuildReport output missing %q:\n%s", needle, out)
		}
	}

	jsonOut, err := BuildJSONReport(ctx, db, dbPath, jobID)
	if err != nil {
		t.Fatalf("BuildJSONReport: %v", err)
	}
	var report Report
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	if report.Config.Enqueued == nil || report.Config.Enqueued.ID != "cfg_enqueued" {
		t.Fatalf("JSON enqueued config = %+v", report.Config.Enqueued)
	}
	if report.Config.Started == nil || report.Config.Started.ID != "cfg_started" {
		t.Fatalf("JSON started config = %+v", report.Config.Started)
	}
	if !report.Config.CrossedReloadBoundary {
		t.Fatal("expected crossed reload boundary")
	}
	if report.Config.Started.Semantics["baggage_immutability"] != "deep_accretion_immutable_paths" {
		t.Fatalf("started semantics = %+v", report.Config.Started.Semantics)
	}
}

func testSnapshot(id, reason string, loadedAt time.Time) *configsnapshot.Snapshot {
	sourceHash := "sha256:source"
	sourcePath := "/tmp/config.yaml"
	source := "explicit"
	version := "test-version"
	binaryPath := "/tmp/ductile"
	return &configsnapshot.Snapshot{
		ID:                 id,
		ConfigHash:         "sha256:" + id,
		SourceHash:         &sourceHash,
		SourcePath:         &sourcePath,
		Source:             &source,
		Reason:             reason,
		LoadedAt:           loadedAt,
		DuctileVersion:     &version,
		BinaryPath:         &binaryPath,
		SnapshotFormat:     configsnapshot.SnapshotFormat,
		Semantics:          json.RawMessage(`{"baggage_durability":"author_explicit_claims","baggage_immutability":"deep_accretion_immutable_paths","baggage_transition":"explicit_claims_only","plugin_retry_field":"v2_boundary_compatibility_signal","retry_policy_owner":"core"}`),
		PluginFingerprints: json.RawMessage(`[]`),
		SanitizedConfig:    json.RawMessage(`{}`),
		SecretFingerprints: json.RawMessage(`[]`),
	}
}

func TestBuildJSONReport(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	ctxStore := state.NewContextStore(db)
	q := queue.New(db)

	rootCtx, _ := ctxStore.Create(ctx, nil, "chain", "step_a", json.RawMessage(`{"origin":"test"}`))
	rootJobID, _ := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "p",
		Command:        "c",
		SubmittedBy:    "test",
		EventContextID: &rootCtx.ID,
	})

	out, err := BuildJSONReport(ctx, db, dbPath, rootJobID)
	if err != nil {
		t.Fatalf("BuildJSONReport: %v", err)
	}

	var report Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v", err)
	}

	if report.JobID != rootJobID {
		t.Errorf("job_id = %s, want %s", report.JobID, rootJobID)
	}
	if len(report.Steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(report.Steps))
	}
	if report.Steps[0].StepID != "step_a" {
		t.Errorf("step_id = %s, want %s", report.Steps[0].StepID, "step_a")
	}
}

func TestBuildJSONReportWithoutContextID(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	q := queue.New(db)

	jobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	out, err := BuildJSONReport(ctx, db, dbPath, jobID)
	if err != nil {
		t.Fatalf("BuildJSONReport: %v", err)
	}

	var report Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v", err)
	}

	if report.JobID != jobID {
		t.Errorf("job_id = %s, want %s", report.JobID, jobID)
	}
	if report.ContextID != "" {
		t.Errorf("context_id = %q, want empty", report.ContextID)
	}
	if report.Hops != 0 {
		t.Errorf("hops = %d, want 0", report.Hops)
	}
	if len(report.Steps) != 0 {
		t.Errorf("steps = %d, want 0", len(report.Steps))
	}
}

func TestBuildJSONReportIncludesExecutionHistory(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	q := queue.New(db)

	jobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := q.Dequeue(ctx); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if err := q.CompleteWithResult(ctx, jobID, queue.StatusSucceeded, json.RawMessage(`{"status":"ok"}`), nil, nil); err != nil {
		t.Fatalf("CompleteWithResult: %v", err)
	}

	out, err := BuildJSONReport(ctx, db, dbPath, jobID)
	if err != nil {
		t.Fatalf("BuildJSONReport: %v", err)
	}

	var report Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v", err)
	}

	if report.Attempt != 1 {
		t.Fatalf("attempt=%d want 1", report.Attempt)
	}
	if len(report.Transitions) != 3 {
		t.Fatalf("transitions len=%d want 3: %+v", len(report.Transitions), report.Transitions)
	}
	if report.Transitions[0].From != "" || report.Transitions[0].To != string(queue.StatusQueued) {
		t.Fatalf("first transition=%+v want NULL -> queued", report.Transitions[0])
	}
	if report.Transitions[2].From != string(queue.StatusRunning) || report.Transitions[2].To != string(queue.StatusSucceeded) {
		t.Fatalf("last transition=%+v want running -> succeeded", report.Transitions[2])
	}
	if len(report.Attempts) != 1 || report.Attempts[0].Attempt != 1 {
		t.Fatalf("attempt facts=%+v want attempt 1", report.Attempts)
	}
	if !report.Consistency.CachedStatusMatches {
		t.Fatal("expected cached status to match latest transition")
	}
	if !report.Consistency.AttemptFactsMatch {
		t.Fatal("expected attempt facts to match cache")
	}
	if report.Consistency.LegacyMissingData {
		t.Fatal("did not expect legacy missing data")
	}
}
