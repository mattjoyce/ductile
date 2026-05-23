package state

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/storage"
)

func newSWStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "swj.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

// seedRaw inserts one job_stopwatch row directly (bypassing the regular
// dispatcher path) for tests that need to control recorded_at and
// dur_ns precisely.
func seedRaw(t *testing.T, s *Store, plugin, pipeline, stepID, status string, durNs int64, recordedAt time.Time) {
	t.Helper()
	const q = `
		INSERT INTO job_stopwatch (
			job_id, plugin, pipeline, step_id, pipeline_instance_id,
			attempt, enter_wall_ns, exit_wall_ns, dur_ns,
			runtime_pre_ns, runtime_post_ns, status, subs_json, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	jobID := plugin + "-" + recordedAt.Format("150405.000000000")
	if _, err := s.db.ExecContext(context.Background(), q,
		jobID, plugin, pipeline, stepID, "inst",
		1, recordedAt.UnixNano(), recordedAt.UnixNano()+durNs, durNs,
		0, 0, status, "[]",
		recordedAt.UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatalf("seed raw: %v", err)
	}
}

func TestStopwatchRollupDay_ComputesPercentiles(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	// Ten samples for one bucket: 100, 200, ..., 1000 ns. Sorted.
	day := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	for i := int64(1); i <= 10; i++ {
		seedRaw(t, s, "echo", "p", "s", "ok", i*100, day.Add(time.Duration(i)*time.Minute))
	}

	written, err := s.StopwatchRollupDay(context.Background(), "2026-05-23")
	if err != nil {
		t.Fatalf("StopwatchRollupDay: %v", err)
	}
	if written != 1 {
		t.Fatalf("expected 1 rollup row, got %d", written)
	}

	row := s.db.QueryRowContext(context.Background(), `
		SELECT sample_count, sum_dur_ns, min_dur_ns, max_dur_ns,
		       p50_dur_ns, p90_dur_ns, p99_dur_ns
		FROM job_stopwatch_rollup_daily
		WHERE plugin = ? AND pipeline = ? AND step_id = ? AND status = ? AND day_utc = ?
	`, "echo", "p", "s", "ok", "2026-05-23")
	var count, sum, minD, maxD, p50, p90, p99 int64
	if err := row.Scan(&count, &sum, &minD, &maxD, &p50, &p90, &p99); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}
	if sum != 5500 {
		t.Errorf("sum = %d, want 5500", sum)
	}
	if minD != 100 || maxD != 1000 {
		t.Errorf("min/max = %d/%d, want 100/1000", minD, maxD)
	}
	// Nearest-rank percentiles for n=10:
	//   p50: ceil(50*10/100)-1 = 4 → sortedDurs[4] = 500
	//   p90: ceil(90*10/100)-1 = 8 → sortedDurs[8] = 900
	//   p99: ceil(99*10/100)-1 = 9 → sortedDurs[9] = 1000
	if p50 != 500 || p90 != 900 || p99 != 1000 {
		t.Errorf("p50/p90/p99 = %d/%d/%d, want 500/900/1000", p50, p90, p99)
	}
}

func TestStopwatchRollupDay_IdempotentForExistingBuckets(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	day := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	seedRaw(t, s, "echo", "p", "s", "ok", 100, day)

	for i := 0; i < 3; i++ {
		if _, err := s.StopwatchRollupDay(context.Background(), "2026-05-23"); err != nil {
			t.Fatalf("rollup pass %d: %v", i, err)
		}
	}

	var rollupCount int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM job_stopwatch_rollup_daily WHERE day_utc = ?`, "2026-05-23",
	).Scan(&rollupCount); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rollupCount != 1 {
		t.Errorf("expected 1 rollup row after 3 passes, got %d", rollupCount)
	}
}

func TestStopwatchRollupDay_SeparatesBucketsByEveryKeyDim(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	day := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	// Same plugin but different status -> distinct buckets.
	seedRaw(t, s, "echo", "p", "s", "ok", 100, day)
	seedRaw(t, s, "echo", "p", "s", "err", 200, day)
	// Different plugin -> distinct bucket.
	seedRaw(t, s, "other", "p", "s", "ok", 300, day)
	// Empty pipeline (one-off trigger) -> distinct bucket.
	seedRaw(t, s, "echo", "", "", "ok", 400, day)

	written, err := s.StopwatchRollupDay(context.Background(), "2026-05-23")
	if err != nil {
		t.Fatalf("StopwatchRollupDay: %v", err)
	}
	if written != 4 {
		t.Errorf("expected 4 buckets, got %d", written)
	}
}

