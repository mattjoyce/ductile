package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mattjoyce/ductile/internal/state"
)

// StopwatchReader is the read interface the /stopwatch/{plugin}
// endpoint needs. Production code wires *state.Store; tests use a
// lightweight mock.
type StopwatchReader interface {
	StopwatchRowsForPlugin(ctx context.Context, plugin string, since time.Time) ([]state.StopwatchAggregationRow, error)
}

// StopwatchResponse is the per-plugin latency aggregation returned by
// GET /stopwatch/{plugin}. Aggregated by pipeline step_id (the bucket
// the stopwatch ledger actually uses); the response field name "step"
// is honest about this rather than pretending the data is per-command.
//
// SubsUnavailable is empty when sub-spans were not requested or were
// returned. When the caller requested ?include_subs=true but the
// principal scope did not permit exposing plugin-supplied content,
// SubsUnavailable carries the reason (currently the single value
// "insufficient_scope") so the caller can tell "no sub-spans were
// captured" from "you were not allowed to see them".
type StopwatchResponse struct {
	Plugin          string               `json:"plugin"`
	Window          string               `json:"window"`
	CapturedAt      time.Time            `json:"captured_at"`
	Steps           []StopwatchStepStats `json:"steps"`
	SubsUnavailable string               `json:"subs_unavailable,omitempty"`
}

// Reason values for StopwatchResponse.SubsUnavailable. Single value
// today; named constant so future reasons (e.g. "ledger_capped",
// "subs_disabled_in_config") can be added without callers special-
// casing string literals.
const (
	SubsUnavailableInsufficientScope = "insufficient_scope"
)

// StopwatchStepStats holds percentile aggregation for one pipeline
// step (or for the empty step bucket when the plugin was invoked
// directly via /plugin/{name}/{command}).
type StopwatchStepStats struct {
	Step        string              `json:"step"`
	SampleCount int                 `json:"sample_count"`
	P50Ms       int                 `json:"p50_ms"`
	P95Ms       int                 `json:"p95_ms"`
	P99Ms       int                 `json:"p99_ms"`
	TrendP95Ms  []int               `json:"trend_p95_ms"`
	Subs        []StopwatchSubStats `json:"subs,omitempty"`
}

// StopwatchSubStats holds percentile aggregation for one named
// sub-span within a step. Only populated when include_subs=true is
// requested AND the principal scope permits exposing plugin-supplied
// content (jobs:result:ro family).
type StopwatchSubStats struct {
	Name        string `json:"name"`
	SampleCount int    `json:"sample_count"`
	P50Ms       int    `json:"p50_ms"`
	P95Ms       int    `json:"p95_ms"`
	P99Ms       int    `json:"p99_ms"`
}

const stopwatchTrendBuckets = 6

// handleStopwatch handles GET /stopwatch/{plugin}.
func (s *Server) handleStopwatch(w http.ResponseWriter, r *http.Request) {
	plugin := chi.URLParam(r, "plugin")
	if plugin == "" {
		s.writeError(w, http.StatusBadRequest, "plugin path parameter required")
		return
	}
	if s.stopwatch == nil {
		s.writeError(w, http.StatusServiceUnavailable, "stopwatch reader not configured")
		return
	}

	windowStr := r.URL.Query().Get("window")
	if windowStr == "" {
		windowStr = "1h"
	}
	windowDur, ok := parseStopwatchWindow(windowStr)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid window (use 5m, 1h, or 24h)")
		return
	}

	// HAZARD: subs_json carries plugin-supplied, unvalidated content
	// (see internal/state/stopwatch.go RecordStopwatch comment). Gate
	// sub-span exposure behind the result-class scope the same way
	// canSeeJobResultsFromCtx does for job result payloads. Downgrade
	// rather than 403 (principal can still read the base latency
	// profile), but signal the downgrade explicitly via
	// SubsUnavailable on the response so the caller can distinguish
	// "no sub-spans captured" from "scope denied".
	subsRequested := r.URL.Query().Get("include_subs") == "true"
	includeSubs := subsRequested
	var subsUnavailable string
	if subsRequested && !s.canSeeJobResultsFromCtx(r.Context()) {
		includeSubs = false
		subsUnavailable = SubsUnavailableInsufficientScope
	}

	now := time.Now().UTC()
	since := now.Add(-windowDur)

	rows, err := s.stopwatch.StopwatchRowsForPlugin(r.Context(), plugin, since)
	if err != nil {
		s.logger.Error("stopwatch query failed", "plugin", plugin, "error", err)
		s.writeError(w, http.StatusInternalServerError, "stopwatch query failed")
		return
	}

	resp := aggregateStopwatch(plugin, windowStr, windowDur, now, since, rows, includeSubs)
	resp.SubsUnavailable = subsUnavailable
	respondJSON(w, http.StatusOK, resp)
}

