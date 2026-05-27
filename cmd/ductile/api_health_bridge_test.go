package main

import (
	"testing"

	"github.com/mattjoyce/ductile/internal/api"
)

// TestClassifySelfcheck pins the four-way mapping from the CLI's
// boolean systemStatusCheck to the API's Status string. The
// gateway-is-its-own-lock case is the load-bearing one: without it,
// every API /system/selfcheck call against a live gateway would
// flip the badge red because of the WAL-safety pid_lock fail.
func TestClassifySelfcheck(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		checkName string
		ok        bool
		activePID int
		detail    string
		selfPID   int
		want      string
	}{
		{
			name: "ok check passes through",
			checkName: "config_discovery", ok: true,
			want: api.StatusOK,
		},
		{
			name: "pid_lock held by us is healthy",
			checkName: "pid_lock", ok: false, activePID: 4242,
			detail: "another instance appears active (pid 4242)", selfPID: 4242,
			want: api.StatusOK,
		},
		{
			name: "pid_lock held by someone else is fail",
			checkName: "pid_lock", ok: false, activePID: 4242,
			detail: "another instance appears active (pid 4242)", selfPID: 9999,
			want: api.StatusFail,
		},
		{
			name: "skipped: prefix detected",
			checkName: "db_integrity", ok: false,
			detail: "skipped: active gateway holds PID lock — quiesce before selfcheck",
			want:   api.StatusSkipped,
		},
		{
			name: "plain failure",
			checkName: "db_schema", ok: false,
			detail: "missing index job_log_completed_at_idx",
			want:   api.StatusFail,
		},
		{
			name: "selfPID=0 (no process) does not match pid_lock self case",
			checkName: "pid_lock", ok: false, activePID: 0, selfPID: 0,
			want: api.StatusFail,
		},
	}
	for _, c := range cases {
		got := classifySelfcheck(c.checkName, c.ok, c.activePID, c.detail, c.selfPID)
		if got != c.want {
			t.Errorf("%s: classifySelfcheck = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBridgeReportOK(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		checks []api.SystemCheck
		want   bool
	}{
		{"empty", nil, true},
		{"all skipped is ok", []api.SystemCheck{{Status: api.StatusSkipped}, {Status: api.StatusSkipped}}, true},
		{"one fail flips", []api.SystemCheck{{Status: api.StatusOK}, {Status: api.StatusFail}}, false},
		{"warn alone does not flip", []api.SystemCheck{{Status: api.StatusWarn}}, true},
	}
	for _, c := range cases {
		if got := bridgeReportOK(c.checks); got != c.want {
			t.Errorf("%s: bridgeReportOK = %v, want %v", c.name, got, c.want)
		}
	}
}
