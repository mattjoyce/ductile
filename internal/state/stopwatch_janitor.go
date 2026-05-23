package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// stopwatchBucketKey identifies a (plugin, pipeline, step_id, status, day)
// rollup row. NULL pipeline/step_id from raw rows are coalesced to empty
// string to match the rollup table's NOT NULL DEFAULT '' columns.
type stopwatchBucketKey struct {
	plugin   string
	pipeline string
	stepID   string
	status   string
	dayUTC   string
}

// StopwatchRollupDay aggregates raw job_stopwatch rows for a single
// UTC day into the job_stopwatch_rollup_daily table, then returns the
// number of rollup rows written. Buckets that already exist (UNIQUE
// constraint) are skipped — re-running a day is a no-op for already
// rolled-up buckets.
//
// dayUTC must be a YYYY-MM-DD string in UTC. The caller is responsible
// for never rolling up the current day (in-progress).
//
// Percentiles are computed in Go from sorted dur_ns values. For the
// expected per-bucket sample size (single-digit thousands at most on
// normal workloads) this is well within budget; a SQLite window-function
// implementation would be defensible at higher volumes.
func (s *Store) StopwatchRollupDay(ctx context.Context, dayUTC string) (int, error) {
	if dayUTC == "" {
		return 0, errors.New("StopwatchRollupDay: dayUTC is empty")
	}

	// Find buckets in this day that are not already rolled up. The
	// UNIQUE on the rollup table makes "already rolled up" a discrete
	// per-bucket fact, not a per-day fact.
	const findRowsQ = `
		SELECT
			r.plugin,
			COALESCE(r.pipeline, '')             AS pipeline,
			COALESCE(r.step_id, '')              AS step_id,
			r.status,
			r.dur_ns
		FROM job_stopwatch r
		WHERE DATE(r.recorded_at) = ?
		  AND NOT EXISTS (
			SELECT 1 FROM job_stopwatch_rollup_daily x
			WHERE x.plugin   = r.plugin
			  AND x.pipeline = COALESCE(r.pipeline, '')
			  AND x.step_id  = COALESCE(r.step_id, '')
			  AND x.status   = r.status
			  AND x.day_utc  = ?
		  )
		ORDER BY r.plugin, pipeline, step_id, r.status, r.dur_ns
	`
	rows, err := s.db.QueryContext(ctx, findRowsQ, dayUTC, dayUTC)
	if err != nil {
		return 0, fmt.Errorf("StopwatchRollupDay: query: %w", err)
	}
	defer rows.Close()

	buckets := make(map[stopwatchBucketKey][]int64)
	for rows.Next() {
		var key stopwatchBucketKey
		key.dayUTC = dayUTC
		var dur int64
		if err := rows.Scan(&key.plugin, &key.pipeline, &key.stepID, &key.status, &dur); err != nil {
			return 0, fmt.Errorf("StopwatchRollupDay: scan: %w", err)
		}
		buckets[key] = append(buckets[key], dur)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("StopwatchRollupDay: iterate: %w", err)
	}
	if len(buckets) == 0 {
		return 0, nil
	}

	// Insert one rollup row per bucket inside a single transaction so
	// the rollup is all-or-nothing per day.
	const insertQ = `
		INSERT OR IGNORE INTO job_stopwatch_rollup_daily (
			plugin, pipeline, step_id, status, day_utc,
			sample_count, sum_dur_ns, min_dur_ns, max_dur_ns,
			p50_dur_ns, p90_dur_ns, p99_dur_ns, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("StopwatchRollupDay: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC().Format(time.RFC3339Nano)
	written := 0
	for key, durs := range buckets {
		// durs are already sorted asc by the SQL ORDER BY for each bucket,
		// because the bucket keys come grouped. But to be defensive
		// against any future SQL change, sort here too.
		sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
		stats := summarize(durs)
		if _, err := tx.ExecContext(
			ctx, insertQ,
			key.plugin, key.pipeline, key.stepID, key.status, key.dayUTC,
			stats.count, stats.sum, stats.min, stats.max,
			stats.p50, stats.p90, stats.p99, now,
		); err != nil {
			return 0, fmt.Errorf("StopwatchRollupDay: insert: %w", err)
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("StopwatchRollupDay: commit: %w", err)
	}
	return written, nil
}

type bucketStats struct {
	count               int
	sum, min, max       int64
	p50, p90, p99       int64
}

// summarize computes the stats fields from a sorted slice of dur_ns.
// Percentile selection uses the nearest-rank method: for percentile p in
// [0, 100], pick the element at index ceil(p * N / 100) - 1 (0-indexed,
// clamped to [0, N-1]). Simple and deterministic; no interpolation.
func summarize(sortedDurs []int64) bucketStats {
	n := len(sortedDurs)
	if n == 0 {
		return bucketStats{}
	}
	stats := bucketStats{
		count: n,
		min:   sortedDurs[0],
		max:   sortedDurs[n-1],
	}
	for _, d := range sortedDurs {
		stats.sum += d
	}
	stats.p50 = sortedDurs[percentileIndex(50, n)]
	stats.p90 = sortedDurs[percentileIndex(90, n)]
	stats.p99 = sortedDurs[percentileIndex(99, n)]
	return stats
}

func percentileIndex(p, n int) int {
	if n <= 0 {
		return 0
	}
	// ceil(p * n / 100) - 1, clamped to [0, n-1].
	idx := (p*n + 99) / 100
	idx--
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return idx
}

// StopwatchUnrolledDays returns the distinct UTC days that have raw
// job_stopwatch rows but aren't fully represented in the rollup table.
// Excludes the current UTC day (still in progress). Capped at limit to
// bound catch-up work on long-stale instances. Returns days in ascending
// order so the caller can roll up oldest-first.
func (s *Store) StopwatchUnrolledDays(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 90
	}
	const q = `
		SELECT DISTINCT DATE(recorded_at) AS day_utc
		FROM job_stopwatch
		WHERE DATE(recorded_at) < DATE('now')
		ORDER BY day_utc
		LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("StopwatchUnrolledDays: query: %w", err)
	}
	defer rows.Close()

	var days []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, fmt.Errorf("StopwatchUnrolledDays: scan: %w", err)
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

