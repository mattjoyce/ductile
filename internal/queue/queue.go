package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mattjoyce/ductile/internal/state"
)

const maxStderrBytes = 64 * 1024

type Queue struct {
	db                       *sql.DB
	logger                   *slog.Logger
	dedupeTTL                time.Duration
	configSnapshotIDProvider func() string
}

type Option func(*Queue)

func WithLogger(logger *slog.Logger) Option {
	return func(q *Queue) {
		if logger != nil {
			q.logger = logger
		}
	}
}

func WithDedupeTTL(ttl time.Duration) Option {
	return func(q *Queue) {
		if ttl > 0 {
			q.dedupeTTL = ttl
		}
	}
}

func WithConfigSnapshotIDProvider(provider func() string) Option {
	return func(q *Queue) {
		q.configSnapshotIDProvider = provider
	}
}

func New(db *sql.DB, opts ...Option) *Queue {
	q := &Queue{
		db:        db,
		logger:    slog.Default(),
		dedupeTTL: 24 * time.Hour,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(q)
		}
	}
	return q
}

func (q *Queue) GetDB() *sql.DB {
	return q.db
}

func (q *Queue) recordJobTransition(ctx context.Context, tx *sql.Tx, jobID string, fromStatus *Status, toStatus Status, reason *string, createdAt string) error {
	var from any
	if fromStatus != nil {
		from = *fromStatus
	}
	var reasonVal any
	if reason != nil && strings.TrimSpace(*reason) != "" {
		reasonVal = *reason
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO job_transitions(job_id, from_status, to_status, reason, created_at)
VALUES(?, ?, ?, ?, ?);
`, jobID, from, toStatus, reasonVal, createdAt); err != nil {
		return fmt.Errorf("record job transition: %w", err)
	}
	return nil
}

func (q *Queue) recordJobAttempt(ctx context.Context, tx *sql.Tx, jobID string, attempt int, createdAt string) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO job_attempts(job_id, attempt, created_at)
VALUES(?, ?, ?);
`, jobID, attempt, createdAt); err != nil {
		return fmt.Errorf("record job attempt: %w", err)
	}
	return nil
}

// Depth returns the number of outstanding jobs (queued or running).
// Used by /healthz for quick operational visibility.
func (q *Queue) Depth(ctx context.Context) (int, error) {
	row := q.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM job_queue
WHERE status IN (?, ?);
`, StatusQueued, StatusRunning)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("queue depth: %w", err)
	}
	return n, nil
}

// Metrics returns high-frequency state metrics for the queue.
func (q *Queue) Metrics(ctx context.Context) (QueueMetrics, error) {
	var m QueueMetrics
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Queue Depth (Queued)
	err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_queue WHERE status = ?", StatusQueued).Scan(&m.QueueDepth)
	if err != nil {
		return m, fmt.Errorf("metrics depth: %w", err)
	}

	// Running Count
	err = q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_queue WHERE status = ?", StatusRunning).Scan(&m.RunningCount)
	if err != nil {
		return m, fmt.Errorf("metrics running: %w", err)
	}

	// Delayed Count (Queued with next_retry_at > now)
	err = q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_queue WHERE status = ? AND next_retry_at > ?", StatusQueued, now).Scan(&m.DelayedCount)
	if err != nil {
		return m, fmt.Errorf("metrics delayed: %w", err)
	}

	// Dead Count
	err = q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM job_queue WHERE status = ?", StatusDead).Scan(&m.DeadCount)
	if err != nil {
		return m, fmt.Errorf("metrics dead: %w", err)
	}

	// Active Jobs
	rows, err := q.db.QueryContext(ctx, "SELECT id, plugin, command, started_at FROM job_queue WHERE status = ? ORDER BY started_at ASC", StatusRunning)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var aj ActiveJob
			var startedAtS sql.NullString
			if err := rows.Scan(&aj.JobID, &aj.Plugin, &aj.Command, &startedAtS); err == nil {
				if startedAtS.Valid {
					if t, err := time.Parse(time.RFC3339Nano, startedAtS.String); err == nil {
						aj.StartedAt = t
					}
				}
				m.ActiveJobs = append(m.ActiveJobs, aj)
			}
		}
	}

	// Plugin Lanes (Saturation)
	rows, err = q.db.QueryContext(ctx, "SELECT plugin, COUNT(*) FROM job_queue WHERE status = ? GROUP BY plugin", StatusRunning)
	if err == nil {
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var pl PluginLane
			if err := rows.Scan(&pl.Plugin, &pl.ActiveCount); err == nil {
				m.PluginLanes = append(m.PluginLanes, pl)
			}
		}
	}

	return m, nil
}

// RunningCountsByPlugin returns current running job counts grouped by plugin.
func (q *Queue) RunningCountsByPlugin(ctx context.Context) (map[string]int, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT plugin, COUNT(*)
FROM job_queue
WHERE status = ?
GROUP BY plugin;
`, StatusRunning)
	if err != nil {
		return nil, fmt.Errorf("running counts by plugin: %w", err)
	}
	defer func() { _ = rows.Close() }()

	counts := make(map[string]int)
	for rows.Next() {
		var (
			plugin string
			count  int
		)
		if err := rows.Scan(&plugin, &count); err != nil {
			return nil, fmt.Errorf("scan running count: %w", err)
		}
		counts[plugin] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate running counts: %w", err)
	}
	return counts, nil
}

// CountOutstandingJobs returns queued+running jobs for a plugin command.
func (q *Queue) CountOutstandingJobs(ctx context.Context, plugin, command string) (int, error) {
	if plugin == "" {
		return 0, fmt.Errorf("plugin is empty")
	}
	if command == "" {
		return 0, fmt.Errorf("command is empty")
	}

	row := q.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM job_queue
WHERE plugin = ? AND command = ? AND status IN (?, ?);
`, plugin, command, StatusQueued, StatusRunning)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count outstanding jobs: %w", err)
	}
	return n, nil
}

// CountOutstandingPollJobs returns queued+running poll jobs for a plugin.
func (q *Queue) CountOutstandingPollJobs(ctx context.Context, plugin string) (int, error) {
	return q.CountOutstandingJobs(ctx, plugin, "poll")
}

// ListSchedulerActivePolls returns scheduler-submitted poll jobs currently
// in queued or running status, ordered oldest-first. Used by the system
// scheduler diagnostic to surface what the scheduler currently thinks is in
// flight, against the canonical store.
func (q *Queue) ListSchedulerActivePolls(ctx context.Context, submittedBy string) ([]*SchedulerActivePoll, error) {
	if submittedBy == "" {
		return nil, fmt.Errorf("submitted_by is empty")
	}

	rows, err := q.db.QueryContext(ctx, `
SELECT id, plugin, dedupe_key, status, attempt, created_at, started_at
FROM job_queue
WHERE command = ? AND submitted_by = ? AND status IN (?, ?)
ORDER BY created_at ASC, rowid ASC;
`, "poll", submittedBy, StatusQueued, StatusRunning)
	if err != nil {
		return nil, fmt.Errorf("list scheduler active polls: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			q.logger.Warn("close rows", "error", cerr)
		}
	}()

	var out []*SchedulerActivePoll
	for rows.Next() {
		var (
			poll       SchedulerActivePoll
			dedupeKey  sql.NullString
			statusS    string
			createdAtS string
			startedAtS sql.NullString
		)
		if err := rows.Scan(&poll.JobID, &poll.Plugin, &dedupeKey, &statusS, &poll.Attempt, &createdAtS, &startedAtS); err != nil {
			return nil, fmt.Errorf("scan scheduler active poll: %w", err)
		}
		poll.Status = Status(statusS)
		createdAt, err := time.Parse(time.RFC3339Nano, createdAtS)
		if err != nil {
			return nil, fmt.Errorf("parse created_at: %w", err)
		}
		poll.CreatedAt = createdAt
		if startedAtS.Valid {
			t, err := time.Parse(time.RFC3339Nano, startedAtS.String)
			if err != nil {
				return nil, fmt.Errorf("parse started_at: %w", err)
			}
			poll.StartedAt = &t
		}
		if dedupeKey.Valid {
			key := dedupeKey.String
			poll.DedupeKey = &key
		}
		out = append(out, &poll)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduler active polls: %w", err)
	}
	return out, nil
}

// CountOutstandingJobsBySubmitter returns queued+running jobs for a plugin
// command, restricted to a specific submitter. The scheduler uses this so
// externally-submitted jobs (CLI/webhook/router) do not consume the
// scheduler's parallelism budget.
func (q *Queue) CountOutstandingJobsBySubmitter(ctx context.Context, plugin, command, submittedBy string) (int, error) {
	if plugin == "" {
		return 0, fmt.Errorf("plugin is empty")
	}
	if command == "" {
		return 0, fmt.Errorf("command is empty")
	}
	if submittedBy == "" {
		return 0, fmt.Errorf("submitted_by is empty")
	}

	row := q.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM job_queue
WHERE plugin = ? AND command = ? AND submitted_by = ? AND status IN (?, ?);
`, plugin, command, submittedBy, StatusQueued, StatusRunning)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count outstanding jobs by submitter: %w", err)
	}
	return n, nil
}

