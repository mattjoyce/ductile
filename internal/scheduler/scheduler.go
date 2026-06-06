package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/scheduleexpr"
)

// RecoveryHookFunc is called by the scheduler when a job reaches a terminal
// state during crash recovery. The dispatcher wires this to its hook-firing
// machinery so that on-hook pipelines (e.g. discord_notify) are triggered for
// recovered dead jobs.
type RecoveryHookFunc func(ctx context.Context, job *queue.Job, signal string, payload map[string]any)

// Scheduler manages the scheduling and recovery of plugin jobs.
type Scheduler struct {
	cfg             *config.Config
	queue           QueueService // Use the interface here
	events          *events.Hub
	logger          *slog.Logger
	stopCh          chan struct{}
	wg              sync.WaitGroup
	supportsCommand func(pluginName, commandName string) bool
	tickCount       uint64
	recoveryHook    RecoveryHookFunc
}

const (
	pollCommand      = "poll"
	handleCommand    = "handle"
	catchUpSkip      = "skip"
	catchUpRunOnce   = "run_once"
	catchUpRunAll    = "run_all"
	catchUpRunAllMax = 100
)

// Option mutates scheduler behavior.
type Option func(*Scheduler)

// WithCommandSupportChecker validates plugin command support using the discovered registry.
func WithCommandSupportChecker(fn func(pluginName, commandName string) bool) Option {
	return func(s *Scheduler) {
		s.supportsCommand = fn
	}
}

// SetRecoveryHook sets the callback invoked when crash recovery marks a job as
// dead. This must be called before Start. The typical wiring is:
//
//	sched := scheduler.New(...)
//	disp := dispatch.New(...)
//	sched.SetRecoveryHook(disp.FireRecoveryHook)
//	sched.Start(ctx)
func (s *Scheduler) SetRecoveryHook(fn RecoveryHookFunc) {
	s.recoveryHook = fn
}

