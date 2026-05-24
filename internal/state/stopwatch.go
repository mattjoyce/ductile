package state

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mattjoyce/ductile/internal/stopwatch"
)

// RecordStopwatch persists a supervisor-side timing Record for a single
// plugin invocation. Append-only — retries produce additional rows
// distinguished by Record.Attempt.
//
// The caller (dispatcher) extracts pipeline / pipelineInstanceID from the
// request context and passes them in; this keeps the writer free of any
// map-shape coupling. step_id lives on the Record (as StepName).
//
// On failure the writer returns an error. The caller is responsible for
// swallowing the error after logging — supervisor ledger writes are
// best-effort; losing a telemetry row must never crash the job.
//
// HAZARD (read carefully if you're adding an API endpoint that reads
// from job_stopwatch): subs_json contains plugin-supplied, unvalidated
// content. A compromised plugin can embed sensitive data in span names
// (e.g. {"name": "cc:4242...", "dur_ns": 1}). Treat subs_json as
// "result-class" data under the same scope shaping that already
// protects Result / LastError / Stderr — gate it behind
// jobs:result:ro in any future API response shape. See
// internal/api/handlers.go canSeeJobResults for the established
// pattern. As of 2026-05-24 subs_json is NOT exposed via any API
// endpoint; this comment exists so that property remains intentional.
func (s *Store) RecordStopwatch(
	ctx context.Context,
	jobID string,
	rec stopwatch.Record,
	pipeline, pipelineInstanceID string,
) error {
	if jobID == "" {
		return fmt.Errorf("RecordStopwatch: jobID is empty")
	}

	subsBytes, err := json.Marshal(rec.Subs)
	if err != nil {
		return fmt.Errorf("RecordStopwatch: marshal subs: %w", err)
	}

	const q = `
		INSERT INTO job_stopwatch (
			job_id, plugin, pipeline, step_id, pipeline_instance_id,
			attempt, enter_wall_ns, exit_wall_ns, dur_ns,
			runtime_pre_ns, runtime_post_ns, status, subs_json, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := s.db.ExecContext(ctx, q,
		jobID,
		rec.PluginID,
		pipeline,
		rec.StepName,
		pipelineInstanceID,
		rec.Attempt,
		rec.EnterWallNs,
		rec.ExitWallNs,
		rec.DurNs,
		rec.RuntimePreNs,
		rec.RuntimePostNs,
		rec.Status,
		string(subsBytes),
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("RecordStopwatch: insert: %w", err)
	}
	return nil
}