// CancelOutstandingJobs marks queued+running jobs for a plugin command as dead.
func (q *Queue) CancelOutstandingJobs(ctx context.Context, plugin, command, reason string) (int, error) {
	if plugin == "" {
		return 0, fmt.Errorf("plugin is empty")
	}
	if command == "" {
		return 0, fmt.Errorf("command is empty")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled by scheduler"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cancel outstanding jobs tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
SELECT id, status
FROM job_queue
WHERE plugin = ? AND command = ? AND status IN (?, ?);
`, plugin, command, StatusQueued, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("select outstanding jobs to cancel: %w", err)
	}
	type cancelledJob struct {
		id     string
		status Status
	}
	var jobs []cancelledJob
	for rows.Next() {
		var job cancelledJob
		var statusS string
		if err := rows.Scan(&job.id, &statusS); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan outstanding job to cancel: %w", err)
		}
		job.status = Status(statusS)
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close outstanding jobs rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate outstanding jobs to cancel: %w", err)
	}

	for _, job := range jobs {
		fromStatus := job.status
		if err := q.recordJobTransition(ctx, tx, job.id, &fromStatus, StatusDead, &reason, now); err != nil {
			return 0, err
		}
	}

	res, err := tx.ExecContext(ctx, `
UPDATE job_queue
SET status = ?, completed_at = ?, last_error = ?
WHERE plugin = ? AND command = ? AND status IN (?, ?);
`, StatusDead, now, reason, plugin, command, StatusQueued, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("cancel outstanding jobs: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("cancel outstanding jobs rows affected: %w", err)
	}
	if affected != int64(len(jobs)) {
		return 0, fmt.Errorf("cancel outstanding jobs: selected %d jobs but updated %d", len(jobs), affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit cancel outstanding jobs tx: %w", err)
	}
	return int(affected), nil
}

func (q *Queue) Enqueue(ctx context.Context, req EnqueueRequest) (string, error) {
	if req.Plugin == "" {
		return "", fmt.Errorf("plugin is empty")
	}
	if req.Command == "" {
		return "", fmt.Errorf("command is empty")
	}
	if req.SubmittedBy == "" {
		return "", fmt.Errorf("submitted_by is empty")
	}
	if req.DedupeKey != nil {
		dedupeKey := strings.TrimSpace(*req.DedupeKey)
		dedupeTTL := q.dedupeTTL
		if req.DedupeTTL != nil {
			dedupeTTL = *req.DedupeTTL
		}
		if dedupeKey != "" {
			if req.DedupeTTL != nil {
				existingID, found, err := q.findOutstandingByDedupeKey(ctx, req.Plugin, req.Command, dedupeKey)
				if err != nil {
					return "", fmt.Errorf("dedupe lookup: %w", err)
				}
				if found {
					q.logger.Info(
						"dropped duplicate enqueue",
						"dedupe_key", dedupeKey,
						"existing_job_id", existingID,
						"dedupe_reason", "outstanding",
					)
					return "", &DedupeDropError{
						DedupeKey:     dedupeKey,
						ExistingJobID: existingID,
					}
				}
			}

			if dedupeTTL > 0 {
				existingID, found, err := q.findRecentSucceededByDedupeKey(ctx, req.Plugin, req.Command, dedupeKey, dedupeTTL)
				if err != nil {
					return "", fmt.Errorf("dedupe lookup: %w", err)
				}
				if found {
					q.logger.Info(
						"dropped duplicate enqueue",
						"dedupe_key", dedupeKey,
						"existing_job_id", existingID,
						"dedupe_ttl", dedupeTTL.String(),
						"dedupe_reason", "recent_success",
					)
					return "", &DedupeDropError{
						DedupeKey:     dedupeKey,
						ExistingJobID: existingID,
					}
				}
			}
		}
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	maxAttempts := req.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 4
	}

	var payload any
	if len(req.Payload) > 0 {
		payload = req.Payload
	}
	enqueuedConfigSnapshotID := strings.TrimSpace(req.EnqueuedConfigSnapshotID)
	if enqueuedConfigSnapshotID == "" && q.configSnapshotIDProvider != nil {
		enqueuedConfigSnapshotID = strings.TrimSpace(q.configSnapshotIDProvider())
	}
	var enqueuedConfigSnapshotVal any
	if enqueuedConfigSnapshotID != "" {
		enqueuedConfigSnapshotVal = enqueuedConfigSnapshotID
	}

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin enqueue tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO job_queue(
  id, plugin, command, payload, status, attempt, max_attempts, submitted_by, dedupe_key,
  created_at, parent_job_id, source_event_id, event_context_id, enqueued_config_snapshot_id
)
VALUES(?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?);
`, id, req.Plugin, req.Command, payload, StatusQueued, maxAttempts, req.SubmittedBy, req.DedupeKey, now, req.ParentJobID, req.SourceEventID, req.EventContextID, enqueuedConfigSnapshotVal)
	if err != nil {
		return "", fmt.Errorf("enqueue job: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("enqueue job rows affected: %w", err)
	}
	if affected == 0 {
		// INSERT OR IGNORE no-op. On the source-event path this means a row
		// with the same (parent_job_id, source_event_id, plugin, command)
		// identity already exists — a benign same-target redelivery. C-FRO-16:
		// surface it instead of returning (id, nil) for a row never inserted.
		if req.SourceEventID != nil && strings.TrimSpace(*req.SourceEventID) != "" {
			parentJobID := ""
			if req.ParentJobID != nil {
				parentJobID = *req.ParentJobID
			}
			return "", &SourceEventConflictError{
				ParentJobID:   parentJobID,
				SourceEventID: *req.SourceEventID,
				Plugin:        req.Plugin,
				Command:       req.Command,
			}
		}
	}

	if affected > 0 {
		if err := q.recordJobTransition(ctx, tx, id, nil, StatusQueued, nil, now); err != nil {
			return "", err
		}
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit enqueue tx: %w", err)
	}

	return id, nil
}

func (q *Queue) findRecentSucceededByDedupeKey(ctx context.Context, plugin, command, dedupeKey string, ttl time.Duration) (string, bool, error) {
	cutoff := time.Now().UTC().Add(-ttl).Format(time.RFC3339Nano)
	var id string
	err := q.db.QueryRowContext(ctx, `
SELECT id
FROM job_queue
WHERE dedupe_key = ?
  AND plugin = ?
  AND command = ?
  AND status = ?
  AND completed_at IS NOT NULL
  AND completed_at >= ?
ORDER BY completed_at DESC
LIMIT 1;
`, dedupeKey, plugin, command, StatusSucceeded, cutoff).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (q *Queue) findOutstandingByDedupeKey(ctx context.Context, plugin, command, dedupeKey string) (string, bool, error) {
	var id string
	err := q.db.QueryRowContext(ctx, `
SELECT id
FROM job_queue
WHERE dedupe_key = ?
  AND plugin = ?
  AND command = ?
  AND status IN (?, ?)
ORDER BY created_at DESC, rowid DESC
LIMIT 1;
`, dedupeKey, plugin, command, StatusQueued, StatusRunning).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// DedupeStatus mirrors what Enqueue would see for the (plugin, command,
// dedupeKey) tuple, without writing any state. Returns the existing job ID
// when a match exists. Outstanding (queued/running) takes precedence over
// recent-success, matching the order Enqueue applies.
//
// Used by state.Admitter to decide admission before durable context creation,
// avoiding orphan event_context rows when the queue would drop the eventual
// Enqueue as a duplicate (P2-08, P2-09).
func (q *Queue) DedupeStatus(ctx context.Context, plugin, command, dedupeKey string) (state.DedupeStatus, string, error) {
	key := strings.TrimSpace(dedupeKey)
	if key == "" || strings.TrimSpace(plugin) == "" || strings.TrimSpace(command) == "" {
		return state.DedupeStatusNone, "", nil
	}

	existingID, found, err := q.findOutstandingByDedupeKey(ctx, plugin, command, key)
	if err != nil {
		return state.DedupeStatusNone, "", fmt.Errorf("dedupe lookup (outstanding): %w", err)
	}
	if found {
		return state.DedupeStatusOutstanding, existingID, nil
	}

	if q.dedupeTTL > 0 {
		existingID, found, err = q.findRecentSucceededByDedupeKey(ctx, plugin, command, key, q.dedupeTTL)
		if err != nil {
			return state.DedupeStatusNone, "", fmt.Errorf("dedupe lookup (recent_success): %w", err)
		}
		if found {
			return state.DedupeStatusRecentSuccess, existingID, nil
		}
	}

	return state.DedupeStatusNone, "", nil
}

// Dequeue claims the oldest queued job and marks it running. Returns (nil, nil)
// if the queue is empty.
func (q *Queue) Dequeue(ctx context.Context) (*Job, error) {
	return q.DequeueEligible(ctx, nil, nil)
}

// DequeueEligible claims the oldest queued job whose plugin is NOT in
// skipPlugins, whose dedupe_key is not in activeConcurrencyKeys, and for which
// no job with the same (dedupe_key, plugin, command) is already running. This
// lets the dispatcher skip plugins at their parallelism cap and serializes
// jobs that share a concurrency key FOR THE SAME TARGET (e.g. same repo
// target), using job_queue as the source of truth.
//
// The running-exclusion is target-scoped (plugin + command) on purpose
// (C-FRO-16): fan-out siblings inherit one source-event dedupe_key but go to
// distinct targets, so a raw same-dedupe_key exclusion serialized them and
// starved later siblings behind a slow/stuck first one. The documented
// contract is a per-target guard (docs/ZK_2026-03-04_concurrency-rollout.md).
//
// Dequeue calls this with empty explicit filters; same (key, target) running
// exclusion still applies because job_queue is the concurrency source of
// truth. Returns (nil, nil) when no eligible job exists.
func (q *Queue) DequeueEligible(ctx context.Context, skipPlugins []string, activeConcurrencyKeys []string) (*Job, error) {
	now := time.Now().UTC()
	nowS := now.Format(time.RFC3339Nano)

	// Build the dynamic WHERE clause and args.
	args := []any{StatusQueued, nowS}

	pluginFilter := ""
	if len(skipPlugins) > 0 {
		placeholders := make([]string, len(skipPlugins))
		for i, p := range skipPlugins {
			placeholders[i] = "?"
			args = append(args, p)
		}
		pluginFilter = " AND candidate.plugin NOT IN (" + strings.Join(placeholders, ",") + ")"
	}

	keyFilter := ""
	if len(activeConcurrencyKeys) > 0 {
		placeholders := make([]string, len(activeConcurrencyKeys))
		for i, k := range activeConcurrencyKeys {
			placeholders[i] = "?"
			args = append(args, k)
		}
		keyFilter = " AND (candidate.dedupe_key IS NULL OR candidate.dedupe_key NOT IN (" + strings.Join(placeholders, ",") + "))"
	}

	query := `
WITH next AS (
  SELECT candidate.id
  FROM job_queue AS candidate
  WHERE candidate.status = ? AND (candidate.next_retry_at IS NULL OR candidate.next_retry_at = '' OR candidate.next_retry_at <= ?)` +
		pluginFilter + keyFilter + `
    AND (
      candidate.dedupe_key IS NULL
      OR NOT EXISTS (
        SELECT 1
        FROM job_queue AS running
        WHERE running.status = ?
          AND running.dedupe_key = candidate.dedupe_key
          AND running.plugin = candidate.plugin
          AND running.command = candidate.command
      )
    )
  ORDER BY candidate.created_at ASC, candidate.rowid ASC
  LIMIT 1
)
UPDATE job_queue
SET status = ?, started_at = ?, started_config_snapshot_id = ?
WHERE id IN (SELECT id FROM next)
RETURNING
  id, plugin, command, payload, status, attempt, max_attempts, submitted_by, dedupe_key,
  created_at, started_at, completed_at, next_retry_at, last_error, parent_job_id, source_event_id, event_context_id,
  enqueued_config_snapshot_id, started_config_snapshot_id;`

	startedConfigSnapshotID := ""
	if q.configSnapshotIDProvider != nil {
		startedConfigSnapshotID = strings.TrimSpace(q.configSnapshotIDProvider())
	}
	var startedConfigSnapshotVal any
	if startedConfigSnapshotID != "" {
		startedConfigSnapshotVal = startedConfigSnapshotID
	}
	args = append(args, StatusRunning, StatusRunning, nowS, startedConfigSnapshotVal)

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin dequeue tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	job, err := q.scanDequeuedJob(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, nil
	}

	fromStatus := StatusQueued
	if err := q.recordJobTransition(ctx, tx, job.ID, &fromStatus, StatusRunning, nil, nowS); err != nil {
		return nil, err
	}
	if err := q.recordJobAttempt(ctx, tx, job.ID, job.Attempt, nowS); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit dequeue tx: %w", err)
	}

	return job, nil
}

// scanDequeuedJob scans a single RETURNING row from a dequeue CTE into a Job.
func (q *Queue) scanDequeuedJob(row *sql.Row) (*Job, error) {
	var (
		j                        Job
		payload                  sql.NullString
		dedupeKey                sql.NullString
		createdAtS               string
		startedAtS               sql.NullString
		completedAtS             sql.NullString
		nextRetryAtS             sql.NullString
		lastError                sql.NullString
		parentJobID              sql.NullString
		sourceEventID            sql.NullString
		eventContextID           sql.NullString
		enqueuedConfigSnapshotID sql.NullString
		startedConfigSnapshotID  sql.NullString
		statusS                  string
	)
	err := row.Scan(
		&j.ID, &j.Plugin, &j.Command, &payload, &statusS, &j.Attempt, &j.MaxAttempts, &j.SubmittedBy, &dedupeKey,
		&createdAtS, &startedAtS, &completedAtS, &nextRetryAtS, &lastError, &parentJobID, &sourceEventID, &eventContextID,
		&enqueuedConfigSnapshotID, &startedConfigSnapshotID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dequeue job: %w", err)
	}

	j.Status = Status(statusS)
	if payload.Valid {
		j.Payload = []byte(payload.String)
	}
	if dedupeKey.Valid {
		j.DedupeKey = &dedupeKey.String
	}
	if t, err := time.Parse(time.RFC3339Nano, createdAtS); err == nil {
		j.CreatedAt = t
	}
	if startedAtS.Valid {
		if t, err := time.Parse(time.RFC3339Nano, startedAtS.String); err == nil {
			j.StartedAt = &t
		}
	}
	if completedAtS.Valid {
		if t, err := time.Parse(time.RFC3339Nano, completedAtS.String); err == nil {
			j.CompletedAt = &t
		}
	}
	if nextRetryAtS.Valid {
		if t, err := time.Parse(time.RFC3339Nano, nextRetryAtS.String); err == nil {
			j.NextRetryAt = &t
		}
	}
	if lastError.Valid {
		j.LastError = &lastError.String
	}
	if parentJobID.Valid {
		j.ParentJobID = &parentJobID.String
	}
	if sourceEventID.Valid {
		j.SourceEventID = &sourceEventID.String
	}
	if eventContextID.Valid {
		j.EventContextID = &eventContextID.String
	}
	if enqueuedConfigSnapshotID.Valid {
		j.EnqueuedConfigSnapshotID = &enqueuedConfigSnapshotID.String
	}
	if startedConfigSnapshotID.Valid {
		j.StartedConfigSnapshotID = &startedConfigSnapshotID.String
	}
	return &j, nil
}

// FindJobsByStatus retrieves all jobs with the given status.
func (q *Queue) FindJobsByStatus(ctx context.Context, status Status) ([]*Job, error) {
	rows, err := q.db.QueryContext(ctx, `
SELECT
  id, plugin, command, payload, status, attempt, max_attempts, submitted_by, dedupe_key,
  created_at, started_at, completed_at, next_retry_at, last_error, parent_job_id, source_event_id, event_context_id
FROM job_queue
WHERE status = ?;
`, status)
	if err != nil {
		return nil, fmt.Errorf("query jobs by status: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			q.logger.Warn("close rows", "error", err)
		}
	}()

	var jobs []*Job
	for rows.Next() {
		var (
			j              Job
			payload        sql.NullString
			dedupeKey      sql.NullString
			createdAtS     string
			startedAtS     sql.NullString
			completedAtS   sql.NullString
			nextRetryAtS   sql.NullString
			lastError      sql.NullString
			parentJobID    sql.NullString
			sourceEventID  sql.NullString
			eventContextID sql.NullString
			statusS        string
		)
		if err := rows.Scan(
			&j.ID, &j.Plugin, &j.Command, &payload, &statusS, &j.Attempt, &j.MaxAttempts, &j.SubmittedBy, &dedupeKey,
			&createdAtS, &startedAtS, &completedAtS, &nextRetryAtS, &lastError, &parentJobID, &sourceEventID, &eventContextID,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}

		j.Status = Status(statusS)
		if payload.Valid {
			j.Payload = []byte(payload.String)
		}
		if dedupeKey.Valid {
			j.DedupeKey = &dedupeKey.String
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAtS); err == nil {
			j.CreatedAt = t
		}
		if startedAtS.Valid {
			if t, err := time.Parse(time.RFC3339Nano, startedAtS.String); err == nil {
				j.StartedAt = &t
			}
		}
		if completedAtS.Valid {
			if t, err := time.Parse(time.RFC3339Nano, completedAtS.String); err == nil {
				j.CompletedAt = &t
			}
		}
		if nextRetryAtS.Valid {
			if t, err := time.Parse(time.RFC3339Nano, nextRetryAtS.String); err == nil {
				j.NextRetryAt = &t
			}
		}
		if lastError.Valid {
			j.LastError = &lastError.String
		}
		if parentJobID.Valid {
			j.ParentJobID = &parentJobID.String
		}
		if sourceEventID.Valid {
			j.SourceEventID = &sourceEventID.String
		}
		if eventContextID.Valid {
			j.EventContextID = &eventContextID.String
		}
		jobs = append(jobs, &j)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job rows: %w", err)
	}

	return jobs, nil
}

// ListJobs retrieves jobs using optional filters and returns total matches before limit.
func (q *Queue) ListJobs(ctx context.Context, filter ListJobsFilter) ([]*JobSummary, int, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	var (
		whereParts []string
		args       []any
	)
	whereParts = append(whereParts, "1=1")

	if filter.Plugin != "" {
		whereParts = append(whereParts, "plugin = ?")
		args = append(args, filter.Plugin)
	}
	if filter.Command != "" {
		whereParts = append(whereParts, "command = ?")
		args = append(args, filter.Command)
	}
	if filter.Status != nil {
		whereParts = append(whereParts, "status = ?")
		args = append(args, *filter.Status)
	}

	whereClause := strings.Join(whereParts, " AND ")

	// whereClause is composed from fixed fragments; values are parameterized via args.
	countQuery := "SELECT COUNT(*) FROM job_queue WHERE " + whereClause + ";"
	var total int
	if err := q.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	listQuery := strings.Builder{}
	listQuery.WriteString("\nSELECT\n  id, plugin, command, status, created_at, started_at, completed_at, attempt\nFROM job_queue\nWHERE ")
	listQuery.WriteString(whereClause)
	listQuery.WriteString("\nORDER BY created_at DESC, rowid DESC\nLIMIT ?;\n")
	listArgs := append(append([]any{}, args...), limit)
	rows, err := q.db.QueryContext(ctx, listQuery.String(), listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			q.logger.Warn("close rows", "error", err)
		}
	}()

	var jobs []*JobSummary
	for rows.Next() {
		var (
			job         JobSummary
			statusS     string
			createdAtS  string
			startedAtS  sql.NullString
			completedAt sql.NullString
		)

		if err := rows.Scan(&job.JobID, &job.Plugin, &job.Command, &statusS, &createdAtS, &startedAtS, &completedAt, &job.Attempt); err != nil {
			return nil, 0, fmt.Errorf("scan listed job: %w", err)
		}

		job.Status = Status(statusS)

		createdAt, err := time.Parse(time.RFC3339Nano, createdAtS)
		if err != nil {
			return nil, 0, fmt.Errorf("parse listed job created_at: %w", err)
		}
		job.CreatedAt = createdAt

		if startedAtS.Valid {
			t, err := time.Parse(time.RFC3339Nano, startedAtS.String)
			if err != nil {
				return nil, 0, fmt.Errorf("parse listed job started_at: %w", err)
			}
			job.StartedAt = &t
		}

		if completedAt.Valid {
			t, err := time.Parse(time.RFC3339Nano, completedAt.String)
			if err != nil {
				return nil, 0, fmt.Errorf("parse listed job completed_at: %w", err)
			}
			job.CompletedAt = &t
		}

		jobs = append(jobs, &job)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate listed jobs: %w", err)
	}

	return jobs, total, nil
}

// ListJobLogs retrieves job log rows with optional filters and search terms.
func (q *Queue) ListJobLogs(ctx context.Context, filter JobLogFilter) ([]*JobLogEntry, int, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}

	var (
		whereParts []string
		args       []any
	)
	whereParts = append(whereParts, "1=1")

	if filter.JobID != "" {
		whereParts = append(whereParts, "(q.id = ? OR l.id LIKE ?)")
		args = append(args, filter.JobID, filter.JobID+"-%")
	}
	if filter.Plugin != "" {
		whereParts = append(whereParts, "l.plugin = ?")
		args = append(args, filter.Plugin)
	}
	if filter.Command != "" {
		whereParts = append(whereParts, "l.command = ?")
		args = append(args, filter.Command)
	}
	if filter.Status != nil {
		whereParts = append(whereParts, "l.status = ?")
		args = append(args, *filter.Status)
	}
	if filter.SubmittedBy != "" {
		whereParts = append(whereParts, "l.submitted_by = ?")
		args = append(args, filter.SubmittedBy)
	}
	if filter.Since != nil {
		whereParts = append(whereParts, "l.completed_at >= ?")
		args = append(args, filter.Since.Format(time.RFC3339Nano))
	}
	if filter.Until != nil {
		whereParts = append(whereParts, "l.completed_at <= ?")
		args = append(args, filter.Until.Format(time.RFC3339Nano))
	}
	if strings.TrimSpace(filter.Query) != "" {
		needle := "%" + strings.ToLower(strings.TrimSpace(filter.Query)) + "%"
		whereParts = append(whereParts, "(LOWER(COALESCE(l.last_error, '')) LIKE ? OR LOWER(COALESCE(l.stderr, '')) LIKE ? OR LOWER(COALESCE(l.result, '')) LIKE ?)")
		args = append(args, needle, needle, needle)
	}

	whereClause := strings.Join(whereParts, " AND ")

	countQuery := "SELECT COUNT(*) FROM job_log l LEFT JOIN job_queue q ON l.job_id = q.id AND l.attempt = q.attempt WHERE " + whereClause + ";"
	var total int
	if err := q.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count job logs: %w", err)
	}

	listQuery := strings.Builder{}
	listQuery.WriteString("\nSELECT\n  COALESCE(l.job_id, COALESCE(q.id, '')) AS job_id, l.id, l.plugin, l.command, l.status, l.attempt, l.submitted_by, l.created_at, l.completed_at, l.last_error, l.stderr")
	if filter.IncludeResult {
		listQuery.WriteString(", l.result")
	} else {
		listQuery.WriteString(", NULL")
	}
	listQuery.WriteString("\nFROM job_log l\nLEFT JOIN job_queue q ON l.job_id = q.id AND l.attempt = q.attempt\nWHERE ")
	listQuery.WriteString(whereClause)
	listQuery.WriteString("\nORDER BY l.completed_at DESC, l.rowid DESC\nLIMIT ?;\n")

	listArgs := append(append([]any{}, args...), limit)
	rows, err := q.db.QueryContext(ctx, listQuery.String(), listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list job logs: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			q.logger.Warn("close rows", "error", err)
		}
	}()

	var logs []*JobLogEntry
	for rows.Next() {
		var (
			entry       JobLogEntry
			jobID       sql.NullString
			statusS     string
			createdAtS  string
			completedAt string
			lastErrS    sql.NullString
			stderrS     sql.NullString
			resultS     sql.NullString
		)

		if err := rows.Scan(&jobID, &entry.LogID, &entry.Plugin, &entry.Command, &statusS, &entry.Attempt, &entry.SubmittedBy, &createdAtS, &completedAt, &lastErrS, &stderrS, &resultS); err != nil {
			return nil, 0, fmt.Errorf("scan job log: %w", err)
		}

		if jobID.Valid {
			entry.JobID = jobID.String
		}
		entry.Status = Status(statusS)

		createdAt, err := time.Parse(time.RFC3339Nano, createdAtS)
		if err != nil {
			return nil, 0, fmt.Errorf("parse job log created_at: %w", err)
		}
		entry.CreatedAt = createdAt

		completedAtT, err := time.Parse(time.RFC3339Nano, completedAt)
		if err != nil {
			return nil, 0, fmt.Errorf("parse job log completed_at: %w", err)
		}
		entry.CompletedAt = completedAtT

		if lastErrS.Valid {
			entry.LastError = &lastErrS.String
		}
		if stderrS.Valid {
			entry.Stderr = &stderrS.String
		}
		if resultS.Valid {
			entry.Result = json.RawMessage(resultS.String)
		}

		logs = append(logs, &entry)
	}

	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate job logs: %w", err)
	}

	return logs, total, nil
}

