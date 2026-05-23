package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusSkipped   Status = "skipped"
	StatusFailed    Status = "failed"
	StatusTimedOut  Status = "timed_out"
	StatusDead      Status = "dead"
)

type Job struct {
	ID                       string
	Plugin                   string
	Command                  string
	Payload                  json.RawMessage
	Status                   Status
	Attempt                  int
	MaxAttempts              int
	SubmittedBy              string
	DedupeKey                *string
	CreatedAt                time.Time
	StartedAt                *time.Time
	CompletedAt              *time.Time
	NextRetryAt              *time.Time
	LastError                *string
	ParentJobID              *string
	SourceEventID            *string
	EventContextID           *string
	EnqueuedConfigSnapshotID *string
	StartedConfigSnapshotID  *string
}

// JobTransition records one append-only status change for a job.
type JobTransition struct {
	ID         int64
	JobID      string
	FromStatus *Status
	ToStatus   Status
	Reason     *string
	CreatedAt  time.Time
}

// JobAttempt records one actual execution start for a job.
type JobAttempt struct {
	ID        int64
	JobID     string
	Attempt   int
	CreatedAt time.Time
}

// JobLineage reports append-only execution facts alongside cached queue fields.
type JobLineage struct {
	JobID                string
	CachedStatus         Status
	CachedAttempt        int
	Transitions          []JobTransition
	Attempts             []JobAttempt
	LatestStatus         *Status
	StatusMatchesLatest  bool
	AttemptFactsMatch    bool
	HasLegacyMissingData bool
}

type EnqueueRequest struct {
	Plugin                   string
	Command                  string
	Payload                  json.RawMessage
	MaxAttempts              int
	SubmittedBy              string
	DedupeKey                *string
	DedupeTTL                *time.Duration
	ParentJobID              *string
	EventContextID           *string
	SourceEventID            *string
	EnqueuedConfigSnapshotID string
}

// ListJobsFilter defines optional filters for listing jobs.
type ListJobsFilter struct {
	Plugin  string
	Command string
	Status  *Status
	Limit   int
}

// JobLogFilter defines optional filters for listing job logs.
type JobLogFilter struct {
	JobID         string
	Plugin        string
	Command       string
	Status        *Status
	SubmittedBy   string
	Query         string
	Since         *time.Time
	Until         *time.Time
	Limit         int
	IncludeResult bool
}

// JobLogEntry represents a stored job log record for audit/query.
type JobLogEntry struct {
	JobID                    string
	LogID                    string
	Plugin                   string
	Command                  string
	Status                   Status
	Attempt                  int
	SubmittedBy              string
	CreatedAt                time.Time
	CompletedAt              time.Time
	LastError                *string
	Stderr                   *string
	Result                   json.RawMessage
	EnqueuedConfigSnapshotID *string
	StartedConfigSnapshotID  *string
}

type CircuitState string

const (
	CircuitClosed   CircuitState = "closed"
	CircuitOpen     CircuitState = "open"
	CircuitHalfOpen CircuitState = "half_open"
)

type CircuitBreaker struct {
	Plugin       string
	Command      string
	State        CircuitState
	FailureCount int
	OpenedAt     *time.Time
	LastFailure  *time.Time
	LastJobID    *string
	UpdatedAt    time.Time
}

// CircuitBreakerTransitionReason classifies why a breaker state fact was recorded.
type CircuitBreakerTransitionReason string

const (
	// CircuitTransitionFailureThreshold means failures reached the configured threshold.
	CircuitTransitionFailureThreshold CircuitBreakerTransitionReason = "failure_threshold"
	// CircuitTransitionSuccess means a successful probe or poll closed the breaker.
	CircuitTransitionSuccess CircuitBreakerTransitionReason = "success"
	// CircuitTransitionCooldownElapsed means an open breaker moved to half-open after cooldown.
	CircuitTransitionCooldownElapsed CircuitBreakerTransitionReason = "cooldown_elapsed"
	// CircuitTransitionManualReset means an operator manually reset the breaker.
	CircuitTransitionManualReset CircuitBreakerTransitionReason = "manual_reset"
)

// CircuitBreakerTransition records one append-only circuit breaker state fact.
type CircuitBreakerTransition struct {
	ID           string
	Plugin       string
	Command      string
	FromState    *CircuitState
	ToState      CircuitState
	FailureCount int
	Reason       CircuitBreakerTransitionReason
	JobID        *string
	CreatedAt    time.Time
}

