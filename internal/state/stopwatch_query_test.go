package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/stopwatch"
	"github.com/mattjoyce/ductile/internal/storage"
)

func TestStopwatchRowsForPlugin_FiltersAndOrders(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "sw.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	// Seed job_queue rows (defensive: not FK-enforced today but matches
	// the convention in stopwatch_test.go).
	for _, id := range []string{"j-old", "j-fresh-1", "j-fresh-2", "j-other-plugin"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO job_queue (id, plugin, command, status, submitted_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, id, "p", "poll", "succeeded", "test", "2026-05-23T00:00:00Z"); err != nil {
			t.Fatalf("seed job_queue %s: %v", id, err)
		}
	}

	s := NewStore(db)

	// Three rows for plugin "withings"; one is stale relative to the
	// since-cutoff. One row for plugin "other" must NOT appear.
	type seed struct {
		jobID  string
		plugin string
		step   string
		offset time.Duration // relative to "now"
	}
	now := time.Now().UTC()
	seeds := []seed{
		{"j-old", "withings", "poll", -2 * time.Hour},
		{"j-fresh-1", "withings", "poll", -30 * time.Minute},
		{"j-fresh-2", "withings", "poll", -10 * time.Minute},
		{"j-other-plugin", "other", "poll", -10 * time.Minute},
	}

	for i, sd := range seeds {
		rec := stopwatch.Record{
			PluginID:      sd.plugin,
			StepName:      sd.step,
			Attempt:       1,
			EnterWallNs:   0,
			ExitWallNs:    0,
			DurNs:         int64(i+1) * 1_000_000, // 1ms, 2ms, 3ms, 4ms
			RuntimePreNs:  0,
			RuntimePostNs: 0,
			Status:        stopwatch.StatusOK,
			Subs:          nil,
		}
		if err := s.RecordStopwatch(ctx, sd.jobID, rec, "", ""); err != nil {
			t.Fatalf("RecordStopwatch %s: %v", sd.jobID, err)
		}
		// Overwrite recorded_at to the simulated offset; RecordStopwatch
		// uses time.Now() which would put everything in the same instant.
		stamp := now.Add(sd.offset).Format(time.RFC3339Nano)
		if _, err := db.ExecContext(ctx,
			`UPDATE job_stopwatch SET recorded_at = ? WHERE job_id = ?`,
			stamp, sd.jobID,
		); err != nil {
			t.Fatalf("rewrite recorded_at for %s: %v", sd.jobID, err)
		}
	}

	// Query withings rows since 1 hour ago — should return only the two
	// fresh withings rows, ordered by recorded_at ascending.
	since := now.Add(-time.Hour)
	got, err := s.StopwatchRowsForPlugin(ctx, "withings", since)
	if err != nil {
		t.Fatalf("StopwatchRowsForPlugin: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (excludes stale + other plugin); got=%+v", len(got), got)
	}

	// Ordering: -30m before -10m.
	if !got[0].RecordedAt.Before(got[1].RecordedAt) {
		t.Errorf("rows not ordered ascending by recorded_at: %v then %v", got[0].RecordedAt, got[1].RecordedAt)
	}

	// Other-plugin rows must not leak.
	for _, r := range got {
		if r.StepID != "poll" {
			t.Errorf("unexpected step_id %q", r.StepID)
		}
	}

	// Querying a plugin with no rows in the window returns empty, not error.
	none, err := s.StopwatchRowsForPlugin(ctx, "withings", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("StopwatchRowsForPlugin (empty window): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("expected zero rows in empty window, got %d", len(none))
	}
}