// PruneStopwatchOlderThan deletes raw job_stopwatch rows whose
// recorded_at is strictly before cutoff. Bounded by batchSize to avoid
// long write-locks; the caller may invoke repeatedly to drain a
// backlog. Returns the number of rows deleted.
func (s *Store) PruneStopwatchOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	const q = `
		DELETE FROM job_stopwatch
		WHERE id IN (
			SELECT id FROM job_stopwatch
			WHERE recorded_at < ?
			ORDER BY id
			LIMIT ?
		)
	`
	res, err := s.db.ExecContext(ctx, q, cutoff.UTC().Format(time.RFC3339Nano), batchSize)
	if err != nil {
		return 0, fmt.Errorf("PruneStopwatchOlderThan: exec: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("PruneStopwatchOlderThan: rows affected: %w", err)
	}
	return int(affected), nil
}

// JanitorHeartbeat is the last-known state of a named janitor. Used by
// the doctor check to warn when retention is silently failing.
type JanitorHeartbeat struct {
	Name         string
	LastRunAt    time.Time
	LastStatus   string
	LastError    string
	RowsRolledUp int
	RowsDeleted  int
}

// WriteJanitorHeartbeat upserts a heartbeat row. Always uses time.Now()
// at the moment of write so the heartbeat reflects when persistence
// happened, not when the work began.
func (s *Store) WriteJanitorHeartbeat(ctx context.Context, hb JanitorHeartbeat) error {
	if hb.Name == "" {
		return errors.New("WriteJanitorHeartbeat: name is empty")
	}
	const q = `
		INSERT INTO janitor_heartbeat (
			name, last_run_at, last_status, last_error, rows_rolled_up, rows_deleted
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			last_run_at    = excluded.last_run_at,
			last_status    = excluded.last_status,
			last_error     = excluded.last_error,
			rows_rolled_up = excluded.rows_rolled_up,
			rows_deleted   = excluded.rows_deleted
	`
	_, err := s.db.ExecContext(
		ctx, q,
		hb.Name,
		time.Now().UTC().Format(time.RFC3339Nano),
		hb.LastStatus,
		hb.LastError,
		hb.RowsRolledUp,
		hb.RowsDeleted,
	)
	if err != nil {
		return fmt.Errorf("WriteJanitorHeartbeat: exec: %w", err)
	}
	return nil
}

// ReadJanitorHeartbeat fetches the heartbeat row for a named janitor.
// Returns (zero, sql.ErrNoRows) if the janitor has never written a
// heartbeat.
func (s *Store) ReadJanitorHeartbeat(ctx context.Context, name string) (JanitorHeartbeat, error) {
	const q = `
		SELECT name, last_run_at, last_status, last_error, rows_rolled_up, rows_deleted
		FROM janitor_heartbeat
		WHERE name = ?
	`
	var hb JanitorHeartbeat
	var lastRunStr string
	err := s.db.QueryRowContext(ctx, q, name).Scan(
		&hb.Name, &lastRunStr, &hb.LastStatus, &hb.LastError,
		&hb.RowsRolledUp, &hb.RowsDeleted,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return JanitorHeartbeat{}, err
		}
		return JanitorHeartbeat{}, fmt.Errorf("ReadJanitorHeartbeat: scan: %w", err)
	}
	t, parseErr := time.Parse(time.RFC3339Nano, lastRunStr)
	if parseErr != nil {
		return JanitorHeartbeat{}, fmt.Errorf("ReadJanitorHeartbeat: parse last_run_at %q: %w", lastRunStr, parseErr)
	}
	hb.LastRunAt = t
	return hb, nil
}