// LatestCompletedCommandResult returns the most recently completed result for a
// plugin command submitted by a given producer identity.
func (q *Queue) LatestCompletedCommandResult(ctx context.Context, plugin, command, submittedBy string) (*CommandResult, error) {
	if plugin == "" {
		return nil, fmt.Errorf("plugin is empty")
	}
	if command == "" {
		return nil, fmt.Errorf("command is empty")
	}
	if submittedBy == "" {
		return nil, fmt.Errorf("submitted_by is empty")
	}

	row := q.db.QueryRowContext(ctx, `
SELECT id, status, completed_at
FROM job_queue
WHERE plugin = ? AND command = ? AND submitted_by = ? AND completed_at IS NOT NULL
  AND status IN (?, ?, ?, ?)
ORDER BY completed_at DESC, rowid DESC
LIMIT 1;
`, plugin, command, submittedBy, StatusSucceeded, StatusFailed, StatusTimedOut, StatusDead)

	var (
		jobID       string
		statusS     string
		completedAt string
	)
	if err := row.Scan(&jobID, &statusS, &completedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("latest completed command result: %w", err)
	}

	completedAtT, err := time.Parse(time.RFC3339Nano, completedAt)
	if err != nil {
		return nil, fmt.Errorf("latest completed command result: parse completed_at: %w", err)
	}

	return &CommandResult{
		JobID:       jobID,
		Status:      Status(statusS),
		CompletedAt: completedAtT,
	}, nil
}

