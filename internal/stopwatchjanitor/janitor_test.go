package stopwatchjanitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

// fakeStore is a thin in-memory Store implementation that records every
// call. Use it to assert the janitor's orchestration without coupling
// the test to the SQLite-backed state.Store. Real state.Store is also
// covered by an end-to-end test below.
type fakeStore struct {
	days            []string
	daysErr         error
	rollupErr       error
	rolledPerDay    int
	pruneErr        error
	prunePerCall    int
	hbWritten       atomic.Pointer[state.JanitorHeartbeat]
	rollupCalls     atomic.Int32
	pruneCalls      atomic.Int32
}

func (f *fakeStore) StopwatchUnrolledDays(_ context.Context, _ int) ([]string, error) {
	return f.days, f.daysErr
}

func (f *fakeStore) StopwatchRollupDay(_ context.Context, _ string) (int, error) {
	f.rollupCalls.Add(1)
	return f.rolledPerDay, f.rollupErr
}

func (f *fakeStore) PruneStopwatchOlderThan(_ context.Context, _ time.Time, _ int) (int, error) {
	f.pruneCalls.Add(1)
	if f.pruneErr != nil {
		return 0, f.pruneErr
	}
	return f.prunePerCall, nil
}

func (f *fakeStore) WriteJanitorHeartbeat(_ context.Context, hb state.JanitorHeartbeat) error {
	f.hbWritten.Store(&hb)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTick_HappyPath_WritesOkHeartbeat(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		days:         []string{"2020-01-01", "2020-01-02"},
		rolledPerDay: 5,
		prunePerCall: 100,
	}
	cfg := config.StopwatchTelemetryConfig{
		Rollup: config.StopwatchRollupConfig{Enabled: true},
	}
	j := New(store, cfg, discardLogger())

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	hb := store.hbWritten.Load()
	if hb == nil {
		t.Fatal("no heartbeat written")
	}
	if hb.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", hb.LastStatus)
	}
	if hb.RowsRolledUp != 10 {
		t.Errorf("rolled_up = %d, want 10 (2 days * 5 rows each)", hb.RowsRolledUp)
	}
}

func TestTick_RollupDisabled_SkipsRollupCalls(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		days:         []string{"2020-01-01"},
		rolledPerDay: 5,
		prunePerCall: 0,
	}
	cfg := config.StopwatchTelemetryConfig{
		Rollup: config.StopwatchRollupConfig{Enabled: false},
	}
	j := New(store, cfg, discardLogger())

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if store.rollupCalls.Load() != 0 {
		t.Errorf("expected 0 rollup calls when disabled, got %d", store.rollupCalls.Load())
	}
	if store.pruneCalls.Load() == 0 {
		t.Errorf("prune should still run when rollup is disabled")
	}
}

func TestTick_RollupErrorStillRunsPruneAndWritesErrHeartbeat(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		days:         []string{"2020-01-01"},
		rollupErr:    errors.New("rollup boom"),
		prunePerCall: 7,
	}
	cfg := config.StopwatchTelemetryConfig{
		Rollup: config.StopwatchRollupConfig{Enabled: true},
	}
	j := New(store, cfg, discardLogger())

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if store.pruneCalls.Load() == 0 {
		t.Errorf("prune should still run when rollup errors")
	}
	hb := store.hbWritten.Load()
	if hb == nil || hb.LastStatus != "err" {
		t.Errorf("heartbeat status should be err, got %+v", hb)
	}
}

func TestPrunePass_LoopsUntilDrained(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		days:         nil,
		prunePerCall: 0, // first call returns 0 -> drained immediately
	}
	cfg := config.StopwatchTelemetryConfig{}
	j := New(store, cfg, discardLogger())

	deleted, err := j.prunePass(context.Background())
	if err != nil {
		t.Fatalf("prunePass: %v", err)
	}
	if deleted != 0 {
		t.Errorf("deleted = %d, want 0", deleted)
	}
	if store.pruneCalls.Load() != 1 {
		t.Errorf("expected single prune call when drained, got %d", store.pruneCalls.Load())
	}
}

