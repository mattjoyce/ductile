package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mattjoyce/ductile/internal/doctor"
)

// SystemCheckReport is the unified shape returned by /system/doctor and
// /system/selfcheck. Both endpoints surface a flat list of named checks
// plus an overall boolean so the console can render the chrome badge
// uniformly regardless of which subsystem produced the result.
//
// OK is true when no check has Status == StatusFail. Warnings and
// skipped checks do not flip OK — only outright failures do. This
// matches the human expectation: "is anything actually broken right
// now?" and lets the WAL-safety skips (the active gateway holds the
// PID lock and cannot safely run integrity checks) leave the badge
// green.
type SystemCheckReport struct {
	OK         bool          `json:"ok"`
	CapturedAt time.Time     `json:"captured_at"`
	Checks     []SystemCheck `json:"checks"`
}

// SystemCheck is one entry in a SystemCheckReport. Status takes one
// of the four named values below. Detail is human-readable and may be
// empty. Field is populated for doctor entries that carry an
// Issue.Field (e.g. "tokens.api"); empty for selfcheck.
type SystemCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Field  string `json:"field,omitempty"`
}

// Status values for SystemCheck. Three-state result plus a
// warn level for doctor-style "noted, not blocking" findings.
const (
	StatusOK      = "ok"      // check ran and passed
	StatusFail    = "fail"    // check ran and failed; flips report.OK
	StatusWarn    = "warn"    // check ran, noted a concern; does NOT flip report.OK
	StatusSkipped = "skipped" // check did not run (e.g. WAL-safety); does NOT flip report.OK
)

// Per-handler deadlines for the system check endpoints. Both surfaces
// touch disk (config tree read for doctor; SQLite open + PRAGMA for
// selfcheck) and the global write timeout (10m) is far too generous a
// fallback for an operator-facing badge call. Named here so the
// values are reviewable in one place.
const (
	doctorDeadline    = 5 * time.Second
	selfcheckDeadline = 10 * time.Second
)

// DoctorFunc runs doctor.Validate (with all required hook-pipeline
// projection) and returns the result. The runtime closure adapts the
// CLI's existing config-check pipeline so the handler stays free of
// cmd/ductile coupling.
type DoctorFunc func(ctx context.Context) (*doctor.Result, error)

// SelfcheckFunc runs the system-selfcheck invariant battery against
// the active gateway's config and DB. Returns a pre-shaped
// SystemCheckReport because selfcheck's native struct lives in
// cmd/ductile and the api package cannot import that.
type SelfcheckFunc func(ctx context.Context) (SystemCheckReport, error)

// handleDoctor handles GET /system/doctor.
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if s.config.DoctorFunc == nil {
		s.writeError(w, http.StatusServiceUnavailable, "doctor not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), doctorDeadline)
	defer cancel()

	result, err := s.config.DoctorFunc(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			s.writeError(w, http.StatusGatewayTimeout,
				"doctor exceeded "+doctorDeadline.String()+" deadline")
			return
		}
		s.logger.Error("doctor run failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "doctor run failed")
		return
	}
	respondJSON(w, http.StatusOK, doctorResultToReport(result))
}

// handleSelfcheck handles GET /system/selfcheck.
func (s *Server) handleSelfcheck(w http.ResponseWriter, r *http.Request) {
	if s.config.SelfcheckFunc == nil {
		s.writeError(w, http.StatusServiceUnavailable, "selfcheck not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfcheckDeadline)
	defer cancel()

	report, err := s.config.SelfcheckFunc(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			s.writeError(w, http.StatusGatewayTimeout,
				"selfcheck exceeded "+selfcheckDeadline.String()+" deadline")
			return
		}
		s.logger.Error("selfcheck run failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "selfcheck run failed")
		return
	}
	if report.CapturedAt.IsZero() {
		report.CapturedAt = time.Now().UTC()
	}
	// Re-derive OK from the per-check statuses, in case the bridge
	// returned a value that disagrees with the per-check rollup
	// (defensive — the bridge is supposed to compute this, but
	// belt-and-suspenders is cheap here).
	report.OK = reportOK(report.Checks)
	respondJSON(w, http.StatusOK, report)
}

// doctorResultToReport adapts a doctor.Result into the unified
// SystemCheckReport shape. Errors become Status=fail checks (and
// flip report.OK); warnings become Status=warn (and do not).
func doctorResultToReport(r *doctor.Result) SystemCheckReport {
	out := SystemCheckReport{
		CapturedAt: time.Now().UTC(),
		Checks:     make([]SystemCheck, 0, len(r.Errors)+len(r.Warnings)),
	}
	for _, e := range r.Errors {
		out.Checks = append(out.Checks, SystemCheck{
			Name:   e.Category,
			Status: StatusFail,
			Detail: e.Message,
			Field:  e.Field,
		})
	}
	for _, wn := range r.Warnings {
		out.Checks = append(out.Checks, SystemCheck{
			Name:   wn.Category,
			Status: StatusWarn,
			Detail: wn.Message,
			Field:  wn.Field,
		})
	}
	out.OK = reportOK(out.Checks)
	return out
}

// reportOK is the single source of truth for the OK roll-up:
// true unless any check is Status=fail. Warn and Skipped don't count.
func reportOK(checks []SystemCheck) bool {
	for _, c := range checks {
		if c.Status == StatusFail {
			return false
		}
	}
	return true
}
