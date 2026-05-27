package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/doctor"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
)

// setupServerWithSystemFuncs wires a Server with caller-supplied
// DoctorFunc and SelfcheckFunc so each handler can be exercised
// independently of the real config-on-disk pipeline.
func setupServerWithSystemFuncs(t *testing.T, doctorFn DoctorFunc, selfcheckFn SelfcheckFunc) *Server {
	t.Helper()
	db := setupTestDB(t)
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	cfg := Config{
		Listen:        "localhost:8080",
		Tokens:        []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
		DoctorFunc:    doctorFn,
		SelfcheckFunc: selfcheckFn,
	}
	return New(cfg, Deps{
		Queue:        q,
		Registry:     &mockRegistry{},
		Router:       &mockRouter{},
		Waiter:       &mockWaiter{},
		ContextStore: cs,
		Admitter:     state.NewAdmitter(q, state.DefaultMaxContextBytes),
		Hub:          hub,
		Logger:       slog.Default(),
	})
}

func TestDoctorResultToReport(t *testing.T) {
	t.Parallel()
	result := &doctor.Result{
		Valid: false,
		Errors: []doctor.Issue{
			{Category: "tokens", Field: "tokens.api", Message: "missing scope"},
		},
		Warnings: []doctor.Issue{
			{Category: "plugins", Field: "", Message: "unused plugin: x"},
		},
	}
	report := doctorResultToReport(result)
	if report.OK {
		t.Errorf("OK = true, want false (because there is an error)")
	}
	if len(report.Checks) != 2 {
		t.Fatalf("checks len = %d, want 2", len(report.Checks))
	}
	if report.Checks[0].Status != StatusFail {
		t.Errorf("first check Status = %q, want %q", report.Checks[0].Status, StatusFail)
	}
	if report.Checks[0].Name != "tokens" || report.Checks[0].Field != "tokens.api" {
		t.Errorf("first check Name/Field = %q/%q, want tokens/tokens.api", report.Checks[0].Name, report.Checks[0].Field)
	}
	if report.Checks[1].Status != StatusWarn {
		t.Errorf("second check Status = %q, want %q", report.Checks[1].Status, StatusWarn)
	}

	// Warnings alone must not flip OK — only fail status does.
	okResult := &doctor.Result{
		Valid: true,
		Warnings: []doctor.Issue{
			{Category: "schedule", Message: "schedule looks suspicious"},
		},
	}
	okReport := doctorResultToReport(okResult)
	if !okReport.OK {
		t.Errorf("warnings-only result: OK = false, want true (warn does not flip)")
	}
	if len(okReport.Checks) != 1 {
		t.Fatalf("warnings-only checks len = %d, want 1", len(okReport.Checks))
	}
	if okReport.Checks[0].Status != StatusWarn {
		t.Errorf("warnings-only check Status = %q, want %q", okReport.Checks[0].Status, StatusWarn)
	}
}

