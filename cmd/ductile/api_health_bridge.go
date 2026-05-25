package main

import (
	"time"

	"github.com/mattjoyce/ductile/internal/api"
)

// selfcheckReportForAPI runs the same selfcheck battery the CLI does
// and returns it in the unified api.SystemCheckReport shape the HTTP
// handler emits. The selfcheck struct lives in this package and the
// api package cannot import it, so this bridge function lives next to
// the source of truth.
func selfcheckReportForAPI(configPath string) api.SystemCheckReport {
	report := collectSystemSelfcheck(configPath)
	out := api.SystemCheckReport{
		OK:         report.Healthy,
		CapturedAt: time.Now().UTC(),
		Checks:     make([]api.SystemCheck, 0, len(report.Checks)),
	}
	for _, c := range report.Checks {
		check := api.SystemCheck{
			Name:   c.Name,
			OK:     c.OK,
			Detail: c.Detail,
		}
		if c.Path != "" {
			if check.Detail != "" {
				check.Detail = c.Path + ": " + check.Detail
			} else {
				check.Detail = c.Path
			}
		}
		out.Checks = append(out.Checks, check)
	}
	return out
}