func TestPrunePass_RespectsBatchBudget(t *testing.T) {
	t.Parallel()
	// prunePerCall == batchSize -> always "full batch", janitor keeps
	// looping until MaxPruneBatchesPerTick.
	store := &fakeStore{
		prunePerCall: DefaultJanitorBatchSize,
	}
	cfg := config.StopwatchTelemetryConfig{}
	j := New(store, cfg, discardLogger())

	if _, err := j.prunePass(context.Background()); err != nil {
		t.Fatalf("prunePass: %v", err)
	}
	if got := store.pruneCalls.Load(); got != int32(MaxPruneBatchesPerTick) {
		t.Errorf("expected exactly %d prune batches at budget, got %d", MaxPruneBatchesPerTick, got)
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	t.Parallel()
	j := New(&fakeStore{}, config.StopwatchTelemetryConfig{}, discardLogger())
	wantRetention := time.Duration(DefaultRetentionDays) * 24 * time.Hour
	if j.retention != wantRetention {
		t.Errorf("retention = %s, want %s", j.retention, wantRetention)
	}
	if j.interval != DefaultJanitorInterval {
		t.Errorf("interval = %s, want %s", j.interval, DefaultJanitorInterval)
	}
	if j.batchSize != DefaultJanitorBatchSize {
		t.Errorf("batchSize = %d, want %d", j.batchSize, DefaultJanitorBatchSize)
	}
}

func TestNew_RespectsExplicitConfig(t *testing.T) {
	t.Parallel()
	cfg := config.StopwatchTelemetryConfig{
		RetentionDays: 30,
		Janitor: config.StopwatchJanitorConfig{
			Interval:  15 * time.Minute,
			BatchSize: 1000,
		},
	}
	j := New(&fakeStore{}, cfg, discardLogger())
	if j.retention != 30*24*time.Hour {
		t.Errorf("retention = %s, want 30d", j.retention)
	}
	if j.interval != 15*time.Minute {
		t.Errorf("interval = %s, want 15m", j.interval)
	}
	if j.batchSize != 1000 {
		t.Errorf("batchSize = %d, want 1000", j.batchSize)
	}
}

// TestTick_EndToEnd_AgainstRealStore exercises the janitor against a
// real SQLite-backed state.Store: seed raw rows in the past, run a
// tick, verify rollup rows appeared and raw rows were pruned.
func TestTick_EndToEnd_AgainstRealStore(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "janitor_e2e.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := state.NewStore(db)

	// Seed 5 raw rows on a day well past any retention window.
	oldDay := time.Now().UTC().AddDate(0, 0, -60) // 60d ago
	for i := int64(1); i <= 5; i++ {
		const q = `
			INSERT INTO job_stopwatch (
				job_id, plugin, pipeline, step_id, pipeline_instance_id,
				attempt, enter_wall_ns, exit_wall_ns, dur_ns,
				runtime_pre_ns, runtime_post_ns, status, subs_json, recorded_at
			) VALUES (?, 'echo', 'p', 's', 'inst', 1, 0, ?, ?, 0, 0, 'ok', '[]', ?)
		`
		ts := oldDay.Add(time.Duration(i) * time.Minute)
		if _, err := db.ExecContext(context.Background(), q,
			"j"+ts.Format("150405"), i*100, i*100, ts.Format(time.RFC3339Nano),
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cfg := config.StopwatchTelemetryConfig{
		RetentionDays: 14,
		Rollup:        config.StopwatchRollupConfig{Enabled: true},
		Janitor:       config.StopwatchJanitorConfig{BatchSize: 100},
	}
	j := New(s, cfg, discardLogger())

	if err := j.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Rollup row exists with the right count.
	var rollupCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT sample_count FROM job_stopwatch_rollup_daily WHERE plugin = 'echo'`,
	).Scan(&rollupCount); err != nil {
		t.Fatalf("rollup row missing: %v", err)
	}
	if rollupCount != 5 {
		t.Errorf("rollup sample_count = %d, want 5", rollupCount)
	}

	// Raw rows pruned (60d ago < 14d retention cutoff).
	var rawCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM job_stopwatch`,
	).Scan(&rawCount); err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if rawCount != 0 {
		t.Errorf("raw rows still present after prune: %d", rawCount)
	}

	// Heartbeat written.
	hb, err := s.ReadJanitorHeartbeat(context.Background(), janitorName)
	if err != nil {
		t.Fatalf("ReadJanitorHeartbeat: %v", err)
	}
	if hb.LastStatus != "ok" {
		t.Errorf("heartbeat status = %q, want ok", hb.LastStatus)
	}
	if hb.RowsRolledUp != 1 || hb.RowsDeleted != 5 {
		t.Errorf("heartbeat counts = rolled_up=%d deleted=%d, want 1/5", hb.RowsRolledUp, hb.RowsDeleted)
	}
}
