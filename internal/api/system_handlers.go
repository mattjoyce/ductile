package api

import (
	"context"
	"net/http"
	"time"

	"github.com/mattjoyce/ductile/internal/doctor"
)

// SystemCheckReport is the unified shape returned by /system/doctor and
// /system/selfcheck. Both endpoints surface a flat list of named checks
// plus an overall boolean so the console can render the chrome badge
// uniformly regardless of which subsystem produced the result.
type SystemCheckReport struct {
	OK         bool          `json:"ok"`
	CapturedAt time.Time     `json:"captured_at"`
	Checks     []SystemCheck `json:"checks"`
}

// SystemCheck is one entry in a SystemCheckReport. Severity is "error"
// or "warning" for doctor entries (warnings do not flip overall OK to
// false; only errors do); empty for selfcheck entries because the
// selfcheck model already conflates severity into the per-check ok flag.
type SystemCheck struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Severity string `json:"severity,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Field    string `json:"field,omitempty"`
}

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
	result, err := s.config.DoctorFunc(r.Context())
	if err != nil {
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
	report, err := s.config.SelfcheckFunc(r.Context())
	if err != nil {
		s.logger.Error("selfcheck run failed", "error", err)
		s.writeError(w, http.StatusInternalServerError, "selfcheck run failed")
		return
	}
	if report.CapturedAt.IsZero() {
		report.CapturedAt = time.Now().UTC()
	}
	respondJSON(w, http.StatusOK, report)
}

// doctorResultToReport adapts a doctor.Result into the unified
// SystemCheckReport shape. Errors become checks with severity=error
// and drive the overall OK flag; warnings become checks with
// severity=warning and do NOT flip overall OK.
func doctorResultToReport(r *doctor.Result) SystemCheckReport {
	out := SystemCheckReport{
		OK:         r.Valid,
		CapturedAt: time.Now().UTC(),
		Checks:     make([]SystemCheck, 0, len(r.Errors)+len(r.Warnings)),
	}
	for _, e := range r.Errors {
		out.Checks = append(out.Checks, SystemCheck{
			Name:     e.Category,
			OK:       false,
			Severity: "error",
			Detail:   e.Message,
			Field:    e.Field,
		})
	}
	for _, w := range r.Warnings {
		out.Checks = append(out.Checks, SystemCheck{
			Name:     w.Category,
			OK:       false,
			Severity: "warning",
			Detail:   w.Message,
			Field:    w.Field,
		})
	}
	return out
}