// LatestCompletedPollResult returns the most recently completed poll result for a plugin
// submitted by the scheduler identity.
func (q *Queue) LatestCompletedPollResult(ctx context.Context, plugin, submittedBy string) (*PollResult, error) {
	return q.LatestCompletedCommandResult(ctx, plugin, "poll", submittedBy)
}

// GetScheduleEntryState returns persisted schedule-entry state for plugin+schedule_id.
func (q *Queue) GetScheduleEntryState(ctx context.Context, plugin, scheduleID string) (*ScheduleEntryState, error) {
	if plugin == "" {
		return nil, fmt.Errorf("plugin is empty")
	}
	if scheduleID == "" {
		return nil, fmt.Errorf("schedule_id is empty")
	}

	row := q.db.QueryRowContext(ctx, `
SELECT plugin, schedule_id, command, status, reason, last_fired_at, last_success_job_id, last_success_at, next_run_at, updated_at
FROM schedule_entries
WHERE plugin = ? AND schedule_id = ?;
`, plugin, scheduleID)

	var (
		state            ScheduleEntryState
		statusS          string
		reason           sql.NullString
		lastFiredAt      sql.NullString
		lastSuccessJobID sql.NullString
		lastSuccessAt    sql.NullString
		nextRunAt        sql.NullString
		updatedAt        string
	)
	if err := row.Scan(
		&state.Plugin,
		&state.ScheduleID,
		&state.Command,
		&statusS,
		&reason,
		&lastFiredAt,
		&lastSuccessJobID,
		&lastSuccessAt,
		&nextRunAt,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get schedule entry state: %w", err)
	}

	state.Status = ScheduleEntryStatus(statusS)
	if reason.Valid {
		state.Reason = &reason.String
	}
	if lastFiredAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastFiredAt.String); err == nil {
			state.LastFiredAt = &t
		}
	}
	if lastSuccessJobID.Valid {
		state.LastSuccessJobID = &lastSuccessJobID.String
	}
	if lastSuccessAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastSuccessAt.String); err == nil {
			state.LastSuccessAt = &t
		}
	}
	if nextRunAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, nextRunAt.String); err == nil {
			state.NextRunAt = &t
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		state.UpdatedAt = t
	}
	return &state, nil
}

