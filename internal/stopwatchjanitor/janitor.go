// Package stopwatchjanitor periodically rolls up raw job_stopwatch
// rows into per-day quartile buckets and then prunes the raw rows past
// the operator-configured retention TTL.
//
// The janitor is intentionally isolated from the dispatcher: its own
// goroutine, its own failure domain. A panic or persistent SQL error
// in the janitor must not crash plugin invocations or stop the queue;
// the supervisor's domain-data path keeps running and the operator
// notices via the heartbeat staleness check (doctor warn-log).
//
// Armstrong: let the janitor crash and respawn; nothing else cares.
// Hickey: rollups are values (per-day quartile facts) computed once
// from raw event rows, so the rollup table and the raw table are
// distinct concerns with distinct lifecycles.
package stopwatchjanitor

import (
	"context"
	"log/slog"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/state"
)

// Defaults applied when StopwatchTelemetryConfig leaves a field at its
// zero value. The numbers are chosen for the local-prod profile
// (single-instance, modest job volume); higher-volume deployments
// should set them explicitly in config.
const (
	DefaultRetentionDays    = 14
	DefaultJanitorInterval  = 1 * time.Hour
	DefaultJanitorBatchSize = 5000

	// MaxRollupDaysPerTick caps the number of distinct unrolled days
	// the janitor will process in a single tick. Prevents a
	// long-stale instance from doing a multi-thousand-day catch-up in
	// one shot; subsequent ticks drain the rest.
	MaxRollupDaysPerTick = 90

	// MaxPruneBatchesPerTick caps how many delete batches one tick
	// runs. Together with BatchSize this bounds the writer lock hold
	// time per tick to roughly BatchSize * batches deletes.
	MaxPruneBatchesPerTick = 50

	janitorName = "stopwatch"
)

// Store is the slice of state.Store the janitor needs. Defined here
// (the consumer) so the janitor can be tested with a thin fake and so
// state.Store is not coupled to janitor concerns.
type Store interface {
	StopwatchUnrolledDays(ctx context.Context, limit int) ([]string, error)
	StopwatchRollupDay(ctx context.Context, dayUTC string) (int, error)
	PruneStopwatchOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int, error)
	WriteJanitorHeartbeat(ctx context.Context, hb state.JanitorHeartbeat) error
}

// Janitor holds the per-instance dependencies for a single janitor
// loop. Construct with New and either call Tick directly (tests) or
// Run (supervisor lifecycle).
type Janitor struct {
	store         Store
	retention     time.Duration
	rollupEnabled bool
	interval      time.Duration
	batchSize     int
	logger        *slog.Logger
}

// New builds a Janitor with defaults applied for any zero-valued field
// in cfg. logger must be non-nil; pass slog.Default() if you don't
// care.
func New(store Store, cfg config.StopwatchTelemetryConfig, logger *slog.Logger) *Janitor {
	retentionDays := cfg.RetentionDays
	if retentionDays == 0 {
		retentionDays = DefaultRetentionDays
	}
	interval := cfg.Janitor.Interval
	if interval == 0 {
		interval = DefaultJanitorInterval
	}
	batchSize := cfg.Janitor.BatchSize
	if batchSize == 0 {
		batchSize = DefaultJanitorBatchSize
	}
	return &Janitor{
		store:         store,
		retention:     time.Duration(retentionDays) * 24 * time.Hour,
		rollupEnabled: cfg.Rollup.Enabled,
		interval:      interval,
		batchSize:     batchSize,
		logger:        logger.With("component", "stopwatch_janitor"),
	}
}

