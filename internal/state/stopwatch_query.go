package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// StopwatchAggregationRow is one job_stopwatch row projected for
// downstream percentile aggregation. SubsJSON is the raw JSON column;
// callers parse it only when sub-span breakdown is requested AND the
// principal scope permits exposing plugin-supplied content (the
// HAZARD documented on RecordStopwatch).
type StopwatchAggregationRow struct {
	StepID     string
	DurNs      int64
	RecordedAt time.Time
	SubsJSON   []byte
}

// StopwatchRowsForPlugin returns job_stopwatch rows for one plugin
// whose recorded_at is at or after since, ordered by recorded_at.
// Used by the /stopwatch/{plugin} API to compute p50/p95/p99 over a
// rolling window.
//
// Aggregation happens in the caller (the API handler), not here — this
// keeps the SQL boundary thin and the percentile math testable without
// a database.
func (s *Store) StopwatchRowsForPlugin(ctx context.Context, plugin string, since time.Time) ([]StopwatchAggregationRow, error) {
	const q = `
		SELECT step_id, dur_ns, recorded_at, subs_json
		FROM job_stopwatch
		WHERE plugin = ?
		  AND recorded_at >= ?
		ORDER BY recorded_at ASC
	`
	rows, err := s.db.QueryContext(ctx, q, plugin, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("StopwatchRowsForPlugin: query: %w", err)
	}
	defer rows.Close()

	var out []StopwatchAggregationRow
	for rows.Next() {
		var (
			stepID     sql.NullString
			durNs      int64
			recordedAt string
			subsJSON   []byte
		)
		if err := rows.Scan(&stepID, &durNs, &recordedAt, &subsJSON); err != nil {
			return nil, fmt.Errorf("StopwatchRowsForPlugin: scan: %w", err)
		}
		ts, err := time.Parse(time.RFC3339Nano, recordedAt)
		if err != nil {
			// Drop malformed rows rather than fail the whole query; the
			// gateway always writes RFC3339Nano so a bad row implies
			// external tampering. Log via the caller (return continues).
			continue
		}
		out = append(out, StopwatchAggregationRow{
			StepID:     stepID.String,
			DurNs:      durNs,
			RecordedAt: ts,
			SubsJSON:   subsJSON,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("StopwatchRowsForPlugin: rows: %w", err)
	}
	return out, nil
}