// UpsertScheduleEntryState inserts or updates persisted schedule-entry state.
func (q *Queue) UpsertScheduleEntryState(ctx context.Context, state ScheduleEntryState) error {
	if state.Plugin == "" {
		return fmt.Errorf("plugin is empty")
	}
	if state.ScheduleID == "" {
		return fmt.Errorf("schedule_id is empty")
	}
	if state.Command == "" {
		return fmt.Errorf("command is empty")
	}
	if state.Status == "" {
		state.Status = ScheduleEntryActive
	}

	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	var reason any
	if state.Reason != nil {
		reason = *state.Reason
	}
	var lastFiredAt any
	if state.LastFiredAt != nil {
		lastFiredAt = state.LastFiredAt.UTC().Format(time.RFC3339Nano)
	}
	var lastSuccessJobID any
	if state.LastSuccessJobID != nil {
		lastSuccessJobID = *state.LastSuccessJobID
	}
	var lastSuccessAt any
	if state.LastSuccessAt != nil {
		lastSuccessAt = state.LastSuccessAt.UTC().Format(time.RFC3339Nano)
	}
	var nextRunAt any
	if state.NextRunAt != nil {
		nextRunAt = state.NextRunAt.UTC().Format(time.RFC3339Nano)
	}

	_, err := q.db.ExecContext(ctx, `
INSERT INTO schedule_entries(plugin, schedule_id, command, status, reason, last_fired_at, last_success_job_id, last_success_at, next_run_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(plugin, schedule_id) DO UPDATE SET
  command = excluded.command,
  status = excluded.status,
  reason = excluded.reason,
  last_fired_at = excluded.last_fired_at,
  last_success_job_id = excluded.last_success_job_id,
  last_success_at = excluded.last_success_at,
  next_run_at = excluded.next_run_at,
  updated_at = excluded.updated_at;
`, state.Plugin, state.ScheduleID, state.Command, state.Status, reason, lastFiredAt, lastSuccessJobID, lastSuccessAt, nextRunAt, updatedAt)
	if err != nil {
		return fmt.Errorf("upsert schedule entry state: %w", err)
	}

	return nil
}