// parseStopwatchWindow accepts the small fixed vocabulary the console
// uses (5m / 1h / 24h). Free-form Go duration strings are deliberately
// rejected so the rolling window can be reasoned about in the UI.
func parseStopwatchWindow(w string) (time.Duration, bool) {
	switch w {
	case "5m":
		return 5 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	default:
		return 0, false
	}
}

// aggregateStopwatch is the pure aggregation: takes raw rows from the
// stopwatch ledger and produces the per-step percentile summary the
// API returns. Split out from handleStopwatch so the percentile and
// bucketing logic is testable without HTTP plumbing.
func aggregateStopwatch(
	plugin, window string,
	windowDur time.Duration,
	now, since time.Time,
	rows []state.StopwatchAggregationRow,
	includeSubs bool,
) StopwatchResponse {
	type stepBucket struct {
		durs     []int64
		times    []time.Time
		subDurs  map[string][]int64
	}
	steps := map[string]*stepBucket{}

	for _, row := range rows {
		b, ok := steps[row.StepID]
		if !ok {
			b = &stepBucket{subDurs: map[string][]int64{}}
			steps[row.StepID] = b
		}
		b.durs = append(b.durs, row.DurNs)
		b.times = append(b.times, row.RecordedAt)

		if !includeSubs || len(row.SubsJSON) == 0 {
			continue
		}
		var parsed []map[string]any
		if err := json.Unmarshal(row.SubsJSON, &parsed); err != nil {
			continue
		}
		for _, sub := range parsed {
			name, _ := sub["name"].(string)
			if name == "" {
				continue
			}
			durFloat, _ := sub["dur_ns"].(float64)
			b.subDurs[name] = append(b.subDurs[name], int64(durFloat))
		}
	}

	sortedSteps := make([]string, 0, len(steps))
	for name := range steps {
		sortedSteps = append(sortedSteps, name)
	}
	sort.Strings(sortedSteps)

	bucketDur := windowDur / time.Duration(stopwatchTrendBuckets)
	if bucketDur <= 0 {
		bucketDur = 1
	}

	out := StopwatchResponse{
		Plugin:     plugin,
		Window:     window,
		CapturedAt: now,
		Steps:      make([]StopwatchStepStats, 0, len(sortedSteps)),
	}

	for _, name := range sortedSteps {
		b := steps[name]
		stat := StopwatchStepStats{
			Step:        name,
			SampleCount: len(b.durs),
			P50Ms:       nsToMs(percentileNs(b.durs, 0.50)),
			P95Ms:       nsToMs(percentileNs(b.durs, 0.95)),
			P99Ms:       nsToMs(percentileNs(b.durs, 0.99)),
			TrendP95Ms:  make([]int, stopwatchTrendBuckets),
		}

		bucketDurs := make([][]int64, stopwatchTrendBuckets)
		for i, t := range b.times {
			idx := int(t.Sub(since) / bucketDur)
			if idx < 0 {
				idx = 0
			}
			if idx >= stopwatchTrendBuckets {
				idx = stopwatchTrendBuckets - 1
			}
			bucketDurs[idx] = append(bucketDurs[idx], b.durs[i])
		}
		for i := 0; i < stopwatchTrendBuckets; i++ {
			if len(bucketDurs[i]) == 0 {
				continue
			}
			stat.TrendP95Ms[i] = nsToMs(percentileNs(bucketDurs[i], 0.95))
		}

		if includeSubs && len(b.subDurs) > 0 {
			subNames := make([]string, 0, len(b.subDurs))
			for sn := range b.subDurs {
				subNames = append(subNames, sn)
			}
			sort.Strings(subNames)
			for _, sn := range subNames {
				durs := b.subDurs[sn]
				stat.Subs = append(stat.Subs, StopwatchSubStats{
					Name:        sn,
					SampleCount: len(durs),
					P50Ms:       nsToMs(percentileNs(durs, 0.50)),
					P95Ms:       nsToMs(percentileNs(durs, 0.95)),
					P99Ms:       nsToMs(percentileNs(durs, 0.99)),
				})
			}
		}

		out.Steps = append(out.Steps, stat)
	}

	return out
}

// percentileNs returns the p-th percentile of the input durations in
// nanoseconds using the nearest-rank method. p in [0, 1]. Returns 0
// for an empty slice. Input is not mutated.
func percentileNs(durs []int64, p float64) int64 {
	if len(durs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// nsToMs converts a nanosecond duration to whole milliseconds,
// rounding to nearest. The console renders integer ms; sub-ms
// precision is not meaningful for human latency reading.
func nsToMs(ns int64) int {
	return int((ns + 500_000) / 1_000_000)
}
