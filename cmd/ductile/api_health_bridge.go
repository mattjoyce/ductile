package main

import (
	"os"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/api"
)

// selfcheckReportForAPI runs the same selfcheck battery the CLI does
// and returns it in the unified api.SystemCheckReport shape the HTTP
// handler emits. The selfcheck struct lives in this package and the
// api package cannot import it, so this bridge function lives next to
// the source of truth.
//
// Two semantic adjustments happen here that the CLI does not need:
//
//  1. Detail strings that begin with "skipped:" map to Status=skipped
//     rather than Status=fail. The CLI conflates skip and fail into one
//     boolean (and flips overall Healthy) because its exit code only
//     has room for ok / not-ok. The API splits them so a console badge
//     can stay green when, e.g., db_integrity is skipped because the
//     gateway is the active WAL holder.
//
//  2. The pid_lock check that fails because an active gateway holds
//     the lock is reinterpreted as Status=ok when the active PID is
//     this process — we are the gateway, holding our own lock is the
//     healthy state, not a failure. The CLI runs out-of-process so it
//     legitimately treats any active holder as "can't safely proceed";
//     the API runs in-process so the same observation means "we're up".
func selfcheckReportForAPI(configPath string) api.SystemCheckReport {
	report := collectSystemSelfcheck(configPath)
	out := api.SystemCheckReport{
		CapturedAt: time.Now().UTC(),
		Checks:     make([]api.SystemCheck, 0, len(report.Checks)),
	}
	selfPID := os.Getpid()
	for _, c := range report.Checks {
		check := api.SystemCheck{
			Name:   c.Name,
			Detail: c.Detail,
		}
		// Fold the systemStatusCheck.Path into Detail for the unified
		// shape — the console doesn't need a separate filesystem path
		// surface today, and keeping it inside Detail preserves the
		// information without growing the SystemCheck struct.
		if c.Path != "" {
			if check.Detail != "" {
				check.Detail = c.Path + ": " + check.Detail
			} else {
				check.Detail = c.Path
			}
		}
		check.Status = classifySelfcheck(c.Name, c.OK, c.ActivePID, c.Detail, selfPID)
		out.Checks = append(out.Checks, check)
	}
	out.OK = bridgeReportOK(out.Checks)
	return out
}

// classifySelfcheck maps one CLI-shaped systemStatusCheck into one of
// the four API Status values. Split out so the per-check decision
// tree is testable in isolation.
func classifySelfcheck(name string, ok bool, activePID int, detail string, selfPID int) string {
	if ok {
		return api.StatusOK
	}
	// pid_lock case: when the lock is held by us, that's the healthy
	// state for an in-process API. The CLI treats it as fail because
	// the CLI is asking "is anyone else holding the DB?"; we are the
	// gateway, so "we are holding it" is the answer the API wants.
	if name == "pid_lock" && activePID == selfPID && selfPID > 0 {
		return api.StatusOK
	}
	if strings.HasPrefix(detail, "skipped:") {
		return api.StatusSkipped
	}
	return api.StatusFail
}

// bridgeReportOK mirrors api.reportOK so the bridge can compute the
// rollup without importing the unexported helper. Cheap to duplicate;
// the semantics are simple (any fail flips it) and the bridge is the
// only place outside the api package that needs to know.
func bridgeReportOK(checks []api.SystemCheck) bool {
	for _, c := range checks {
		if c.Status == api.StatusFail {
			return false
		}
	}
	return true
}