func TestStopwatchUnrolledDays_ExcludesToday(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	// Seed two past days and one row "now" (today).
	for _, d := range []time.Time{
		time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2021, 6, 15, 12, 0, 0, 0, time.UTC),
	} {
		seedRaw(t, s, "echo", "p", "s", "ok", 100, d)
	}
	seedRaw(t, s, "echo", "p", "s", "ok", 100, time.Now().UTC())

	days, err := s.StopwatchUnrolledDays(context.Background(), 0)
	if err != nil {
		t.Fatalf("StopwatchUnrolledDays: %v", err)
	}
	if len(days) != 2 {
		t.Errorf("expected 2 past days, got %v", days)
	}
	if days[0] != "2020-01-01" || days[1] != "2021-06-15" {
		t.Errorf("days not sorted ascending: %v", days)
	}
	if got := days[len(days)-1]; got == time.Now().UTC().Format("2006-01-02") {
		t.Errorf("today should NOT appear in unrolled days: %v", days)
	}
}

func TestPruneStopwatchOlderThan_BoundedByBatchSize(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	day := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 15; i++ {
		seedRaw(t, s, "echo", "p", "s", "ok", 100, day.Add(time.Duration(i)*time.Second))
	}

	cutoff := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	deleted, err := s.PruneStopwatchOlderThan(context.Background(), cutoff, 10)
	if err != nil {
		t.Fatalf("PruneStopwatchOlderThan: %v", err)
	}
	if deleted != 10 {
		t.Errorf("expected 10 deleted by batch cap, got %d", deleted)
	}

	deleted, err = s.PruneStopwatchOlderThan(context.Background(), cutoff, 10)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if deleted != 5 {
		t.Errorf("expected 5 deleted on second pass, got %d", deleted)
	}
}

func TestPruneStopwatchOlderThan_DoesNotTouchRecentRows(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	seedRaw(t, s, "echo", "p", "s", "ok", 100, time.Now().UTC())
	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	deleted, err := s.PruneStopwatchOlderThan(context.Background(), cutoff, 100)
	if err != nil {
		t.Fatalf("PruneStopwatchOlderThan: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deletions, got %d", deleted)
	}
}

func TestJanitorHeartbeat_UpsertAndRead(t *testing.T) {
	t.Parallel()
	s := newSWStore(t)

	// Read before any write -> ErrNoRows.
	if _, err := s.ReadJanitorHeartbeat(context.Background(), "stopwatch"); err == nil {
		t.Fatal("expected error reading non-existent heartbeat")
	} else if !errors.Is(err, sql.ErrNoRows) {
		// sql.ErrNoRows is what the implementation returns; if a future
		// refactor wraps it, this test should fail loudly.
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}

	if err := s.WriteJanitorHeartbeat(context.Background(), JanitorHeartbeat{
		Name:         "stopwatch",
		LastStatus:   "ok",
		LastError:    "",
		RowsRolledUp: 12,
		RowsDeleted:  450,
	}); err != nil {
		t.Fatalf("WriteJanitorHeartbeat: %v", err)
	}

	hb, err := s.ReadJanitorHeartbeat(context.Background(), "stopwatch")
	if err != nil {
		t.Fatalf("ReadJanitorHeartbeat: %v", err)
	}
	if hb.LastStatus != "ok" || hb.RowsRolledUp != 12 || hb.RowsDeleted != 450 {
		t.Errorf("heartbeat fields mismatch: %+v", hb)
	}
	if time.Since(hb.LastRunAt) > 5*time.Second {
		t.Errorf("last_run_at not recent: %v", hb.LastRunAt)
	}

	// Upsert overwrites.
	if err := s.WriteJanitorHeartbeat(context.Background(), JanitorHeartbeat{
		Name:         "stopwatch",
		LastStatus:   "err",
		LastError:    "boom",
		RowsRolledUp: 0,
		RowsDeleted:  0,
	}); err != nil {
		t.Fatalf("WriteJanitorHeartbeat (upsert): %v", err)
	}
	hb, err = s.ReadJanitorHeartbeat(context.Background(), "stopwatch")
	if err != nil {
		t.Fatalf("ReadJanitorHeartbeat after upsert: %v", err)
	}
	if hb.LastStatus != "err" || hb.LastError != "boom" {
		t.Errorf("upsert did not overwrite: %+v", hb)
	}
}
