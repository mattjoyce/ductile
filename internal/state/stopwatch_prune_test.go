package state

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/storage"
)

func newPruneStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "prune.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

// seedRow inserts a job_stopwatch row directly (bypassing
// RecordStopwatch) so the test controls recorded_at and subs_json
// shape precisely.
func seedRow(t *testing.T, s *Store, plugin, stepID, status string, recordedAt time.Time, subs []map[string]any) {
	t.Helper()
	subsJSON := "[]"
	if subs != nil {
		b, err := json.Marshal(subs)
		if err != nil {
			t.Fatalf("marshal subs: %v", err)
		}
		subsJSON = string(b)
	}
	const q = `
		INSERT INTO job_stopwatch (
			job_id, plugin, pipeline, step_id, pipeline_instance_id,
			attempt, enter_wall_ns, exit_wall_ns, dur_ns,
			runtime_pre_ns, runtime_post_ns, status, subs_json, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	jobID := plugin + "-" + recordedAt.Format("150405.000000000")
	if _, err := s.db.ExecContext(context.Background(), q,
		jobID, plugin, "p", stepID, "inst",
		1, recordedAt.UnixNano(), recordedAt.UnixNano()+100, 100,
		0, 0, status, subsJSON,
		recordedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func rowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM job_stopwatch`,
	).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func subsForJobID(t *testing.T, s *Store, jobID string) []map[string]any {
	t.Helper()
	var raw string
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT subs_json FROM job_stopwatch WHERE job_id = ?`, jobID,
	).Scan(&raw); err != nil {
		t.Fatalf("read subs_json: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal subs_json: %v", err)
	}
	return out
}

// --- PruneStopwatchRows ---

func TestPruneStopwatchRows_FilterByOlderThanOnly(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Now().UTC()

	seedRow(t, s, "echo", "s", "ok", old, nil)
	seedRow(t, s, "echo", "s", "ok", recent, nil)

	deleted, err := s.PruneStopwatchRows(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC().Add(-1 * time.Hour)},
		100,
	)
	if err != nil {
		t.Fatalf("PruneStopwatchRows: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if rowCount(t, s) != 1 {
		t.Errorf("rows remaining = %d, want 1 (the recent row)", rowCount(t, s))
	}
}

func TestPruneStopwatchRows_FilterByPluginAndStep(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	seedRow(t, s, "echo", "s1", "ok", old, nil)
	seedRow(t, s, "echo", "s2", "ok", old.Add(time.Second), nil)
	seedRow(t, s, "other", "s1", "ok", old.Add(2*time.Second), nil)

	// Plugin=echo + Step=s1 → only one of three.
	deleted, err := s.PruneStopwatchRows(context.Background(),
		StopwatchPruneFilter{
			OlderThan: time.Now().UTC(),
			Plugin:    "echo",
			StepID:    "s1",
		},
		100,
	)
	if err != nil {
		t.Fatalf("PruneStopwatchRows: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if rowCount(t, s) != 2 {
		t.Errorf("rows remaining = %d, want 2", rowCount(t, s))
	}
}

func TestPruneStopwatchRows_FilterByStatus(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	seedRow(t, s, "echo", "s", "ok", old, nil)
	seedRow(t, s, "echo", "s", "err", old.Add(time.Second), nil)
	seedRow(t, s, "echo", "s", "timeout", old.Add(2*time.Second), nil)

	deleted, err := s.PruneStopwatchRows(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC(), Status: "err"},
		100,
	)
	if err != nil {
		t.Fatalf("PruneStopwatchRows: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
}

func TestPruneStopwatchRows_RespectsLimit(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		seedRow(t, s, "echo", "s", "ok", old.Add(time.Duration(i)*time.Second), nil)
	}

	deleted, err := s.PruneStopwatchRows(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC()},
		7,
	)
	if err != nil {
		t.Fatalf("PruneStopwatchRows: %v", err)
	}
	if deleted != 7 {
		t.Errorf("deleted = %d, want 7", deleted)
	}
	if rowCount(t, s) != 13 {
		t.Errorf("remaining = %d, want 13", rowCount(t, s))
	}
}

// --- CountStopwatchRowsMatching ---

func TestCountStopwatchRowsMatching_AgreesWithPrune(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		seedRow(t, s, "echo", "s", "ok", old.Add(time.Duration(i)*time.Second), nil)
	}
	for i := 0; i < 3; i++ {
		seedRow(t, s, "other", "s", "ok", old.Add(time.Duration(i)*time.Second), nil)
	}

	filter := StopwatchPruneFilter{OlderThan: time.Now().UTC(), Plugin: "echo"}
	count, err := s.CountStopwatchRowsMatching(context.Background(), filter)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 5 {
		t.Errorf("count = %d, want 5", count)
	}

	// Dry-run-equivalent: count then prune should agree.
	deleted, _ := s.PruneStopwatchRows(context.Background(), filter, 100)
	if deleted != count {
		t.Errorf("prune deleted %d but count returned %d", deleted, count)
	}
}

// --- ClearStopwatchSubs ---

func TestClearStopwatchSubs_NoSpanNameClearsAllSubs(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	subs := []map[string]any{
		{"name": "fetch.http_get", "dur_ns": 100},
		{"name": "fetch.body_read", "dur_ns": 50},
	}
	seedRow(t, s, "fetch", "s", "ok", old, subs)
	seedRow(t, s, "fetch", "s", "ok", old.Add(time.Second), nil) // already empty subs

	touched, err := s.ClearStopwatchSubs(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC()},
		"", 100,
	)
	if err != nil {
		t.Fatalf("ClearStopwatchSubs: %v", err)
	}
	if touched != 1 {
		t.Errorf("touched = %d, want 1 (only the row that had subs)", touched)
	}

	// Row still exists with cleared subs.
	if rowCount(t, s) != 2 {
		t.Errorf("rows = %d, want 2 (clear must not delete rows)", rowCount(t, s))
	}

	got := subsForJobID(t, s, "fetch-"+old.Format("150405.000000000"))
	if len(got) != 0 {
		t.Errorf("subs not cleared: %v", got)
	}
}

func TestClearStopwatchSubs_BySpanNameRemovesOnlyThatSpan(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	subs := []map[string]any{
		{"name": "fetch.http_get", "dur_ns": 100},
		{"name": "fetch.body_read", "dur_ns": 50},
		{"name": "fetch.decode", "dur_ns": 20},
	}
	seedRow(t, s, "fetch", "s", "ok", old, subs)

	touched, err := s.ClearStopwatchSubs(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC()},
		"fetch.body_read", 100,
	)
	if err != nil {
		t.Fatalf("ClearStopwatchSubs: %v", err)
	}
	if touched != 1 {
		t.Errorf("touched = %d, want 1", touched)
	}

	got := subsForJobID(t, s, "fetch-"+old.Format("150405.000000000"))
	if len(got) != 2 {
		t.Fatalf("expected 2 remaining sub-spans, got %d: %v", len(got), got)
	}
	for _, sub := range got {
		if sub["name"] == "fetch.body_read" {
			t.Errorf("body_read still present after clear: %v", sub)
		}
	}
}

func TestClearStopwatchSubs_BySpanName_SkipsRowsWithoutThatSpan(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	withSpan := []map[string]any{
		{"name": "fetch.body_read", "dur_ns": 50},
	}
	withoutSpan := []map[string]any{
		{"name": "fetch.http_get", "dur_ns": 100},
	}
	seedRow(t, s, "fetch", "s", "ok", old, withSpan)
	seedRow(t, s, "fetch", "s", "ok", old.Add(time.Second), withoutSpan)
	seedRow(t, s, "fetch", "s", "ok", old.Add(2*time.Second), nil)

	touched, err := s.ClearStopwatchSubs(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC()},
		"fetch.body_read", 100,
	)
	if err != nil {
		t.Fatalf("ClearStopwatchSubs: %v", err)
	}
	// Only one row actually contained "fetch.body_read".
	if touched != 1 {
		t.Errorf("touched = %d, want 1 (only the row that had body_read)", touched)
	}
}

func TestClearStopwatchSubs_PreservesParentRowFields(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	seedRow(t, s, "fetch", "s1", "ok", old, []map[string]any{{"name": "x", "dur_ns": 1}})

	_, err := s.ClearStopwatchSubs(context.Background(),
		StopwatchPruneFilter{OlderThan: time.Now().UTC()}, "", 100,
	)
	if err != nil {
		t.Fatalf("ClearStopwatchSubs: %v", err)
	}

	// Row's supervisor fields untouched.
	var plugin, stepID, status string
	var durNs int64
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT plugin, step_id, status, dur_ns FROM job_stopwatch`,
	).Scan(&plugin, &stepID, &status, &durNs); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if plugin != "fetch" || stepID != "s1" || status != "ok" || durNs != 100 {
		t.Errorf("supervisor fields mutated: plugin=%q step=%q status=%q dur=%d",
			plugin, stepID, status, durNs)
	}
}