// GetCircuitBreaker returns the persisted circuit breaker row for plugin+command.
func (q *Queue) GetCircuitBreaker(ctx context.Context, plugin, command string) (*CircuitBreaker, error) {
	if plugin == "" {
		return nil, fmt.Errorf("plugin is empty")
	}
	if command == "" {
		return nil, fmt.Errorf("command is empty")
	}

	row := q.db.QueryRowContext(ctx, `
SELECT plugin, command, state, failure_count, opened_at, last_failure_at, last_job_id, updated_at
FROM circuit_breakers
WHERE plugin = ? AND command = ?;
`, plugin, command)

	var (
		cb           CircuitBreaker
		stateS       string
		openedAtS    sql.NullString
		lastFailureS sql.NullString
		lastJobID    sql.NullString
		updatedAtS   string
	)
	if err := row.Scan(
		&cb.Plugin,
		&cb.Command,
		&stateS,
		&cb.FailureCount,
		&openedAtS,
		&lastFailureS,
		&lastJobID,
		&updatedAtS,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get circuit breaker: %w", err)
	}

	cb.State = CircuitState(stateS)
	if openedAtS.Valid {
		t, err := time.Parse(time.RFC3339Nano, openedAtS.String)
		if err == nil {
			cb.OpenedAt = &t
		}
	}
	if lastFailureS.Valid {
		t, err := time.Parse(time.RFC3339Nano, lastFailureS.String)
		if err == nil {
			cb.LastFailure = &t
		}
	}
	if lastJobID.Valid {
		cb.LastJobID = &lastJobID.String
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAtS); err == nil {
		cb.UpdatedAt = t
	}

	return &cb, nil
}

// UpsertCircuitBreaker inserts or updates persisted breaker state.
func (q *Queue) UpsertCircuitBreaker(ctx context.Context, cb CircuitBreaker) error {
	if cb.Plugin == "" {
		return fmt.Errorf("plugin is empty")
	}
	if cb.Command == "" {
		return fmt.Errorf("command is empty")
	}
	if cb.State == "" {
		cb.State = CircuitClosed
	}

	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)

	var openedAt any
	if cb.OpenedAt != nil {
		openedAt = cb.OpenedAt.Format(time.RFC3339Nano)
	}

	var lastFailure any
	if cb.LastFailure != nil {
		lastFailure = cb.LastFailure.Format(time.RFC3339Nano)
	}

	var lastJobID any
	if cb.LastJobID != nil {
		lastJobID = *cb.LastJobID
	}

	_, err := q.db.ExecContext(ctx, `
INSERT INTO circuit_breakers(
  plugin, command, state, failure_count, opened_at, last_failure_at, last_job_id, updated_at
)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(plugin, command) DO UPDATE SET
  state = excluded.state,
  failure_count = excluded.failure_count,
  opened_at = excluded.opened_at,
  last_failure_at = excluded.last_failure_at,
  last_job_id = excluded.last_job_id,
  updated_at = excluded.updated_at;
`, cb.Plugin, cb.Command, cb.State, cb.FailureCount, openedAt, lastFailure, lastJobID, updatedAt)
	if err != nil {
		return fmt.Errorf("upsert circuit breaker: %w", err)
	}
	return nil
}

// RecordCircuitBreakerTransition appends one circuit breaker transition fact.
func (q *Queue) RecordCircuitBreakerTransition(ctx context.Context, transition CircuitBreakerTransition) error {
	if transition.Plugin == "" {
		return fmt.Errorf("plugin is empty")
	}
	if transition.Command == "" {
		return fmt.Errorf("command is empty")
	}
	if transition.ToState == "" {
		return fmt.Errorf("to_state is empty")
	}
	if transition.Reason == "" {
		return fmt.Errorf("reason is empty")
	}
	if transition.ID == "" {
		transition.ID = uuid.NewString()
	}
	if transition.CreatedAt.IsZero() {
		transition.CreatedAt = time.Now().UTC()
	}

	var fromState any
	if transition.FromState != nil {
		fromState = *transition.FromState
	}
	var jobID any
	if transition.JobID != nil && strings.TrimSpace(*transition.JobID) != "" {
		jobID = strings.TrimSpace(*transition.JobID)
	}

	_, err := q.db.ExecContext(ctx, `
INSERT INTO circuit_breaker_transitions(
  id, plugin, command, from_state, to_state, failure_count, reason, job_id, created_at
)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?);
`, transition.ID, transition.Plugin, transition.Command, fromState, transition.ToState, transition.FailureCount, transition.Reason, jobID, transition.CreatedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record circuit breaker transition: %w", err)
	}
	return nil
}