// New creates a new Scheduler instance.
func New(cfg *config.Config, q QueueService, hub *events.Hub, logger *slog.Logger, opts ...Option) *Scheduler {
	if hub == nil {
		hub = events.NewHub(128)
	}
	s := &Scheduler{
		cfg:    cfg,
		queue:  q,
		events: hub,
		logger: logger.With("component", "scheduler"),
		stopCh: make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Start begins the scheduler's tick loop and crash recovery.
func (s *Scheduler) Start(ctx context.Context) error {
	s.logger.Info("Starting scheduler")

	// Perform crash recovery on startup
	if err := s.recoverOrphanedJobs(ctx); err != nil {
		return fmt.Errorf("scheduler crash recovery failed: %w", err)
	}
	if err := s.runStartupCatchUp(ctx); err != nil {
		return fmt.Errorf("scheduler catch-up failed: %w", err)
	}

	s.wg.Add(1)
	go s.tickLoop(ctx)

	return nil
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	s.logger.Info("Stopping scheduler")
	close(s.stopCh)
	s.wg.Wait()
	s.logger.Info("Scheduler stopped")
}

// tickLoop is the main scheduling loop.
func (s *Scheduler) tickLoop(ctx context.Context) {
	defer s.wg.Done()

	// Initial tick immediately
	s.tick(ctx)

	ticker := time.NewTicker(s.cfg.Service.TickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.tick(ctx)
		case <-s.stopCh:
			return
		case <-ctx.Done():
			s.logger.Warn("Scheduler context cancelled, stopping tick loop")
			return
		}
	}
}

// tick performs a single scheduling pass.
func (s *Scheduler) tick(ctx context.Context) {
	s.tickCount++
	s.logger.Debug("Scheduler tick")
	s.events.Publish("scheduler.tick", map[string]any{
		"at": time.Now().UTC(),
	})

	// Sort plugin names for deterministic iteration (critical for testing)
	var pluginNames []string
	for name := range s.cfg.Plugins {
		pluginNames = append(pluginNames, name)
	}
	sort.Strings(pluginNames)

	for _, pluginName := range pluginNames {
		pluginConf := s.cfg.Plugins[pluginName]
		if !pluginConf.Enabled {
			continue
		}

		schedules := pluginConf.NormalizedSchedules()
		if len(schedules) == 0 {
			continue
		}

		for _, schedule := range schedules {
			s.processSchedule(ctx, pluginName, pluginConf, schedule)
		}
	}

	// Prune completed job logs
	if s.cfg.Service.JobLogRetention > 0 {
		if err := s.queue.PruneJobLogs(ctx, s.cfg.Service.JobLogRetention); err != nil {
			s.logger.Error("Failed to prune job logs", "error", err)
		}
	}

	// Prune terminal-state rows from the hot job_queue table. The canonical
	// record lives in job_log; the queue retention is a short visibility
	// window for "recently completed" rows.
	if s.cfg.Service.JobQueueRetention > 0 {
		if err := s.queue.PruneJobQueue(ctx, s.cfg.Service.JobQueueRetention); err != nil {
			s.logger.Error("Failed to prune job queue", "error", err)
		}
	}

	// Prune append-only fact logs over job_queue's mutable status. Retention
	// is independent of job_queue retention now that the cross-table FK is
	// gone — fact logs typically outlive the cache they log over.
	if s.cfg.Service.JobTransitionsRetention > 0 {
		if err := s.queue.PruneJobTransitions(ctx, s.cfg.Service.JobTransitionsRetention); err != nil {
			s.logger.Error("Failed to prune job transitions", "error", err)
		}
	}
	if s.cfg.Service.JobAttemptsRetention > 0 {
		if err := s.queue.PruneJobAttempts(ctx, s.cfg.Service.JobAttemptsRetention); err != nil {
			s.logger.Error("Failed to prune job attempts", "error", err)
		}
	}
	if s.cfg.Service.BreakerTransitionsRetention > 0 {
		if err := s.queue.PruneBreakerTransitions(ctx, s.cfg.Service.BreakerTransitionsRetention); err != nil {
			s.logger.Error("Failed to prune breaker transitions", "error", err)
		}
	}

}

func (s *Scheduler) runStartupCatchUp(ctx context.Context) error {
	now := time.Now().UTC()

	var pluginNames []string
	for name := range s.cfg.Plugins {
		pluginNames = append(pluginNames, name)
	}
	sort.Strings(pluginNames)

	for _, pluginName := range pluginNames {
		pluginConf := s.cfg.Plugins[pluginName]
		if !pluginConf.Enabled {
			continue
		}
		for _, schedule := range pluginConf.NormalizedSchedules() {
			if err := s.catchUpSchedule(ctx, pluginName, pluginConf, schedule, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) catchUpSchedule(ctx context.Context, pluginName string, pluginConf config.PluginConf, schedule config.ScheduleConfig, now time.Time) error {
	if strings.TrimSpace(schedule.Cron) != "" {
		return nil
	}

	baseInterval, err := parseScheduleEvery(schedule.Every)
	if err != nil {
		return nil
	}

	policy := strings.TrimSpace(schedule.CatchUp)
	if policy == "" {
		policy = catchUpSkip
	}
	if policy == catchUpSkip {
		return nil
	}

	scheduleID := strings.TrimSpace(schedule.ID)
	if scheduleID == "" {
		scheduleID = "default"
	}
	command := strings.TrimSpace(schedule.Command)
	if command == "" {
		command = pollCommand
	}

	entryState, err := s.currentScheduleEntryState(ctx, pluginName, scheduleID, command)
	if err != nil {
		return err
	}
	if entryState.LastFiredAt == nil {
		if entryState.LastSuccessAt == nil {
			return nil
		}
		last := entryState.LastSuccessAt.UTC()
		entryState.LastFiredAt = &last
	}

	lastFired := entryState.LastFiredAt.UTC()
	missed := countMissedRuns(lastFired, now, baseInterval)
	if missed <= 0 {
		return nil
	}

	toRun := 1
	if policy == catchUpRunAll {
		toRun = missed
		if toRun > catchUpRunAllMax {
			toRun = catchUpRunAllMax
		}
	}

	enqueued := 0
	for i := 0; i < toRun; i++ {
		slot := lastFired.Add(baseInterval * time.Duration(i+1))
		dedupeKey := fmt.Sprintf("%s:%s:%s:catchup:%s", pluginName, command, scheduleID, slot.Format(time.RFC3339))
		dedupeTTL := baseInterval
		if _, dedupeHit, enqueueErr := s.enqueueCatchUpJob(ctx, pluginName, command, pluginConf, dedupeKey, dedupeTTL, schedule.Payload); enqueueErr != nil {
			s.logger.Error("Failed to enqueue catch-up job", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", enqueueErr)
			continue
		} else if !dedupeHit {
			enqueued++
		}
	}

	advancedFired := lastFired
	if policy == catchUpRunOnce {
		advancedFired = now
	} else if toRun > 0 {
		advancedFired = lastFired.Add(baseInterval * time.Duration(toRun))
	}
	entryState.LastFiredAt = &advancedFired
	if entryState.NextRunAt == nil || entryState.NextRunAt.Before(advancedFired) {
		next := advancedFired.Add(baseInterval)
		entryState.NextRunAt = &next
	}
	if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
		return err
	}

	s.logger.Info("Processed startup catch-up", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "policy", policy, "missed", missed, "enqueued", enqueued)
	return nil
}

func (s *Scheduler) processSchedule(ctx context.Context, pluginName string, pluginConf config.PluginConf, schedule config.ScheduleConfig) {
	scheduleID := strings.TrimSpace(schedule.ID)
	if scheduleID == "" {
		scheduleID = "default"
	}

	command := strings.TrimSpace(schedule.Command)
	if command == "" {
		command = pollCommand
	}

	s.logger.Debug(
		"Processing schedule entry",
		"plugin", pluginName,
		"schedule_id", scheduleID,
		"command", command,
		"schedule_every", schedule.Every,
		"schedule_cron", schedule.Cron,
		"jitter", schedule.Jitter,
	)

	entryState, err := s.currentScheduleEntryState(ctx, pluginName, scheduleID, command)
	if err != nil {
		s.logger.Error("Failed to load schedule entry state", "plugin", pluginName, "schedule_id", scheduleID, "error", err)
		return
	}
	if entryState.Command != command {
		entryState.Command = command
		entryState.LastSuccessJobID = nil
		entryState.LastSuccessAt = nil
		entryState.NextRunAt = nil
		if entryState.Status == queue.ScheduleEntryPausedInvalid {
			entryState.Status = queue.ScheduleEntryActive
			entryState.Reason = nil
		}
		if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
			s.logger.Error("Failed to persist schedule command change", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
			return
		}
	}
	if entryState.Status == queue.ScheduleEntryPausedManual {
		s.events.Publish("scheduler.skipped", map[string]any{
			"plugin":      pluginName,
			"schedule_id": scheduleID,
			"command":     command,
			"reason":      "schedule_paused_manual",
		})
		return
	}
	if entryState.Status == queue.ScheduleEntryExhausted {
		s.events.Publish("scheduler.skipped", map[string]any{
			"plugin":      pluginName,
			"schedule_id": scheduleID,
			"command":     command,
			"reason":      "schedule_exhausted",
		})
		return
	}

	hasEvery := strings.TrimSpace(schedule.Every) != ""
	hasCron := strings.TrimSpace(schedule.Cron) != ""
	hasAt := strings.TrimSpace(schedule.At) != ""
	hasAfter := schedule.After > 0
	oneShot := hasAt || hasAfter
	var baseInterval time.Duration
	var cronExpr *scheduleexpr.CronExpression
	var oneShotRunAt time.Time

	if hasEvery || hasCron {
		parsedInterval, everyMode, parseErr := parseScheduleInterval(schedule.Every, schedule.Cron)
		if parseErr != nil {
			reason := "invalid_schedule_config"
			s.logger.Error("Invalid schedule config for plugin", "plugin", pluginName, "schedule_id", scheduleID, "every", schedule.Every, "cron", schedule.Cron, "error", parseErr)
			if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
				s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
			}
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      reason,
			})
			return
		}
		baseInterval = parsedInterval
		hasEvery = everyMode
	}

	if hasCron {
		parsedCron, parseErr := scheduleexpr.ParseCron(schedule.Cron)
		if parseErr != nil {
			reason := "invalid_schedule_config"
			s.logger.Error("Invalid cron schedule for plugin", "plugin", pluginName, "schedule_id", scheduleID, "cron", schedule.Cron, "error", parseErr)
			if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
				s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
			}
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      reason,
			})
			return
		}
		cronExpr = &parsedCron
	}
	if oneShot {
		if hasAt {
			parsedAt, parseErr := time.Parse(time.RFC3339, schedule.At)
			if parseErr != nil {
				reason := "invalid_schedule_config"
				if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
					s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
				}
				s.events.Publish("scheduler.skipped", map[string]any{
					"plugin":      pluginName,
					"schedule_id": scheduleID,
					"command":     command,
					"reason":      reason,
				})
				return
			}
			oneShotRunAt = parsedAt.UTC()
		} else if hasAfter {
			oneShotRunAt = time.Now().UTC().Add(schedule.After)
		}
	}

	if command == handleCommand {
		reason := "scheduled_handle_disallowed"
		if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
			s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
		}
		s.events.Publish("scheduler.skipped", map[string]any{
			"plugin":      pluginName,
			"schedule_id": scheduleID,
			"command":     command,
			"reason":      reason,
		})
		s.logger.Error("Scheduled command is not allowed", "plugin", pluginName, "schedule_id", scheduleID, "command", command)
		return
	}

	if s.supportsCommand != nil && !s.supportsCommand(pluginName, command) {
		reason := "command_not_supported"
		if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
			s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
		}
		s.events.Publish("scheduler.skipped", map[string]any{
			"plugin":      pluginName,
			"schedule_id": scheduleID,
			"command":     command,
			"reason":      reason,
		})
		s.logger.Warn("Scheduled command not found in plugin manifest", "plugin", pluginName, "schedule_id", scheduleID, "command", command)
		return
	}

	if entryState.Status == queue.ScheduleEntryPausedInvalid {
		if err := s.activateScheduleEntry(ctx, entryState); err != nil {
			s.logger.Error("Failed to reactivate schedule entry", "plugin", pluginName, "schedule_id", scheduleID, "error", err)
			return
		}
		entryState.Status = queue.ScheduleEntryActive
		entryState.Reason = nil
	}

	latestCompleted, err := s.reconcileCircuitBreaker(ctx, pluginName, command, pluginConf)
	if err != nil {
		s.logger.Error("Failed to reconcile circuit breaker state", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		return
	}

	if hasEvery {
		if stateChanged, timingErr := s.reconcileScheduleTiming(&entryState, latestCompleted, baseInterval, schedule.Jitter); timingErr != nil {
			s.logger.Error("Failed to reconcile schedule timing", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", timingErr)
			return
		} else if stateChanged {
			if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
				s.logger.Error("Failed to persist schedule timing state", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
				return
			}
		}
		now := time.Now().UTC()
		if entryState.NextRunAt != nil && now.Before(*entryState.NextRunAt) {
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      "not_due",
				"next_run_at": entryState.NextRunAt.Format(time.RFC3339Nano),
			})
			s.logger.Debug("Skipped scheduled job (not due)", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "next_run_at", entryState.NextRunAt.Format(time.RFC3339Nano))
			return
		}
	} else if cronExpr != nil {
		cronNow, cronErr := s.scheduleCronNow(schedule)
		if cronErr != nil {
			reason := "invalid_schedule_config"
			s.logger.Error("Failed to resolve cron timezone", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", cronErr)
			if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
				s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
			}
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      reason,
			})
			return
		}
		due, nextRunAt, changed, cronErr := s.evaluateCronDueState(entryState, *cronExpr, cronNow)
		if cronErr != nil {
			reason := "invalid_schedule_config"
			s.logger.Error("Failed to evaluate cron schedule", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", cronErr)
			if upsertErr := s.pauseScheduleInvalid(ctx, entryState, reason); upsertErr != nil {
				s.logger.Error("Failed to persist invalid schedule state", "plugin", pluginName, "schedule_id", scheduleID, "error", upsertErr)
			}
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      reason,
			})
			return
		}
		if changed {
			entryState.NextRunAt = &nextRunAt
			if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
				s.logger.Error("Failed to persist cron timing state", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
				return
			}
		}
		if !due {
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      "not_due",
				"next_run_at": nextRunAt.Format(time.RFC3339Nano),
			})
			s.logger.Debug("Skipped scheduled job (not due)", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "next_run_at", nextRunAt.Format(time.RFC3339Nano))
			return
		}
	} else if oneShot {
		now := time.Now().UTC()
		if entryState.NextRunAt == nil {
			entryState.NextRunAt = &oneShotRunAt
			if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
				s.logger.Error("Failed to persist one-shot schedule timing state", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
				return
			}
		} else {
			oneShotRunAt = entryState.NextRunAt.UTC()
		}
		if now.Before(oneShotRunAt) {
			s.events.Publish("scheduler.skipped", map[string]any{
				"plugin":      pluginName,
				"schedule_id": scheduleID,
				"command":     command,
				"reason":      "not_due",
				"next_run_at": oneShotRunAt.Format(time.RFC3339Nano),
			})
			s.logger.Debug("Skipped scheduled job (not due)", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "next_run_at", oneShotRunAt.Format(time.RFC3339Nano))
			return
		}
	}

	if allowed, reason, err := scheduleConstraintsAllowAt(schedule, time.Now().UTC()); err != nil {
		s.logger.Error("Failed schedule constraints check", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		return
	} else if !allowed {
		s.events.Publish("scheduler.skipped", map[string]any{
			"plugin":      pluginName,
			"schedule_id": scheduleID,
			"command":     command,
			"reason":      reason,
		})
		s.logger.Debug("Skipped scheduled job (constraints)", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "reason", reason)
		return
	}

	if allowed, reason, err := s.canSchedule(ctx, pluginName, command, pluginConf, schedule); err != nil {
		s.logger.Error("Failed scheduling checks", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		return
	} else if !allowed {
		s.events.Publish("scheduler.skipped", map[string]any{
			"plugin":      pluginName,
			"schedule_id": scheduleID,
			"command":     command,
			"reason":      reason,
		})
		s.logger.Info("Skipped scheduled job", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "reason", reason)
		return
	}

	dedupeTTL := s.cfg.Service.TickInterval
	if hasEvery {
		dedupeTTL = baseInterval
	}
	dedupeHit, err := s.enqueueScheduledJob(ctx, pluginName, scheduleID, command, pluginConf, dedupeTTL, schedule.Payload)
	if err != nil {
		s.logger.Error("Failed to enqueue scheduled job", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		return
	}
	now := time.Now().UTC()
	entryState.LastFiredAt = &now
	if hasEvery && dedupeHit {
		if err := s.advanceScheduleNextRun(ctx, &entryState, baseInterval, schedule.Jitter); err != nil {
			s.logger.Error("Failed to advance schedule after dedupe hit", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		}
	}
	if !hasEvery && cronExpr != nil {
		cronNow, cronErr := s.scheduleCronNow(schedule)
		if cronErr != nil {
			s.logger.Error("Failed to resolve cron timezone", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", cronErr)
		} else if err := s.advanceCronNextRun(ctx, &entryState, *cronExpr, cronNow); err != nil {
			s.logger.Error("Failed to advance cron schedule", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		}
	}
	if oneShot {
		reason := "one_shot_exhausted"
		entryState.Status = queue.ScheduleEntryExhausted
		entryState.Reason = &reason
		entryState.NextRunAt = nil
		if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
			s.logger.Error("Failed to persist exhausted one-shot state", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		}
		return
	}
	if hasEvery && !dedupeHit {
		if err := s.queue.UpsertScheduleEntryState(ctx, entryState); err != nil {
			s.logger.Error("Failed to persist schedule fired state", "plugin", pluginName, "schedule_id", scheduleID, "command", command, "error", err)
		}
	}
}

func (s *Scheduler) evaluateCronDueState(
	entryState queue.ScheduleEntryState,
	expr scheduleexpr.CronExpression,
	now time.Time,
) (bool, time.Time, bool, error) {
	nowSlot := now.Truncate(time.Minute)
	if entryState.NextRunAt == nil {
		if expr.Matches(nowSlot) {
			return true, nowSlot, false, nil
		}
		next, err := expr.NextAfter(nowSlot.Add(-time.Minute))
		if err != nil {
			return false, time.Time{}, false, err
		}
		return false, next, true, nil
	}
	nextRun := entryState.NextRunAt.Truncate(time.Minute)
	return !nowSlot.Before(nextRun), nextRun, false, nil
}

func (s *Scheduler) advanceCronNextRun(ctx context.Context, state *queue.ScheduleEntryState, expr scheduleexpr.CronExpression, now time.Time) error {
	if state == nil {
		return nil
	}
	nextRunAt, err := expr.NextAfter(now)
	if err != nil {
		return err
	}
	state.NextRunAt = &nextRunAt
	return s.queue.UpsertScheduleEntryState(ctx, *state)
}

func (s *Scheduler) currentScheduleEntryState(ctx context.Context, pluginName, scheduleID, command string) (queue.ScheduleEntryState, error) {
	state, err := s.queue.GetScheduleEntryState(ctx, pluginName, scheduleID)
	if err != nil {
		return queue.ScheduleEntryState{}, err
	}
	if state == nil {
		return queue.ScheduleEntryState{
			Plugin:     pluginName,
			ScheduleID: scheduleID,
			Command:    command,
			Status:     queue.ScheduleEntryActive,
		}, nil
	}
	if state.Command == "" {
		state.Command = command
	}
	if state.Status == "" {
		state.Status = queue.ScheduleEntryActive
	}
	return *state, nil
}

func (s *Scheduler) pauseScheduleInvalid(ctx context.Context, state queue.ScheduleEntryState, reason string) error {
	reasonCopy := reason
	state.Status = queue.ScheduleEntryPausedInvalid
	state.Reason = &reasonCopy
	return s.queue.UpsertScheduleEntryState(ctx, state)
}

func (s *Scheduler) activateScheduleEntry(ctx context.Context, state queue.ScheduleEntryState) error {
	state.Status = queue.ScheduleEntryActive
	state.Reason = nil
	return s.queue.UpsertScheduleEntryState(ctx, state)
}

func (s *Scheduler) advanceScheduleNextRun(ctx context.Context, state *queue.ScheduleEntryState, baseInterval, jitter time.Duration) error {
	if state == nil {
		return nil
	}

	nextRunAt := time.Now().UTC().Add(calculateJitteredInterval(baseInterval, jitter))
	state.NextRunAt = &nextRunAt
	return s.queue.UpsertScheduleEntryState(ctx, *state)
}

// enqueueScheduledJob creates and enqueues a command job for a given schedule entry.
func (s *Scheduler) enqueueScheduledJob(ctx context.Context, pluginName, scheduleID, command string, pluginConf config.PluginConf, dedupeTTL time.Duration, payload map[string]any) (bool, error) {
	dedupeKey := fmt.Sprintf("%s:%s:%s", pluginName, command, scheduleID)

	rawPayload := []byte(`{}`)
	if len(payload) > 0 {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return false, fmt.Errorf("marshal schedule payload: %w", err)
		}
		rawPayload = encoded
	}

	req := queue.EnqueueRequest{
		Plugin:      pluginName,
		Command:     command,
		Payload:     rawPayload,
		SubmittedBy: s.cfg.Service.Name,
		DedupeKey:   &dedupeKey,
		DedupeTTL:   &dedupeTTL,
	}
	if pluginConf.Retry != nil {
		req.MaxAttempts = pluginConf.Retry.MaxAttempts
	}

	jobID, err := s.queue.Enqueue(ctx, req)
	if err != nil {
		var dedupeErr *queue.DedupeDropError
		if errors.As(err, &dedupeErr) {
			s.logger.Info(
				"Skipped enqueue due to dedupe hit",
				"plugin", pluginName,
				"command", command,
				"schedule_id", scheduleID,
				"dedupe_key", dedupeErr.DedupeKey,
				"existing_job_id", dedupeErr.ExistingJobID,
			)
			return true, nil
		}
		return false, fmt.Errorf("enqueue scheduled job for %s/%s: %w", pluginName, command, err)
	}
	s.events.Publish("scheduler.scheduled", map[string]any{
		"job_id":       jobID,
		"plugin":       pluginName,
		"command":      command,
		"schedule_id":  scheduleID,
		"submitted_by": s.cfg.Service.Name,
	})
	s.logger.Info("Enqueued scheduled job", "plugin", pluginName, "command", command, "schedule_id", scheduleID, "job_id", jobID, "dedupe_key", dedupeKey)
	return false, nil
}

func (s *Scheduler) enqueueCatchUpJob(
	ctx context.Context,
	pluginName, command string,
	pluginConf config.PluginConf,
	dedupeKey string,
	dedupeTTL time.Duration,
	payload map[string]any,
) (string, bool, error) {
	rawPayload := []byte(`{}`)
	if len(payload) > 0 {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", false, fmt.Errorf("marshal schedule payload: %w", err)
		}
		rawPayload = encoded
	}

	req := queue.EnqueueRequest{
		Plugin:      pluginName,
		Command:     command,
		Payload:     rawPayload,
		SubmittedBy: s.cfg.Service.Name,
		DedupeKey:   &dedupeKey,
		DedupeTTL:   &dedupeTTL,
	}
	if pluginConf.Retry != nil {
		req.MaxAttempts = pluginConf.Retry.MaxAttempts
	}

	jobID, err := s.queue.Enqueue(ctx, req)
	if err != nil {
		var dedupeErr *queue.DedupeDropError
		if errors.As(err, &dedupeErr) {
			return "", true, nil
		}
		return "", false, err
	}
	s.events.Publish("scheduler.scheduled", map[string]any{
		"job_id":       jobID,
		"plugin":       pluginName,
		"command":      command,
		"schedule_id":  "catch_up",
		"submitted_by": s.cfg.Service.Name,
	})
	return jobID, false, nil
}

func (s *Scheduler) reconcileCircuitBreaker(ctx context.Context, pluginName, command string, pluginConf config.PluginConf) (*queue.CommandResult, error) {
	cb, err := s.currentCircuitBreaker(ctx, pluginName, command)
	if err != nil {
		return nil, err
	}

	latest, err := s.queue.LatestCompletedCommandResult(ctx, pluginName, command, s.cfg.Service.Name)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, nil
	}
	if cb.LastJobID != nil && *cb.LastJobID == latest.JobID {
		return latest, nil
	}

	threshold := breakerThreshold(pluginConf)
	previousState := cb.State

	if latest.Status == queue.StatusSucceeded {
		cb.State = queue.CircuitClosed
		cb.FailureCount = 0
		cb.OpenedAt = nil
		cb.LastFailure = nil
	} else {
		cb.FailureCount++
		completedAt := latest.CompletedAt.UTC()
		cb.LastFailure = &completedAt
		if cb.FailureCount >= threshold {
			cb.State = queue.CircuitOpen
			cb.OpenedAt = &completedAt
		} else {
			cb.State = queue.CircuitClosed
			cb.OpenedAt = nil
		}
	}

	cb.LastJobID = &latest.JobID
	if err := s.queue.UpsertCircuitBreaker(ctx, cb); err != nil {
		return nil, err
	}

	if cb.State != previousState {
		reason := queue.CircuitTransitionSuccess
		if cb.State == queue.CircuitOpen {
			reason = queue.CircuitTransitionFailureThreshold
		}
		if err := s.queue.RecordCircuitBreakerTransition(ctx, queue.CircuitBreakerTransition{
			Plugin:       pluginName,
			Command:      command,
			FromState:    &previousState,
			ToState:      cb.State,
			FailureCount: cb.FailureCount,
			Reason:       reason,
			JobID:        &latest.JobID,
		}); err != nil {
			return nil, err
		}
		s.events.Publish("scheduler.circuit_state_changed", map[string]any{
			"plugin":         pluginName,
			"command":        command,
			"previous_state": previousState,
			"state":          cb.State,
			"failure_count":  cb.FailureCount,
		})
		s.logger.Info(
			"Circuit breaker state changed",
			"plugin", pluginName,
			"command", command,
			"previous_state", previousState,
			"state", cb.State,
			"failure_count", cb.FailureCount,
		)
	}

	return latest, nil
}

func (s *Scheduler) reconcileScheduleTiming(
	state *queue.ScheduleEntryState,
	latestCompleted *queue.CommandResult,
	baseInterval time.Duration,
	jitter time.Duration,
) (bool, error) {
	if state == nil || latestCompleted == nil {
		return false, nil
	}
	if latestCompleted.Status != queue.StatusSucceeded {
		return false, nil
	}
	if state.LastSuccessJobID != nil && *state.LastSuccessJobID == latestCompleted.JobID {
		return false, nil
	}

	completedAt := latestCompleted.CompletedAt.UTC()
	nextRunAt := completedAt.Add(calculateJitteredInterval(baseInterval, jitter))
	jobID := latestCompleted.JobID

	state.LastSuccessJobID = &jobID
	state.LastSuccessAt = &completedAt
	state.NextRunAt = &nextRunAt
	return true, nil
}

func (s *Scheduler) canSchedule(ctx context.Context, pluginName, command string, pluginConf config.PluginConf, schedule config.ScheduleConfig) (bool, string, error) {
	cb, err := s.currentCircuitBreaker(ctx, pluginName, command)
	if err != nil {
		return false, "", err
	}

	if cb.State == queue.CircuitOpen {
		if !cooldownElapsed(cb, breakerResetAfter(pluginConf), time.Now().UTC()) {
			return false, "circuit_open", nil
		}

		previousState := cb.State
		cb.State = queue.CircuitHalfOpen
		if err := s.queue.UpsertCircuitBreaker(ctx, cb); err != nil {
			return false, "", err
		}
		if err := s.queue.RecordCircuitBreakerTransition(ctx, queue.CircuitBreakerTransition{
			Plugin:       pluginName,
			Command:      command,
			FromState:    &previousState,
			ToState:      cb.State,
			FailureCount: cb.FailureCount,
			Reason:       queue.CircuitTransitionCooldownElapsed,
			JobID:        cb.LastJobID,
		}); err != nil {
			return false, "", err
		}
		s.events.Publish("scheduler.circuit_half_open", map[string]any{
			"plugin":  pluginName,
			"command": command,
		})
		s.logger.Info("Circuit breaker moved to half-open", "plugin", pluginName, "command", command)
	}

	ifRunning := strings.TrimSpace(schedule.IfRunning)
	if ifRunning == "" {
		ifRunning = "skip"
	}
	switch ifRunning {
	case "skip":
		outstanding, err := s.outstandingForPolicy(ctx, pluginName, command)
		if err != nil {
			return false, "", err
		}
		if outstanding > 0 {
			return false, "already_running", nil
		}
	case "queue":
		maxOutstanding := pluginConf.MaxOutstandingPolls
		if maxOutstanding <= 0 {
			maxOutstanding = 1
		}
		outstanding, err := s.outstandingForPolicy(ctx, pluginName, command)
		if err != nil {
			return false, "", err
		}
		if outstanding >= maxOutstanding {
			return false, "outstanding_limit", nil
		}
	case "cancel":
		outstanding, err := s.outstandingForPolicy(ctx, pluginName, command)
		if err != nil {
			return false, "", err
		}
		if outstanding > 0 {
			cancelled, cancelErr := s.queue.CancelOutstandingJobs(ctx, pluginName, command, "cancelled by scheduler (if_running=cancel)")
			if cancelErr != nil {
				return false, "", cancelErr
			}
			s.logger.Info("Cancelled outstanding scheduled jobs", "plugin", pluginName, "command", command, "cancelled", cancelled)
		}
	default:
		return false, "", fmt.Errorf("invalid if_running policy %q", ifRunning)
	}

	return true, "", nil
}

func (s *Scheduler) currentCircuitBreaker(ctx context.Context, pluginName, command string) (queue.CircuitBreaker, error) {
	cb, err := s.queue.GetCircuitBreaker(ctx, pluginName, command)
	if err != nil {
		return queue.CircuitBreaker{}, err
	}
	if cb == nil {
		return queue.CircuitBreaker{
			Plugin:       pluginName,
			Command:      command,
			State:        queue.CircuitClosed,
			FailureCount: 0,
		}, nil
	}
	if cb.State == "" {
		cb.State = queue.CircuitClosed
	}
	return *cb, nil
}

func breakerThreshold(pluginConf config.PluginConf) int {
	// Single-sourced through the shared resolver — same default source as the
	// `--effective` view, so the breaker threshold cannot silently drift (card #75).
	return config.ResolvePluginConf(pluginConf, 0).BreakerThreshold()
}

func breakerResetAfter(pluginConf config.PluginConf) time.Duration {
	return config.ResolvePluginConf(pluginConf, 0).BreakerResetAfter()
}

func cooldownElapsed(cb queue.CircuitBreaker, resetAfter time.Duration, now time.Time) bool {
	if cb.OpenedAt == nil {
		return true
	}
	return !now.Before(cb.OpenedAt.Add(resetAfter))
}

// recoverOrphanedJobs scans for and recovers jobs marked as "running" at startup.
func (s *Scheduler) recoverOrphanedJobs(ctx context.Context) error {
	s.logger.Info("Performing crash recovery for orphaned jobs")

	runningJobs, err := s.queue.FindJobsByStatus(ctx, queue.StatusRunning)
	if err != nil {
		return fmt.Errorf("failed to find running jobs for recovery: %w", err)
	}

	if len(runningJobs) == 0 {
		s.logger.Info("No orphaned jobs found.")
		return nil
	}

	s.logger.Warn("Found orphaned jobs, attempting recovery", "count", len(runningJobs))

	for _, job := range runningJobs {
		s.logger.Warn(
			"Recovering orphaned job",
			"job_id", job.ID,
			"plugin", job.Plugin,
			"command", job.Command,
			"current_attempt", job.Attempt,
			"max_attempts", job.MaxAttempts,
		)

		job.Attempt++ // Increment attempt counter for recovery

		var newStatus queue.Status
		var lastErrorMsg string
		var nextRetryAt *time.Time

		if job.Attempt <= job.MaxAttempts {
			newStatus = queue.StatusQueued
			s.logger.Warn(
				"Re-queueing orphaned job",
				"job_id", job.ID,
				"plugin", job.Plugin,
				"command", job.Command,
				"new_attempt", job.Attempt,
				"status", newStatus,
			)

			err := s.queue.UpdateJobForRecovery(ctx, job.ID, newStatus, job.Attempt, nextRetryAt, lastErrorMsg)
			if err != nil {
				s.logger.Error(
					"Failed to re-queue orphaned job during recovery",
					"job_id", job.ID,
					"error", err,
				)
			}
		} else {
			newStatus = queue.StatusDead
			lastErrorMsg = fmt.Sprintf("Job marked dead during crash recovery: max attempts (%d) reached", job.MaxAttempts)
			s.logger.Error(
				"Marking orphaned job as dead (max attempts reached)",
				"job_id", job.ID,
				"plugin", job.Plugin,
				"command", job.Command,
				"final_attempt", job.Attempt,
				"status", newStatus,
				"error", lastErrorMsg,
			)

			// Update the attempt counter first, then use CompleteWithResult
			// to set completed_at and write the job_log entry — the same
			// terminal-state path as normally-failed jobs.
			if err := s.queue.UpdateJobForRecovery(ctx, job.ID, newStatus, job.Attempt, nil, lastErrorMsg); err != nil {
				s.logger.Error(
					"Failed to update orphaned job during recovery",
					"job_id", job.ID,
					"error", err,
				)
				continue
			}
			if err := s.queue.CompleteWithResult(ctx, job.ID, newStatus, nil, &lastErrorMsg, nil); err != nil {
				s.logger.Error(
					"Failed to complete dead orphaned job",
					"job_id", job.ID,
					"error", err,
				)
			}

			// Publish event to the hub so subscribers see the terminal transition.
			s.events.Publish("job.failed", map[string]any{
				"job_id":   job.ID,
				"plugin":   job.Plugin,
				"command":  job.Command,
				"status":   string(newStatus),
				"error":    lastErrorMsg,
				"recovery": true,
			})

			// Fire on-hook pipelines (e.g. job-failure-notify → discord_notify).
			if s.recoveryHook != nil {
				s.recoveryHook(ctx, job, "job.failed", map[string]any{
					"job_id":   job.ID,
					"plugin":   job.Plugin,
					"command":  job.Command,
					"status":   string(newStatus),
					"error":    lastErrorMsg,
					"recovery": true,
				})
			}
		}
	}

	return nil
}

// calculateJitteredInterval adds a random jitter to the base interval.
func calculateJitteredInterval(baseInterval time.Duration, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return baseInterval
	}
	// Generate a random duration between 0 and jitter.
	// #nosec G404 -- jitter is non-cryptographic scheduling noise.
	randomJitter := time.Duration(rand.Int63n(jitter.Nanoseconds()))
	return baseInterval + randomJitter
}

// parseScheduleEvery converts the 'every' string from config to a base duration.
func parseScheduleInterval(every, cronExpr string) (time.Duration, bool, error) {
	hasEvery := strings.TrimSpace(every) != ""
	hasCron := strings.TrimSpace(cronExpr) != ""
	if hasEvery == hasCron {
		return 0, false, fmt.Errorf("schedule must set exactly one of every or cron")
	}
	if hasEvery {
		d, err := config.ParseInterval(every)
		if err != nil {
			return 0, false, err
		}
		return d, true, nil
	}
	if _, err := scheduleexpr.ParseCron(cronExpr); err != nil {
		return 0, false, err
	}
	return 0, false, nil
}

func parseScheduleEvery(every string) (time.Duration, error) {
	return config.ParseInterval(every)
}

func countMissedRuns(lastFiredAt, now time.Time, interval time.Duration) int {
	if interval <= 0 {
		return 0
	}
	last := lastFiredAt.UTC()
	current := now.UTC()
	if !current.After(last) {
		return 0
	}
	nextDue := last.Add(interval)
	if current.Before(nextDue) {
		return 0
	}
	return int(current.Sub(nextDue)/interval) + 1
}

func scheduleConstraintsAllowAt(schedule config.ScheduleConfig, now time.Time) (bool, string, error) {
	loc := time.UTC
	if tz := strings.TrimSpace(schedule.Timezone); tz != "" {
		loaded, err := time.LoadLocation(tz)
		if err != nil {
			return false, "", err
		}
		loc = loaded
	}
	localNow := now.In(loc)

	if len(schedule.NotOn) > 0 {
		day := localNow.Weekday()
		for _, token := range schedule.NotOn {
			parsed, err := parseWeekdayConstraint(token)
			if err != nil {
				return false, "", err
			}
			if parsed == day {
				return false, "outside_day_constraint", nil
			}
		}
	}

	window := strings.TrimSpace(schedule.OnlyBetween)
	if window == "" {
		return true, "", nil
	}
	parts := strings.Split(window, "-")
	if len(parts) != 2 {
		return false, "", fmt.Errorf("invalid only_between %q", schedule.OnlyBetween)
	}
	startMin, err := parseClockConstraint(parts[0])
	if err != nil {
		return false, "", err
	}
	endMin, err := parseClockConstraint(parts[1])
	if err != nil {
		return false, "", err
	}
	if startMin == endMin {
		return false, "", fmt.Errorf("invalid only_between %q: start and end cannot be equal", schedule.OnlyBetween)
	}

	nowMin := localNow.Hour()*60 + localNow.Minute()
	if startMin < endMin {
		if nowMin < startMin || nowMin >= endMin {
			return false, "outside_time_window", nil
		}
		return true, "", nil
	}

	// Overnight window, e.g. 22:00-06:00.
	if nowMin >= startMin || nowMin < endMin {
		return true, "", nil
	}
	return false, "outside_time_window", nil
}

func parseClockConstraint(raw string) (int, error) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("expected HH:MM")
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

func (s *Scheduler) scheduleCronNow(schedule config.ScheduleConfig) (time.Time, error) {
	loc := time.Local
	if tz := strings.TrimSpace(schedule.Timezone); tz != "" {
		loaded, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, err
		}
		loc = loaded
	}
	return time.Now().In(loc), nil
}

