package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
)

// setupTestServerForJobScopes wires a Server with caller-supplied tokens
// plus a real queue + state stack, then enqueues + completes one job whose
// result payload is the canonical D1 evidence marker. Returns the job ID
// and the server so individual tests can issue scoped HTTP requests.
func setupTestServerForJobScopes(t *testing.T, tokens []auth.TokenConfig) (string, *Server) {
	t.Helper()

	db := setupTestDB(t)
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	reg := &mockRegistry{}

	srv := New(
		Config{
			Listen: "localhost:0",
			Tokens: tokens,
		},
		q,
		reg,
		&mockRouter{},
		&mockWaiter{},
		cs,
		state.NewAdmitter(q, state.DefaultMaxContextBytes),
		nil,
		hub,
		slog.Default(),
	)

	ctx := context.Background()
	jobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "phase2d1",
		Command:     "handle",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job, err := q.Dequeue(ctx)
	if err != nil || job == nil {
		t.Fatalf("Dequeue: job=%v err=%v", job, err)
	}
	if job.ID != jobID {
		t.Fatalf("dequeued job id = %s, want %s", job.ID, jobID)
	}
	result := json.RawMessage(`{"marker":"DUCTILE_D1_RESULT_MARKER","status":"ok"}`)
	if err := q.CompleteWithResult(ctx, jobID, queue.StatusSucceeded, result, nil, nil); err != nil {
		t.Fatalf("CompleteWithResult: %v", err)
	}

	return jobID, srv
}

func getJob(t *testing.T, srv *Server, token, jobID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/job/"+jobID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)
	return rr
}

// TestPhase2D1_JobsRoStillSeesResult is the back-compat invariant: a token
// granted the legacy jobs:ro super-scope continues to see Result payloads
// in /job/{id} responses. ANY existing operator with jobs:ro keeps their
// current visibility. This is the most important property to lock down.
func TestPhase2D1_JobsRoStillSeesResult(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "readonly", Scopes: []string{"jobs:ro"}},
	})

	rr := getJob(t, srv, "readonly", jobID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp JobStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if len(resp.Result) == 0 {
		t.Fatalf("jobs:ro super-scope must see Result; got empty in body=%s", rr.Body.String())
	}
}

// TestPhase2D1_JobsStatusRoOmitsResult: a principal with ONLY jobs:status:ro
// reaches /job/{id} but does NOT see the result payload. This is the
// narrower-scope shape the recommendation calls for.
func TestPhase2D1_JobsStatusRoOmitsResult(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "status-only", Scopes: []string{"jobs:status:ro"}},
	})

	rr := getJob(t, srv, "status-only", jobID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (jobs:status:ro reaches endpoint), got %d: %s", rr.Code, rr.Body.String())
	}
	// Decode into the typed response and assert the Result field is empty
	// (precise — not a substring search that could trip on field names).
	var resp JobStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if len(resp.Result) != 0 {
		t.Fatalf("jobs:status:ro must NOT see Result; got %d bytes: %s", len(resp.Result), string(resp.Result))
	}
	if resp.Status != "succeeded" {
		t.Fatalf("jobs:status:ro should still see status; got status=%q body=%s", resp.Status, rr.Body.String())
	}
}

// TestPhase2D1_JobsStatusPlusResultRoSeesResult: explicitly combining the
// narrower scopes restores result visibility — the granular path that
// operators can use to grant exactly status+result without logs/tree.
func TestPhase2D1_JobsStatusPlusResultRoSeesResult(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "status-and-result", Scopes: []string{"jobs:status:ro", "jobs:result:ro"}},
	})

	rr := getJob(t, srv, "status-and-result", jobID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "DUCTILE_D1_RESULT_MARKER") {
		t.Fatalf("jobs:status:ro + jobs:result:ro must see result; body=%s", rr.Body.String())
	}
}

// TestPhase2D1_JobsRwSeesResult confirms the write-scope path; the
// super-super-scope still grants everything.
func TestPhase2D1_JobsRwSeesResult(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "rw", Scopes: []string{"jobs:rw"}},
	})

	rr := getJob(t, srv, "rw", jobID)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "DUCTILE_D1_RESULT_MARKER") {
		t.Fatalf("jobs:rw must see result; body=%s", rr.Body.String())
	}
}

// TestPhase2D1_TokenWithoutAnyJobsScopeIsBlocked is the negative-control —
// no jobs-related scope at all returns 403 from the middleware.
func TestPhase2D1_TokenWithoutAnyJobsScopeIsBlocked(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "no-jobs", Scopes: []string{"plugin:catalog:ro"}},
	})

	rr := getJob(t, srv, "no-jobs", jobID)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for no-jobs-scope, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPhase2D1_JobsLogsRoOmitsResultAndStderrAndLastError: a principal with
// jobs:logs:ro reaches /job-logs and sees log lifecycle metadata but
// NONE of the result-class fields (Result, Stderr, LastError) — D1 shapes
// all three under jobs:result:ro, not just Result. Without this, stderr
// or error-message leakage could carry plugin diagnostics or payload echo
// to operators who were granted "logs only".
func TestPhase2D1_JobsLogsRoOmitsResultAndStderrAndLastError(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "logs-only", Scopes: []string{"jobs:logs:ro"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/job-logs?job_id="+jobID+"&include_result=true", nil)
	req.Header.Set("Authorization", "Bearer logs-only")
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp JobLogListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}
	if len(resp.Logs) == 0 {
		t.Fatalf("expected at least one log item; body=%s", rr.Body.String())
	}
	for i, item := range resp.Logs {
		if len(item.Result) != 0 {
			t.Errorf("log[%d] Result should be empty for jobs:logs:ro alone; got %d bytes", i, len(item.Result))
		}
		if item.Stderr != nil {
			t.Errorf("log[%d] Stderr should be nil for jobs:logs:ro alone; got %q", i, *item.Stderr)
		}
		if item.LastError != nil {
			t.Errorf("log[%d] LastError should be nil for jobs:logs:ro alone; got %q", i, *item.LastError)
		}
		// Lifecycle metadata must still be present.
		if item.JobID == "" || item.Status == "" {
			t.Errorf("log[%d] should still carry status/lifecycle fields", i)
		}
	}
}

// TestPhase2D1_JobsLogsPlusResultRoSeesEverything is the positive control:
// granting BOTH jobs:logs:ro AND jobs:result:ro restores full visibility
// on /job-logs items.
func TestPhase2D1_JobsLogsPlusResultRoSeesEverything(t *testing.T) {
	jobID, srv := setupTestServerForJobScopes(t, []auth.TokenConfig{
		{Token: "logs-and-result", Scopes: []string{"jobs:logs:ro", "jobs:result:ro"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/job-logs?job_id="+jobID+"&include_result=true", nil)
	req.Header.Set("Authorization", "Bearer logs-and-result")
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "DUCTILE_D1_RESULT_MARKER") {
		t.Fatalf("jobs:logs:ro + jobs:result:ro must see result marker; body=%s", rr.Body.String())
	}
}