func TestStopwatchSnapshot_EmptyTable(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	count, oldest, err := s.StopwatchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("StopwatchSnapshot: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
	if !oldest.IsZero() {
		t.Errorf("oldest = %v, want zero", oldest)
	}
}

func TestStopwatchSnapshot_PopulatedTable(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	mid := time.Date(2022, 6, 15, 12, 0, 0, 0, time.UTC)
	recent := time.Date(2024, 12, 31, 23, 59, 59, 0, time.UTC)
	for _, ts := range []time.Time{recent, old, mid} { // intentional unsorted insert order
		seedRow(t, s, "echo", "s", "ok", ts, nil)
	}

	count, oldest, err := s.StopwatchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("StopwatchSnapshot: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if !oldest.Equal(old) {
		t.Errorf("oldest = %v, want %v", oldest, old)
	}
}

func TestCountStopwatchSubsMatching_AgreesWithClear(t *testing.T) {
	t.Parallel()
	s := newPruneStore(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	withBody := []map[string]any{{"name": "fetch.body_read", "dur_ns": 50}}
	withoutBody := []map[string]any{{"name": "fetch.http_get", "dur_ns": 100}}
	for i := 0; i < 4; i++ {
		seedRow(t, s, "fetch", "s", "ok", old.Add(time.Duration(i)*time.Second), withBody)
	}
	for i := 0; i < 2; i++ {
		seedRow(t, s, "fetch", "s", "ok", old.Add(time.Duration(10+i)*time.Second), withoutBody)
	}

	filter := StopwatchPruneFilter{OlderThan: time.Now().UTC(), Plugin: "fetch"}
	count, err := s.CountStopwatchSubsMatching(context.Background(), filter, "fetch.body_read")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}

	touched, _ := s.ClearStopwatchSubs(context.Background(), filter, "fetch.body_read", 100)
	if touched != count {
		t.Errorf("clear touched %d but count returned %d", touched, count)
	}
}