// Tick performs one full pass: roll up the oldest unrolled days (when
// rollup is enabled), then prune raw rows past the retention cutoff,
// then write the heartbeat. Errors from rollup are logged and
// swallowed -- the prune still runs because raw retention is
// independently configured and important.
//
// The heartbeat is written even on partial failure, with last_status
// reflecting the worst sub-step outcome. This is deliberate: a stale
// heartbeat is the doctor signal for "janitor isn't ticking at all";
// last_status="err" with an error message is the signal for "janitor
// is ticking but something inside it is failing".
func (j *Janitor) Tick(ctx context.Context) error {
	startedAt := time.Now()
	var (
		rolledUp     int
		deleted      int
		rollupErr    error
		pruneErr     error
		overallError string
	)

	if j.rollupEnabled {
		rolledUp, rollupErr = j.rollupPass(ctx)
		if rollupErr != nil {
			j.logger.Warn("stopwatch rollup pass failed",
				"error", rollupErr,
				"rolled_up_so_far", rolledUp,
			)
		}
	}

	deleted, pruneErr = j.prunePass(ctx)
	if pruneErr != nil {
		j.logger.Warn("stopwatch prune pass failed",
			"error", pruneErr,
			"deleted_so_far", deleted,
		)
	}

	status := "ok"
	if rollupErr != nil || pruneErr != nil {
		status = "err"
		switch {
		case rollupErr != nil && pruneErr != nil:
			overallError = "rollup: " + rollupErr.Error() + "; prune: " + pruneErr.Error()
		case rollupErr != nil:
			overallError = rollupErr.Error()
		default:
			overallError = pruneErr.Error()
		}
	}

	hb := state.JanitorHeartbeat{
		Name:         janitorName,
		LastStatus:   status,
		LastError:    overallError,
		RowsRolledUp: rolledUp,
		RowsDeleted:  deleted,
	}
	if err := j.store.WriteJanitorHeartbeat(ctx, hb); err != nil {
		// Failing to write the heartbeat is the doctor-check's blind
		// spot; log loudly but don't escalate further.
		j.logger.Error("stopwatch janitor heartbeat write failed",
			"error", err,
			"intended_status", status,
		)
	}

	j.logger.Info("stopwatch janitor tick complete",
		"rolled_up", rolledUp,
		"deleted", deleted,
		"status", status,
		"elapsed_ms", time.Since(startedAt).Milliseconds(),
	)

	// Tick never propagates an error -- the heartbeat carries the
	// failure signal. Run() does not need to react.
	return nil
}

// rollupPass processes up to MaxRollupDaysPerTick unrolled days,
// oldest first. Returns the total rollup rows written and the first
// error encountered (subsequent days still attempted).
func (j *Janitor) rollupPass(ctx context.Context) (int, error) {
	days, err := j.store.StopwatchUnrolledDays(ctx, MaxRollupDaysPerTick)
	if err != nil {
		return 0, err
	}

	var firstErr error
	total := 0
	for _, day := range days {
		written, err := j.store.StopwatchRollupDay(ctx, day)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		total += written
	}
	return total, firstErr
}

// prunePass deletes raw rows older than retention, looping until
// either nothing left to delete or the per-tick batch budget is
// exhausted. Returns total rows deleted and the first error.
func (j *Janitor) prunePass(ctx context.Context) (int, error) {
	cutoff := time.Now().UTC().Add(-j.retention)
	total := 0
	for i := 0; i < MaxPruneBatchesPerTick; i++ {
		n, err := j.store.PruneStopwatchOlderThan(ctx, cutoff, j.batchSize)
		if err != nil {
			return total, err
		}
		total += n
		if n < j.batchSize {
			// Drained.
			break
		}
	}
	return total, nil
}

// Run blocks until ctx is canceled, ticking on j.interval. Ticks fire
// immediately on start so the first heartbeat lands without waiting a
// full interval; this matters for short-lived instances and for the
// "did the janitor even run" doctor check after a restart.
func (j *Janitor) Run(ctx context.Context) {
	j.logger.Info("stopwatch janitor starting",
		"interval", j.interval,
		"retention", j.retention,
		"rollup_enabled", j.rollupEnabled,
		"batch_size", j.batchSize,
	)

	// First tick immediately on start.
	_ = j.Tick(ctx)

	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			j.logger.Info("stopwatch janitor stopping", "reason", ctx.Err())
			return
		case <-ticker.C:
			_ = j.Tick(ctx)
		}
	}
}
