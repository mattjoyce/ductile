package state

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mattjoyce/ductile/internal/stopwatch"
	"github.com/mattjoyce/ductile/internal/storage"
)

func TestStoreRecordStopwatch_RoundTrip(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "sw.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Seed a job_queue row so the FK is satisfied.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO job_queue (id, plugin, command, status, submitted_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "job-1", "echo", "poll", "succeeded", "test", "2026-05-23T00:00:00Z"); err != nil {
		t.Fatalf("seed job_queue: %v", err)
	}

	s := NewStore(db)
	rec := stopwatch.Record{
		PluginID:      "echo",
		StepName:      "step-a",
		Attempt:       1,
		EnterWallNs:   1000,
		ExitWallNs:    2500,
		DurNs:         1500,
		RuntimePreNs:  100,
		RuntimePostNs: 50,
		Status:        stopwatch.StatusOK,
		Subs:          []map[string]any{{"name": "db_query", "dur_ns": 700}},
	}

	if err := s.RecordStopwatch(context.Background(), "job-1", rec, "pipe-X", "inst-Y"); err != nil {
		t.Fatalf("RecordStopwatch: %v", err)
	}

	// Read it back and verify shape.
	row := db.QueryRowContext(context.Background(), `
		SELECT job_id, plugin, pipeline, step_id, pipeline_instance_id,
		       attempt, enter_wall_ns, exit_wall_ns, dur_ns,
		       runtime_pre_ns, runtime_post_ns, status, subs_json
		FROM job_stopwatch WHERE job_id = ?
	`, "job-1")

	var (
		jobID, plugin, pipeline, stepID, pipelineInstanceID, status, subsJSON string
		attempt                                                               int
		enterNs, exitNs, durNs, preNs, postNs                                 int64
	)
	if err := row.Scan(&jobID, &plugin, &pipeline, &stepID, &pipelineInstanceID,
		&attempt, &enterNs, &exitNs, &durNs, &preNs, &postNs, &status, &subsJSON); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if jobID != "job-1" || plugin != "echo" || pipeline != "pipe-X" || stepID != "step-a" ||
		pipelineInstanceID != "inst-Y" || attempt != 1 || status != stopwatch.StatusOK {
		t.Errorf("string/int fields mismatch: %+v", []any{jobID, plugin, pipeline, stepID, pipelineInstanceID, attempt, status})
	}
	if enterNs != 1000 || exitNs != 2500 || durNs != 1500 || preNs != 100 || postNs != 50 {
		t.Errorf("timing fields mismatch: enter=%d exit=%d dur=%d pre=%d post=%d", enterNs, exitNs, durNs, preNs, postNs)
	}

	var subsBack []map[string]any
	if err := json.Unmarshal([]byte(subsJSON), &subsBack); err != nil {
		t.Fatalf("unmarshal subs_json: %v", err)
	}
	if len(subsBack) != 1 || subsBack[0]["name"] != "db_query" {
		t.Errorf("subs round-trip broken: %v", subsBack)
	}
}

func TestStoreRecordStopwatch_AppendsPerAttempt(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "sw.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO job_queue (id, plugin, command, status, submitted_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, "job-1", "echo", "poll", "succeeded", "test", "2026-05-23T00:00:00Z"); err != nil {
		t.Fatalf("seed job_queue: %v", err)
	}

	s := NewStore(db)
	base := stopwatch.Record{
		PluginID: "echo", Status: stopwatch.StatusOK, Subs: []map[string]any{},
	}
	for attempt := 1; attempt <= 3; attempt++ {
		r := base
		r.Attempt = attempt
		if err := s.RecordStopwatch(context.Background(), "job-1", r, "", ""); err != nil {
			t.Fatalf("RecordStopwatch attempt %d: %v", attempt, err)
		}
	}

	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM job_stopwatch WHERE job_id = ?`, "job-1").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 rows, got %d", n)
	}
}

func TestStoreRecordStopwatch_EmptyJobIDReturnsError(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "sw.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := NewStore(db)
	err = s.RecordStopwatch(context.Background(), "", stopwatch.Record{}, "", "")
	if err == nil {
		t.Errorf("expected error on empty jobID, got nil")
	}
}
