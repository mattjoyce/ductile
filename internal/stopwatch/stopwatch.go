// Package stopwatch captures per-invocation timing as immutable values.
//
// The dispatcher (the supervisor of plugin invocations) is the only producer.
// Records persist to the ductile DB (job_stopwatch table) via the state
// package; this package owns only the value types and the capture mechanism.
// Plugins never time themselves; they may optionally emit sub-spans on the
// response, which the supervisor merges into the current Record before
// persisting.
//
// A Record is a value: no identity, no mutation, comparable by structural
// equality, JSON-serializable as a stable shape. Telemetry is system data,
// distinct from plugin domain payload — it lives in the supervisor's
// ledger, not in baggage.
package stopwatch

import (
	"log/slog"
	"time"
)

// SubsResponseKey is the optional response-payload key plugins use to emit
// sub-spans. The supervisor reads it, caps at MaxSubsPerRecord, and merges
// into the current Record.
const SubsResponseKey = "ductile_stopwatch_subs"

// MaxSubsPerRecord caps sub-spans accepted from a plugin. Excess are dropped
// with a single warn-log; the cap is a defensive bound, not a quota.
const MaxSubsPerRecord = 32

// Status values for a Record. A closed set; treat as opaque tokens.
const (
	StatusOK           = "ok"
	StatusError        = "err"
	StatusTimeout      = "timeout"
	StatusCaptureError = "capture_error"
)

// Record is the immutable timing value emitted by the supervisor for one
// plugin invocation. Field order is the wire contract; do not reorder.
type Record struct {
	PluginID      string           `json:"plugin_id"`
	StepName      string           `json:"step_name"`
	Attempt       int              `json:"attempt"`
	EnterWallNs   int64            `json:"enter_wall_ns"`
	ExitWallNs    int64            `json:"exit_wall_ns"`
	DurNs         int64            `json:"dur_ns"`
	RuntimePreNs  int64            `json:"runtime_pre_ns"`
	RuntimePostNs int64            `json:"runtime_post_ns"`
	Status        string           `json:"status"`
	Subs          []map[string]any `json:"subs"`
}

// Stopwatch is the per-invocation handle held by the supervisor between
// the pre-spawn moment and the post-spawn moment. It carries only what was
// observed at construction; the resulting Record is built in Finish.
//
// Not safe for concurrent use; each invocation gets its own.
type Stopwatch struct {
	pluginID  string
	stepName  string
	attempt   int
	enterWall time.Time // wall clock with monotonic reading
	preStart  time.Time // start of dispatcher pre-work (== enterWall by default)
	spawnAt   time.Time // moment spawnPlugin is called
}

// New constructs a Stopwatch at the moment the supervisor begins handling
// the invocation. enterWall is captured immediately; spawnAt is set later
// via MarkSpawn just before the plugin call.
func New(pluginID, stepName string, attempt int) *Stopwatch {
	now := time.Now()
	return &Stopwatch{
		pluginID:  pluginID,
		stepName:  stepName,
		attempt:   attempt,
		enterWall: now,
		preStart:  now,
	}
}

// MarkSpawn records the moment the supervisor is about to call spawnPlugin.
// runtime_pre_ns is the gap between New and MarkSpawn.
func (s *Stopwatch) MarkSpawn() {
	if s == nil {
		return
	}
	s.spawnAt = time.Now()
}

// Finish closes the Stopwatch and returns a Record. exitTime should be
// captured immediately after the spawn returns; postWork is any further
// supervisor work performed before Finish is called (or zero if none).
//
// If MarkSpawn was never called, runtime_pre_ns is reported as 0 and the
// full elapsed time is attributed to dur_ns.
func (s *Stopwatch) Finish(exitTime time.Time, status string, postWork time.Duration, subs []map[string]any) Record {
	if s == nil {
		return Record{Status: StatusCaptureError, Subs: []map[string]any{}}
	}

	var preNs, durNs int64
	if !s.spawnAt.IsZero() {
		preNs = s.spawnAt.Sub(s.preStart).Nanoseconds()
		durNs = exitTime.Sub(s.spawnAt).Nanoseconds()
	} else {
		durNs = exitTime.Sub(s.preStart).Nanoseconds()
	}

	return Record{
		PluginID:      s.pluginID,
		StepName:      s.stepName,
		Attempt:       s.attempt,
		EnterWallNs:   s.enterWall.UnixNano(),
		ExitWallNs:    exitTime.UnixNano(),
		DurNs:         durNs,
		RuntimePreNs:  preNs,
		RuntimePostNs: postWork.Nanoseconds(),
		Status:        status,
		Subs:          capSubs(subs, nil),
	}
}

// capSubs returns at most MaxSubsPerRecord entries. If logger is non-nil and
// the cap is exceeded, a single warning is emitted (not per dropped entry).
func capSubs(subs []map[string]any, logger *slog.Logger) []map[string]any {
	if len(subs) <= MaxSubsPerRecord {
		if subs == nil {
			return []map[string]any{}
		}
		return subs
	}
	if logger != nil {
		logger.Warn("stopwatch sub-spans exceeded cap",
			"received", len(subs),
			"cap", MaxSubsPerRecord,
			"dropped", len(subs)-MaxSubsPerRecord,
		)
	}
	return subs[:MaxSubsPerRecord]
}

// SubsFromResponse extracts plugin-emitted sub-spans defensively from an
// arbitrary value (typically pulled from the plugin response). Returns an
// empty slice for any malformed input; never panics. Logs once if the cap
// was exceeded (when logger is non-nil).
func SubsFromResponse(v any, logger *slog.Logger) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	raw, ok := v.([]any)
	if !ok {
		// Some JSON decoders may give []map[string]any directly.
		if direct, okDirect := v.([]map[string]any); okDirect {
			return capSubs(direct, logger)
		}
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return capSubs(out, logger)
}