func TestReportOK(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		checks []SystemCheck
		want   bool
	}{
		{"empty", nil, true},
		{"all ok", []SystemCheck{{Status: StatusOK}, {Status: StatusOK}}, true},
		{"any fail flips", []SystemCheck{{Status: StatusOK}, {Status: StatusFail}}, false},
		{"warn does not flip", []SystemCheck{{Status: StatusOK}, {Status: StatusWarn}}, true},
		{"skipped does not flip", []SystemCheck{{Status: StatusOK}, {Status: StatusSkipped}}, true},
		{"mix warn+skipped+ok", []SystemCheck{{Status: StatusOK}, {Status: StatusSkipped}, {Status: StatusWarn}}, true},
		{"single fail buried in warn/skipped", []SystemCheck{{Status: StatusSkipped}, {Status: StatusFail}, {Status: StatusWarn}}, false},
	}
	for _, c := range cases {
		if got := reportOK(c.checks); got != c.want {
			t.Errorf("%s: reportOK = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHandleDoctor_HappyPath(t *testing.T) {
	t.Parallel()
	doctorFn := func(_ context.Context) (*doctor.Result, error) {
		return &doctor.Result{Valid: true}, nil
	}
	server := setupServerWithSystemFuncs(t, doctorFn, nil)
	req := httptest.NewRequest(http.MethodGet, "/system/doctor", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got SystemCheckReport
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK {
		t.Errorf("OK = false, want true")
	}
	if got.CapturedAt.IsZero() {
		t.Errorf("CapturedAt is zero")
	}
}

func TestHandleDoctor_NilFuncReturns503(t *testing.T) {
	t.Parallel()
	server := setupServerWithSystemFuncs(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/system/doctor", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandleDoctor_FuncErrorReturns500(t *testing.T) {
	t.Parallel()
	doctorFn := func(_ context.Context) (*doctor.Result, error) {
		return nil, errors.New("boom")
	}
	server := setupServerWithSystemFuncs(t, doctorFn, nil)
	req := httptest.NewRequest(http.MethodGet, "/system/doctor", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestHandleDoctor_DeadlineExceededReturns504(t *testing.T) {
	t.Parallel()
	// Closure waits for the deadline to fire, then returns the
	// context error — mirrors what a slow validateConfigAtPath would
	// do once the handler's WithTimeout context is cancelled.
	doctorFn := func(ctx context.Context) (*doctor.Result, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	server := setupServerWithSystemFuncs(t, doctorFn, nil)
	req := httptest.NewRequest(http.MethodGet, "/system/doctor", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()

	// Override the package deadline for the test by running against a
	// short closure deadline — easier than monkey-patching the const.
	// The deadline is per-handler (doctorDeadline = 5s); this test
	// verifies the path when the closure honors ctx.Done(). The
	// handler's WithTimeout will cancel the ctx after doctorDeadline,
	// the closure sees it, returns context.DeadlineExceeded, and the
	// handler maps to 504.
	done := make(chan struct{})
	go func() {
		server.setupRoutes().ServeHTTP(rr, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(doctorDeadline + 2*time.Second):
		t.Fatalf("handler did not return within %v of deadline", doctorDeadline)
	}
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleDoctor_RequiresAuth(t *testing.T) {
	t.Parallel()
	server := setupServerWithSystemFuncs(t, func(context.Context) (*doctor.Result, error) {
		return &doctor.Result{Valid: true}, nil
	}, nil)
	req := httptest.NewRequest(http.MethodGet, "/system/doctor", nil)
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestHandleSelfcheck_HappyPath(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	selfFn := func(_ context.Context) (SystemCheckReport, error) {
		return SystemCheckReport{
			CapturedAt: at,
			Checks: []SystemCheck{
				{Name: "config_discovery", Status: StatusOK, Detail: "/etc/ductile"},
				{Name: "db_integrity", Status: StatusSkipped, Detail: "skipped: active gateway holds PID lock"},
			},
		}, nil
	}
	server := setupServerWithSystemFuncs(t, nil, selfFn)
	req := httptest.NewRequest(http.MethodGet, "/system/selfcheck", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got SystemCheckReport
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.OK {
		// One skipped + one ok = no fail = OK. Audit fix payoff:
		// the running-gateway badge no longer flips red because of
		// WAL-safety skips.
		t.Errorf("OK = false with only ok+skipped checks, want true")
	}
	if len(got.Checks) != 2 {
		t.Errorf("checks len = %d, want 2", len(got.Checks))
	}
	if got.Checks[1].Status != StatusSkipped {
		t.Errorf("db_integrity Status = %q, want %q", got.Checks[1].Status, StatusSkipped)
	}
	if !got.CapturedAt.Equal(at) {
		t.Errorf("CapturedAt = %v, want %v", got.CapturedAt, at)
	}
}

func TestHandleSelfcheck_FailFlipsOK(t *testing.T) {
	t.Parallel()
	selfFn := func(context.Context) (SystemCheckReport, error) {
		return SystemCheckReport{
			Checks: []SystemCheck{
				{Name: "config_discovery", Status: StatusOK},
				{Name: "db_schema", Status: StatusFail, Detail: "missing index"},
			},
		}, nil
	}
	server := setupServerWithSystemFuncs(t, nil, selfFn)
	req := httptest.NewRequest(http.MethodGet, "/system/selfcheck", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got SystemCheckReport
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OK {
		t.Errorf("OK = true with a fail check present, want false")
	}
}

func TestHandleSelfcheck_NilFuncReturns503(t *testing.T) {
	t.Parallel()
	server := setupServerWithSystemFuncs(t, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/system/selfcheck", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHandleSelfcheck_FuncErrorReturns500(t *testing.T) {
	t.Parallel()
	selfFn := func(context.Context) (SystemCheckReport, error) {
		return SystemCheckReport{}, errors.New("boom")
	}
	server := setupServerWithSystemFuncs(t, nil, selfFn)
	req := httptest.NewRequest(http.MethodGet, "/system/selfcheck", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestHandleSelfcheck_DeadlineExceededReturns504(t *testing.T) {
	t.Parallel()
	selfFn := func(ctx context.Context) (SystemCheckReport, error) {
		<-ctx.Done()
		return SystemCheckReport{}, ctx.Err()
	}
	server := setupServerWithSystemFuncs(t, nil, selfFn)
	req := httptest.NewRequest(http.MethodGet, "/system/selfcheck", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		server.setupRoutes().ServeHTTP(rr, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(selfcheckDeadline + 2*time.Second):
		t.Fatalf("handler did not return within %v of deadline", selfcheckDeadline)
	}
	if rr.Code != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleSelfcheck_CapturedAtDefaultsToNow(t *testing.T) {
	t.Parallel()
	selfFn := func(context.Context) (SystemCheckReport, error) {
		return SystemCheckReport{Checks: []SystemCheck{{Name: "x", Status: StatusOK}}}, nil
	}
	server := setupServerWithSystemFuncs(t, nil, selfFn)
	before := time.Now().UTC()
	req := httptest.NewRequest(http.MethodGet, "/system/selfcheck", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	after := time.Now().UTC()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got SystemCheckReport
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CapturedAt.Before(before) || got.CapturedAt.After(after.Add(time.Second)) {
		t.Errorf("CapturedAt = %v not in [%v, %v]", got.CapturedAt, before, after)
	}
}
