package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StopwatchPruneFilter narrows which job_stopwatch rows a prune
// operation touches. OlderThan is required (rows recorded_at < cutoff);
// Plugin / StepID / Status are optional WHERE clauses (empty string
// means "any").
//
// Both PruneStopwatchRows (1st-class — drops whole rows) and
// ClearStopwatchSubs (2nd-class — clears the subs_json field, keeps
// the row + supervisor timing) share this filter.
type StopwatchPruneFilter struct {
	OlderThan time.Time
	Plugin    string
	StepID    string
	Status    string
}

// whereClause builds the SQL WHERE fragment and the argument list. All
// callers prepend "WHERE recorded_at < ?" implicitly via this method.
// Returns the trailing AND-joined clauses and the args in order.
func (f StopwatchPruneFilter) whereClause() (string, []any) {
	parts := []string{"recorded_at < ?"}
	args := []any{f.OlderThan.UTC().Format(time.RFC3339Nano)}
	if f.Plugin != "" {
		parts = append(parts, "plugin = ?")
		args = append(args, f.Plugin)
	}
	if f.StepID != "" {
		parts = append(parts, "step_id = ?")
		args = append(args, f.StepID)
	}
	if f.Status != "" {
		parts = append(parts, "status = ?")
		args = append(args, f.Status)
	}
	return strings.Join(parts, " AND "), args
}

// CountStopwatchRowsMatching returns how many job_stopwatch rows match
// the filter without modifying anything. Use for --dry-run.
func (s *Store) CountStopwatchRowsMatching(ctx context.Context, filter StopwatchPruneFilter) (int, error) {
	where, args := filter.whereClause()
	q := "SELECT COUNT(*) FROM job_stopwatch WHERE " + where
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("CountStopwatchRowsMatching: %w", err)
	}
	return n, nil
}

// PruneStopwatchRows deletes job_stopwatch rows matching the filter,
// up to limit per call. Returns the number of rows deleted. Bounded by
// limit so the writer lock doesn't stall the dispatcher; the operator
// re-invokes to drain backlogs.
func (s *Store) PruneStopwatchRows(ctx context.Context, filter StopwatchPruneFilter, limit int) (int, error) {
	if limit <= 0 {
		limit = 5000
	}
	where, args := filter.whereClause()
	// Two-step pattern (subquery selects ids, outer DELETE bounds the
	// row count) is portable across SQLite versions whose UPDATE/DELETE
	// LIMIT support varies by build flag.
	q := fmt.Sprintf(`
		DELETE FROM job_stopwatch
		WHERE id IN (
			SELECT id FROM job_stopwatch
			WHERE %s
			ORDER BY id
			LIMIT ?
		)
	`, where)
	args = append(args, limit)
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("PruneStopwatchRows: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("PruneStopwatchRows: rows affected: %w", err)
	}
	return int(n), nil
}

// StopwatchSnapshot is a cheap "how full is the ledger" probe used by
// `ductile config check` to surface unbounded growth. Returns the row
// count and the oldest recorded_at (zero when the table is empty).
func (s *Store) StopwatchSnapshot(ctx context.Context) (rowCount int, oldestRecordedAt time.Time, err error) {
	const q = `SELECT COUNT(*), MIN(recorded_at) FROM job_stopwatch`
	var oldestRaw sql.NullString
	if err := s.db.QueryRowContext(ctx, q).Scan(&rowCount, &oldestRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, fmt.Errorf("StopwatchSnapshot: %w", err)
	}
	if !oldestRaw.Valid || oldestRaw.String == "" {
		return rowCount, time.Time{}, nil
	}
	t, parseErr := time.Parse(time.RFC3339Nano, oldestRaw.String)
	if parseErr != nil {
		return rowCount, time.Time{}, fmt.Errorf("StopwatchSnapshot: parse oldest %q: %w", oldestRaw.String, parseErr)
	}
	return rowCount, t, nil
}

// CountStopwatchSubsMatching returns how many job_stopwatch rows match
// the filter AND contain at least one sub-span that would be cleared.
// When spanName is empty, "would be cleared" means "has any sub-span"
// (i.e. subs_json is not '[]' and not empty). When spanName is set,
// only rows containing a sub-span with that name are counted. Use for
// --dry-run.
func (s *Store) CountStopwatchSubsMatching(ctx context.Context, filter StopwatchPruneFilter, spanName string) (int, error) {
	where, args := filter.whereClause()
	var subPredicate string
	if spanName == "" {
		subPredicate = "subs_json IS NOT NULL AND subs_json != '[]' AND subs_json != ''"
	} else {
		subPredicate = "EXISTS (SELECT 1 FROM json_each(subs_json) WHERE json_extract(value, '$.name') = ?)"
		args = append(args, spanName)
	}
	q := fmt.Sprintf("SELECT COUNT(*) FROM job_stopwatch WHERE %s AND %s", where, subPredicate)
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("CountStopwatchSubsMatching: %w", err)
	}
	return n, nil
}

// ClearStopwatchSubs rewrites subs_json on rows matching the filter
// without touching any other field. The supervisor's 1st-class timing
// (dur_ns, status, recorded_at, etc.) is preserved -- only the
// plugin-supplied 2nd-class advisory data is cleared.
//
// When spanName is empty, subs_json becomes '[]' on every matching row
// that currently has any sub-spans. When spanName is set, only entries
// whose "name" field equals spanName are removed (the rest stay), and
// only rows that actually contain such an entry are touched.
//
// Bounded by limit; operator re-invokes for backlogs.
func (s *Store) ClearStopwatchSubs(ctx context.Context, filter StopwatchPruneFilter, spanName string, limit int) (int, error) {
	if limit <= 0 {
		limit = 5000
	}
	where, args := filter.whereClause()

	var q string
	if spanName == "" {
		q = fmt.Sprintf(`
			UPDATE job_stopwatch
			SET subs_json = '[]'
			WHERE id IN (
				SELECT id FROM job_stopwatch
				WHERE %s
				  AND subs_json IS NOT NULL
				  AND subs_json != '[]'
				  AND subs_json != ''
				ORDER BY id
				LIMIT ?
			)
		`, where)
		args = append(args, limit)
	} else {
		// Two uses of spanName: the EXISTS predicate (so we don't
		// rewrite rows that don't contain the name) and the
		// filtering inside json_each (the actual rewrite). Order of
		// args matches the parameter order: filter args, then limit,
		// then spanName-for-WHERE, then spanName-for-SELECT.
		//
		// SQLite-compatible: modernc.org/sqlite supports json_each
		// and json_group_array (SQLite >= 3.38).
		q = fmt.Sprintf(`
			UPDATE job_stopwatch
			SET subs_json = COALESCE(
				(SELECT json_group_array(value)
				 FROM json_each(job_stopwatch.subs_json)
				 WHERE json_extract(value, '$.name') != ?),
				'[]'
			)
			WHERE id IN (
				SELECT id FROM job_stopwatch
				WHERE %s
				  AND EXISTS (
					SELECT 1 FROM json_each(subs_json)
					WHERE json_extract(value, '$.name') = ?
				  )
				ORDER BY id
				LIMIT ?
			)
		`, where)
		// arg order: spanName (UPDATE SET), filter args, spanName (WHERE EXISTS), limit
		args = append([]any{spanName}, append(args, spanName, limit)...)
	}

	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("ClearStopwatchSubs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("ClearStopwatchSubs: rows affected: %w", err)
	}
	return int(n), nil
}