// ListCircuitBreakerTransitions returns recent circuit breaker transition facts.
func (q *Queue) ListCircuitBreakerTransitions(ctx context.Context, plugin, command string, limit int) ([]CircuitBreakerTransition, error) {
	if plugin == "" {
		return nil, fmt.Errorf("plugin is empty")
	}
	if command == "" {
		return nil, fmt.Errorf("command is empty")
	}
	if limit <= 0 {
		limit = 20
	}

	rows, err := q.db.QueryContext(ctx, `
SELECT id, plugin, command, from_state, to_state, failure_count, reason, job_id, created_at
FROM circuit_breaker_transitions
WHERE plugin = ? AND command = ?
ORDER BY created_at DESC, rowid DESC
LIMIT ?;
`, plugin, command, limit)
	if err != nil {
		return nil, fmt.Errorf("list circuit breaker transitions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	transitions := make([]CircuitBreakerTransition, 0)
	for rows.Next() {
		var (
			transition CircuitBreakerTransition
			fromState  sql.NullString
			toState    string
			reason     string
			jobID      sql.NullString
			createdAt  string
		)
		if err := rows.Scan(
			&transition.ID,
			&transition.Plugin,
			&transition.Command,
			&fromState,
			&toState,
			&transition.FailureCount,
			&reason,
			&jobID,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan circuit breaker transition: %w", err)
		}
		if fromState.Valid {
			state := CircuitState(fromState.String)
			transition.FromState = &state
		}
		transition.ToState = CircuitState(toState)
		transition.Reason = CircuitBreakerTransitionReason(reason)
		if jobID.Valid {
			transition.JobID = &jobID.String
		}
		if parsed, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
			transition.CreatedAt = parsed
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate circuit breaker transitions: %w", err)
	}
	return transitions, nil
}

// ResetCircuitBreaker closes a plugin+command circuit and clears failure history.
func (q *Queue) ResetCircuitBreaker(ctx context.Context, plugin, command string) error {
	previous, err := q.GetCircuitBreaker(ctx, plugin, command)
	if err != nil {
		return err
	}
	if err := q.UpsertCircuitBreaker(ctx, CircuitBreaker{
		Plugin:       plugin,
		Command:      command,
		State:        CircuitClosed,
		FailureCount: 0,
	}); err != nil {
		return err
	}

	var fromState *CircuitState
	if previous != nil {
		fromState = &previous.State
	}
	return q.RecordCircuitBreakerTransition(ctx, CircuitBreakerTransition{
		Plugin:       plugin,
		Command:      command,
		FromState:    fromState,
		ToState:      CircuitClosed,
		FailureCount: 0,
		Reason:       CircuitTransitionManualReset,
	})
}

// UpdateJobForRecovery updates a job's status, attempt count, next retry time, and last error.
// This is used for crash recovery to re-queue jobs with backoff or mark them as dead.
func (q *Queue) UpdateJobForRecovery(ctx context.Context, jobID string, newStatus Status, newAttempt int, nextRetryAt *time.Time, lastError string) error {
	var nextRetryAtS *string
	if nextRetryAt != nil {
		s := nextRetryAt.Format(time.RFC3339Nano)
		nextRetryAtS = &s
	}

	var lastErrorS *string
	if lastError != "" {
		lastErrorS = &lastError
	}

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recovery tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var currentStatusS string
	if err := tx.QueryRowContext(ctx, `
SELECT status
FROM job_queue
WHERE id = ?;
`, jobID).Scan(&currentStatusS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrJobNotFound
		}
		return fmt.Errorf("load job %s for recovery: %w", jobID, err)
	}

	currentStatus := Status(currentStatusS)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if currentStatus != newStatus {
		var reason *string
		if lastErrorS != nil {
			reason = lastErrorS
		}
		if err := q.recordJobTransition(ctx, tx, jobID, &currentStatus, newStatus, reason, now); err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
UPDATE job_queue
SET status = ?, attempt = ?, next_retry_at = ?, last_error = ?
WHERE id = ?;
`, newStatus, newAttempt, nextRetryAtS, lastErrorS, jobID)
	if err != nil {
		return fmt.Errorf("update job %s for recovery: %w", jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recovery tx: %w", err)
	}
	return nil
}

// PruneJobLogs deletes job log entries older than the specified retention duration.
func (q *Queue) PruneJobLogs(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		return nil // No retention, do nothing
	}

	cutoff := time.Now().UTC().Add(-retention)
	_, err := q.db.ExecContext(ctx, `
DELETE FROM job_log
WHERE completed_at < ?;
`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("prune job logs: %w", err)
	}
	return nil
}

// PruneJobQueue deletes terminal-state rows from job_queue older than the
// retention duration. job_queue is the hot table for in-flight work; terminal
// rows linger only for a short visibility window because their canonical
// record lives in job_log (see PruneJobLogs for that retention).
//
// Without this prune the queue accumulates succeeded/failed/dead/skipped/
// timed_out rows forever — the same defect surfaced by `system selfcheck`'s
// queue_terminal_freshness invariant.
func (q *Queue) PruneJobQueue(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		return nil
	}

	cutoff := time.Now().UTC().Add(-retention)
	_, err := q.db.ExecContext(ctx, `
DELETE FROM job_queue
WHERE status IN ('succeeded','skipped','failed','timed_out','dead')
  AND completed_at IS NOT NULL
  AND completed_at < ?;
`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("prune job queue: %w", err)
	}
	return nil
}

// PruneJobTransitions deletes job_transitions rows older than the retention
// duration. job_transitions is the append-only fact log over job_queue's
// mutable status; retention here is independent of job_queue retention now
// that the cross-table FK has been dropped (see commit f3b13e6).
func (q *Queue) PruneJobTransitions(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		return nil
	}

	cutoff := time.Now().UTC().Add(-retention)
	_, err := q.db.ExecContext(ctx, `
DELETE FROM job_transitions
WHERE created_at < ?;
`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("prune job transitions: %w", err)
	}
	return nil
}

// PruneJobAttempts deletes job_attempts rows older than the retention
// duration. Append-only fact log of retry attempts; retention paired with
// job_log so forensic visibility into "why did this retry" stays aligned
// with the canonical job log.
func (q *Queue) PruneJobAttempts(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		return nil
	}

	cutoff := time.Now().UTC().Add(-retention)
	_, err := q.db.ExecContext(ctx, `
DELETE FROM job_attempts
WHERE created_at < ?;
`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("prune job attempts: %w", err)
	}
	return nil
}

// PruneBreakerTransitions deletes circuit_breaker_transitions rows older
// than the retention duration. Append-only fact log of breaker state
// changes; lower volume than the job-side fact logs but valuable for
// incident review across longer windows.
func (q *Queue) PruneBreakerTransitions(ctx context.Context, retention time.Duration) error {
	if retention <= 0 {
		return nil
	}

	cutoff := time.Now().UTC().Add(-retention)
	_, err := q.db.ExecContext(ctx, `
DELETE FROM circuit_breaker_transitions
WHERE created_at < ?;
`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("prune breaker transitions: %w", err)
	}
	return nil
}

// Complete marks a job complete and writes a log row. This signature is kept
// stable since other sprint work may call it directly.
func (q *Queue) Complete(ctx context.Context, jobID string, status Status, lastError, stderr *string) error {
	return q.CompleteWithResult(ctx, jobID, status, nil, lastError, stderr)
}

// CompleteWithResult is like Complete but also stores the raw protocol response
// (plugin stdout JSON) in job_log.result for audit/debugging and API retrieval.
func (q *Queue) CompleteWithResult(ctx context.Context, jobID string, status Status, result json.RawMessage, lastError, stderr *string) error {
	if jobID == "" {
		return fmt.Errorf("jobID is empty")
	}
	if status != StatusSucceeded && status != StatusSkipped && status != StatusFailed && status != StatusTimedOut && status != StatusDead {
		return fmt.Errorf("invalid terminal status: %q", status)
	}

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		plugin                   string
		command                  string
		currentStatus            string
		attempt                  int
		submittedBy              string
		createdAt                string
		parentJobID              sql.NullString
		sourceEventID            sql.NullString
		eventContextID           sql.NullString
		enqueuedConfigSnapshotID sql.NullString
		startedConfigSnapshotID  sql.NullString
	)
	if err := tx.QueryRowContext(ctx, `
SELECT plugin, command, status, attempt, submitted_by, created_at, parent_job_id, source_event_id, event_context_id,
       enqueued_config_snapshot_id, started_config_snapshot_id
FROM job_queue
WHERE id = ?;
`, jobID).Scan(&plugin, &command, &currentStatus, &attempt, &submittedBy, &createdAt, &parentJobID, &sourceEventID, &eventContextID,
		&enqueuedConfigSnapshotID, &startedConfigSnapshotID); err != nil {
		return fmt.Errorf("load job for completion: %w", err)
	}

	completedAt := time.Now().UTC().Format(time.RFC3339Nano)
	fromStatus := Status(currentStatus)
	if fromStatus != status {
		if err := q.recordJobTransition(ctx, tx, jobID, &fromStatus, status, lastError, completedAt); err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
UPDATE job_queue
SET status = ?, completed_at = ?, last_error = ?
WHERE id = ?;
`, status, completedAt, lastError, jobID)
	if err != nil {
		return fmt.Errorf("update job completion: %w", err)
	}

	logID := fmt.Sprintf("%s-%d", jobID, attempt)

	var resultVal any
	if len(result) > 0 {
		// Store JSON as a string blob to keep it queryable/debuggable in SQLite.
		resultVal = string(result)
	}

	var stderrVal any
	if stderr != nil {
		s := *stderr
		if len(s) > maxStderrBytes {
			s = s[:maxStderrBytes]
		}
		stderrVal = s
	}

	_, err = tx.ExecContext(ctx, `
INSERT OR IGNORE INTO job_log(
  id, job_id, plugin, command, status, result, attempt, submitted_by, created_at, completed_at, last_error, stderr, parent_job_id, source_event_id, event_context_id,
  enqueued_config_snapshot_id, started_config_snapshot_id
)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`, logID, jobID, plugin, command, status, resultVal, attempt, submittedBy, createdAt, completedAt, lastError, stderrVal, parentJobID, sourceEventID, eventContextID,
		enqueuedConfigSnapshotID, startedConfigSnapshotID)
	if err != nil {
		return fmt.Errorf("insert job_log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// GetJobTree retrieves all jobs in a tree starting from rootJobID (using recursive CTE).
func (q *Queue) GetJobTree(ctx context.Context, rootJobID string) ([]*JobResult, error) {
	rows, err := q.db.QueryContext(ctx, `
WITH RECURSIVE job_tree AS (
    SELECT id, parent_job_id, status, plugin, command, last_error, started_at, completed_at, attempt, event_context_id
    FROM job_queue WHERE id = ?
    UNION ALL
    SELECT jq.id, jq.parent_job_id, jq.status, jq.plugin, jq.command, jq.last_error, jq.started_at, jq.completed_at, jq.attempt, jq.event_context_id
    FROM job_queue jq
    JOIN job_tree jt ON jq.parent_job_id = jt.id
)
SELECT
  t.id, t.parent_job_id, t.status, t.plugin, t.command, t.last_error, t.started_at, t.completed_at,
  l.result,
  COALESCE(ec.step_id, '') AS step_id
FROM job_tree t
LEFT JOIN job_log l ON l.job_id = t.id AND l.attempt = t.attempt
LEFT JOIN event_context ec ON ec.id = t.event_context_id;
`, rootJobID)
	if err != nil {
		return nil, fmt.Errorf("get job tree: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			q.logger.Warn("close rows", "error", err)
		}
	}()

	var results []*JobResult
	for rows.Next() {
		var (
			id          string
			parentID    sql.NullString
			statusS     string
			plugin      string
			command     string
			lastErrS    sql.NullString
			startedAtS  sql.NullString
			completedAt sql.NullString
			resultS     sql.NullString
			stepID      string
		)
		if err := rows.Scan(&id, &parentID, &statusS, &plugin, &command, &lastErrS, &startedAtS, &completedAt, &resultS, &stepID); err != nil {
			return nil, fmt.Errorf("scan job tree row: %w", err)
		}

		var lastErr *string
		if lastErrS.Valid {
			lastErr = &lastErrS.String
		}

		var pID *string
		if parentID.Valid {
			pID = &parentID.String
		}

		var startedAt *time.Time
		if startedAtS.Valid {
			if t, err := time.Parse(time.RFC3339Nano, startedAtS.String); err == nil {
				startedAt = &t
			}
		}

		var completedAtT *time.Time
		if completedAt.Valid {
			if t, err := time.Parse(time.RFC3339Nano, completedAt.String); err == nil {
				completedAtT = &t
			}
		}

		var result json.RawMessage
		if resultS.Valid {
			result = json.RawMessage(resultS.String)
		}

		results = append(results, &JobResult{
			JobID:       id,
			ParentJobID: pID,
			Status:      Status(statusS),
			Plugin:      plugin,
			Command:     command,
			StepID:      stepID,
			Result:      result,
			LastError:   lastErr,
			StartedAt:   startedAt,
			CompletedAt: completedAtT,
		})
	}
	return results, nil
}

// GetJobByID retrieves a job by its ID with result from job_log
func (q *Queue) GetJobByID(ctx context.Context, jobID string) (*JobResult, error) {
	if jobID == "" {
		return nil, fmt.Errorf("jobID is empty")
	}

	row := q.db.QueryRowContext(ctx, `
SELECT
  q.id, q.parent_job_id, q.status, q.plugin, q.command, q.submitted_by, q.last_error, q.started_at, q.completed_at,
  l.result,
  COALESCE(ec.step_id, '') AS step_id
FROM job_queue q
LEFT JOIN job_log l ON l.job_id = q.id AND l.attempt = q.attempt
LEFT JOIN event_context ec ON ec.id = q.event_context_id
WHERE q.id = ?;
`, jobID)

	var (
		id          string
		parentID    sql.NullString
		statusS     string
		plugin      string
		command     string
		submittedBy string
		lastErrS    sql.NullString
		startedAtS  sql.NullString
		completedAt sql.NullString
		resultS     sql.NullString
		stepID      string
	)
	if err := row.Scan(&id, &parentID, &statusS, &plugin, &command, &submittedBy, &lastErrS, &startedAtS, &completedAt, &resultS, &stepID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("get job by id: %w", err)
	}

	var lastErr *string
	if lastErrS.Valid {
		lastErr = &lastErrS.String
	}

	var pID *string
	if parentID.Valid {
		pID = &parentID.String
	}

	var startedAt *time.Time
	if startedAtS.Valid {
		if t, err := time.Parse(time.RFC3339Nano, startedAtS.String); err == nil {
			startedAt = &t
		}
	}

	var completedAtT *time.Time
	if completedAt.Valid {
		if t, err := time.Parse(time.RFC3339Nano, completedAt.String); err == nil {
			completedAtT = &t
		}
	}

	var result json.RawMessage
	if resultS.Valid {
		result = json.RawMessage(resultS.String)
	}

	return &JobResult{
		JobID:       id,
		ParentJobID: pID,
		Status:      Status(statusS),
		Plugin:      plugin,
		Command:     command,
		StepID:      stepID,
		Result:      result,
		LastError:   lastErr,
		StartedAt:   startedAt,
		CompletedAt: completedAtT,
		SubmittedBy: submittedBy,
	}, nil
}

// GetJobLineage retrieves append-only execution facts for a job and compares
// them with the current compatibility/cache fields in job_queue.
func (q *Queue) GetJobLineage(ctx context.Context, jobID string) (*JobLineage, error) {
	if jobID == "" {
		return nil, fmt.Errorf("jobID is empty")
	}

	var (
		statusS string
		attempt int
	)
	if err := q.db.QueryRowContext(ctx, `
SELECT status, attempt
FROM job_queue
WHERE id = ?;
`, jobID).Scan(&statusS, &attempt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("load job lineage cache fields: %w", err)
	}

	lineage := &JobLineage{
		JobID:         jobID,
		CachedStatus:  Status(statusS),
		CachedAttempt: attempt,
		Transitions:   make([]JobTransition, 0),
		Attempts:      make([]JobAttempt, 0),
	}

	transitionRows, err := q.db.QueryContext(ctx, `
SELECT id, job_id, from_status, to_status, reason, created_at
FROM job_transitions
WHERE job_id = ?
ORDER BY created_at ASC, id ASC;
`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query job transitions: %w", err)
	}
	defer func() { _ = transitionRows.Close() }()

	for transitionRows.Next() {
		var (
			transition JobTransition
			fromS      sql.NullString
			toS        string
			reasonS    sql.NullString
			createdAtS string
		)
		if err := transitionRows.Scan(&transition.ID, &transition.JobID, &fromS, &toS, &reasonS, &createdAtS); err != nil {
			return nil, fmt.Errorf("scan job transition: %w", err)
		}
		if fromS.Valid {
			from := Status(fromS.String)
			transition.FromStatus = &from
		}
		transition.ToStatus = Status(toS)
		if reasonS.Valid {
			transition.Reason = &reasonS.String
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAtS); err == nil {
			transition.CreatedAt = t
		}
		lineage.Transitions = append(lineage.Transitions, transition)
	}
	if err := transitionRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job transitions: %w", err)
	}

	attemptRows, err := q.db.QueryContext(ctx, `
SELECT id, job_id, attempt, created_at
FROM job_attempts
WHERE job_id = ?
ORDER BY created_at ASC, id ASC;
`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query job attempts: %w", err)
	}
	defer func() { _ = attemptRows.Close() }()

	for attemptRows.Next() {
		var (
			attemptFact JobAttempt
			createdAtS  string
		)
		if err := attemptRows.Scan(&attemptFact.ID, &attemptFact.JobID, &attemptFact.Attempt, &createdAtS); err != nil {
			return nil, fmt.Errorf("scan job attempt: %w", err)
		}
		if t, err := time.Parse(time.RFC3339Nano, createdAtS); err == nil {
			attemptFact.CreatedAt = t
		}
		lineage.Attempts = append(lineage.Attempts, attemptFact)
	}
	if err := attemptRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job attempts: %w", err)
	}

	if len(lineage.Transitions) > 0 {
		latest := lineage.Transitions[len(lineage.Transitions)-1].ToStatus
		lineage.LatestStatus = &latest
		lineage.StatusMatchesLatest = latest == lineage.CachedStatus
	} else {
		lineage.HasLegacyMissingData = true
	}

	switch {
	case len(lineage.Attempts) == 0:
		lineage.AttemptFactsMatch = lineage.CachedAttempt == 1
	default:
		latestAttempt := lineage.Attempts[len(lineage.Attempts)-1].Attempt
		lineage.AttemptFactsMatch = latestAttempt == lineage.CachedAttempt
	}

	return lineage, nil
}