func parseWeekdayConstraint(token any) (time.Weekday, error) {
	switch v := token.(type) {
	case int:
		return parseWeekdayIntConstraint(v)
	case int64:
		return parseWeekdayIntConstraint(int(v))
	case float64:
		if v != float64(int(v)) {
			return 0, fmt.Errorf("weekday number must be integer: %v", v)
		}
		return parseWeekdayIntConstraint(int(v))
	case string:
		return parseWeekdayStringConstraint(v)
	default:
		return 0, fmt.Errorf("unsupported weekday type %T", token)
	}
}

func parseWeekdayIntConstraint(v int) (time.Weekday, error) {
	if v == 7 {
		return time.Sunday, nil
	}
	if v < 0 || v > 6 {
		return 0, fmt.Errorf("weekday number %d out of range [0,6] or 7 for sunday", v)
	}
	return time.Weekday(v), nil
}

func parseWeekdayStringConstraint(raw string) (time.Weekday, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "sun", "sunday":
		return time.Sunday, nil
	case "mon", "monday":
		return time.Monday, nil
	case "tue", "tues", "tuesday":
		return time.Tuesday, nil
	case "wed", "wednesday":
		return time.Wednesday, nil
	case "thu", "thurs", "thursday":
		return time.Thursday, nil
	case "fri", "friday":
		return time.Friday, nil
	case "sat", "saturday":
		return time.Saturday, nil
	default:
		return 0, fmt.Errorf("unknown weekday %q", raw)
	}
}