type CommandResult struct {
	JobID       string
	Status      Status
	CompletedAt time.Time
}

// PollResult is kept as an alias for compatibility with existing tests/callers.
type PollResult = CommandResult

type ScheduleEntryStatus string

const (
	ScheduleEntryActive        ScheduleEntryStatus = "active"
	ScheduleEntryPausedManual  ScheduleEntryStatus = "paused_manual"
	ScheduleEntryPausedInvalid ScheduleEntryStatus = "paused_invalid"
	ScheduleEntryExhausted     ScheduleEntryStatus = "exhausted"
)

type ScheduleEntryState struct {
	Plugin           string
	ScheduleID       string
	Command          string
	Status           ScheduleEntryStatus
	Reason           *string
	LastFiredAt      *time.Time
	LastSuccessJobID *string
	LastSuccessAt    *time.Time
	NextRunAt        *time.Time
	UpdatedAt        time.Time
}

var ErrJobNotFound = errors.New("job not found")
var ErrDedupeDrop = errors.New("job dedupe drop")

type DedupeDropError struct {
	DedupeKey     string
	ExistingJobID string
}

func (e *DedupeDropError) Error() string {
	return fmt.Sprintf("job deduplicated for key %q (existing job %s)", e.DedupeKey, e.ExistingJobID)
}

func (e *DedupeDropError) Is(target error) bool {
	return target == ErrDedupeDrop
}

// ErrSourceEventConflict reports that an INSERT OR IGNORE on the source-event
// path was a no-op because a row with the same
// (parent_job_id, source_event_id, plugin, command) identity already exists.
// This is a benign at-least-once redelivery dedupe of the SAME fan-out target,
// distinct from the DedupeKey path. C-FRO-16: before this existed, Enqueue
// returned (id, nil) for a row it never inserted, silently losing work.
var ErrSourceEventConflict = errors.New("job source-event conflict")

type SourceEventConflictError struct {
	ParentJobID   string
	SourceEventID string
	Plugin        string
	Command       string
}

func (e *SourceEventConflictError) Error() string {
	return fmt.Sprintf(
		"job source-event conflict (parent %q, source_event %q, target %s/%s)",
		e.ParentJobID, e.SourceEventID, e.Plugin, e.Command,
	)
}

func (e *SourceEventConflictError) Is(target error) bool {
	return target == ErrSourceEventConflict
}

// JobResult is a lightweight projection for API job retrieval.
type JobResult struct {
	JobID       string
	ParentJobID *string
	Status      Status
	Plugin      string
	Command     string
	StepID      string // From event_context.step_id via event_context_id
	Result      json.RawMessage
	LastError   *string
	StartedAt   *time.Time
	CompletedAt *time.Time
	// SubmittedBy records how this job entered the queue ("route", "hook", "test",
	// "api"). P2-11 hook-depth walk needs this to distinguish hook-chain ancestors
	// from non-hook root jobs while traversing ParentJobID.
	SubmittedBy string
}

// JobSummary is a lightweight projection for jobs list APIs.
type JobSummary struct {
	JobID       string
	Plugin      string
	Command     string
	Status      Status
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	Attempt     int
}

// SchedulerActivePoll is a diagnostic projection of a scheduler-submitted
// poll job currently in queued or running status. Surfaced by the
// `ductile system scheduler` command so an operator can answer "what does
// the scheduler think is running right now?" against the canonical store.
type SchedulerActivePoll struct {
	JobID     string
	Plugin    string
	DedupeKey *string
	Status    Status
	Attempt   int
	CreatedAt time.Time
	StartedAt *time.Time
}

// QueueMetrics provides high-frequency state metrics for the queue.
type QueueMetrics struct {
	QueueDepth   int          `json:"queue_depth"`
	RunningCount int          `json:"running_count"`
	DelayedCount int          `json:"delayed_count"`
	DeadCount    int          `json:"dead_count"`
	ActiveJobs   []ActiveJob  `json:"active_jobs,omitempty"`
	PluginLanes  []PluginLane `json:"plugin_lanes,omitempty"`
}

type ActiveJob struct {
	JobID     string    `json:"job_id"`
	Plugin    string    `json:"plugin"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
}

type PluginLane struct {
	Plugin      string `json:"plugin"`
	ActiveCount int    `json:"active_count"`
}
