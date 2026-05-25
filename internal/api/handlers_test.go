package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// mockRegistry implements PluginRegistry for testing
type mockRegistry struct {
	plugins map[string]*plugin.Plugin
}

func (m *mockRegistry) Get(name string) (*plugin.Plugin, bool) {
	p, ok := m.plugins[name]
	return p, ok
}

func (m *mockRegistry) All() map[string]*plugin.Plugin {
	if m.plugins == nil {
		return map[string]*plugin.Plugin{}
	}
	return m.plugins
}

// mockRouter implements PipelineRouter for testing
type mockRouter struct {
	getPipelineByTriggerFunc func(trigger string) *router.PipelineInfo
	getPipelineByNameFunc    func(name string) *router.PipelineInfo
	getEntryDispatchesFunc   func(pipelineName string, event protocol.Event) ([]router.Dispatch, error)
	getNodeFunc              func(pipelineName string, stepID string) (dsl.Node, bool)
	getCompiledRoutesFunc    func(pipelineName string) []dsl.CompiledRoute
	pipelineSummary          []router.PipelineInfo
}

func (m *mockRouter) GetPipelineByTrigger(trigger string) *router.PipelineInfo {
	if m.getPipelineByTriggerFunc != nil {
		return m.getPipelineByTriggerFunc(trigger)
	}
	return nil
}

func (m *mockRouter) GetPipelineByName(name string) *router.PipelineInfo {
	if m.getPipelineByNameFunc != nil {
		return m.getPipelineByNameFunc(name)
	}
	return nil
}

func (m *mockRouter) GetEntryDispatches(pipelineName string, event protocol.Event) ([]router.Dispatch, error) {
	if m.getEntryDispatchesFunc != nil {
		return m.getEntryDispatchesFunc(pipelineName, event)
	}
	return []router.Dispatch{
		{
			Plugin:  "echo",
			Command: "handle",
			Event:   event,
			StepID:  "step-1",
		},
	}, nil
}

func (m *mockRouter) GetNode(pipelineName string, stepID string) (dsl.Node, bool) {
	if m.getNodeFunc != nil {
		return m.getNodeFunc(pipelineName, stepID)
	}
	return dsl.Node{}, false
}

func (m *mockRouter) GetCompiledRoutes(pipelineName string) []dsl.CompiledRoute {
	if m.getCompiledRoutesFunc != nil {
		return m.getCompiledRoutesFunc(pipelineName)
	}
	return nil
}

func (m *mockRouter) PipelineSummary() []router.PipelineInfo { return m.pipelineSummary }

// mockWaiter implements TreeWaiter for testing
type mockWaiter struct {
	waitForJobTreeFunc func(ctx context.Context, rootJobID string, timeout time.Duration) ([]*queue.JobResult, error)
}

func (m *mockWaiter) WaitForJobTree(ctx context.Context, rootJobID string, timeout time.Duration) ([]*queue.JobResult, error) {
	if m.waitForJobTreeFunc != nil {
		return m.waitForJobTreeFunc(ctx, rootJobID, timeout)
	}
	return nil, nil
}

func setupTestServer(t *testing.T, db *sql.DB, reg PluginRegistry) *Server {
	t.Helper()
	logger := slog.Default()
	cfg := Config{
		Listen: "localhost:8080",
		Tokens: []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
	}
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	return New(cfg, q, reg, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), hub, logger)
}

func newTestServer(reg PluginRegistry) *Server {
	db, err := storage.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		panic(err)
	}
	return setupTestServerWithDB(db, reg)
}

func setupTestServerWithDB(db *sql.DB, reg PluginRegistry) *Server {
	logger := slog.Default()
	cfg := Config{
		Listen: "localhost:8080",
		Tokens: []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
	}
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	return New(cfg, q, reg, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), hub, logger)
}

// setupTestServerWithRouter wires a Server with a caller-supplied router so
// individual tests can stub GetPipelineByName / GetEntryDispatches / GetNode
// behavior. Used by phase2_baggage_overlimit_test.go to feed a pipeline whose
// entry baggage claims pull the request payload into the durable context.
func setupTestServerWithRouter(_ *testing.T, db *sql.DB, reg PluginRegistry, r PipelineRouter) *Server {
	logger := slog.Default()
	cfg := Config{
		Listen: "localhost:8080",
		Tokens: []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
	}
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	return New(cfg, q, reg, r, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), hub, logger)
}

func TestHandleRoot_NoAuth(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp RootResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Name != "Ductile Gateway" {
		t.Errorf("expected name %q, got %q", "Ductile Gateway", resp.Name)
	}
	if resp.Description == "" {
		t.Error("expected non-empty description")
	}
	if resp.UptimeSeconds < 0 {
		t.Error("expected non-negative uptime_seconds")
	}
	for _, key := range []string{"health", "skills", "plugins", "openapi", "ai_plugin"} {
		if resp.Discovery[key] == "" {
			t.Errorf("expected discovery[%q] to be set", key)
		}
	}
}

func TestHandleHealthz_NoAuth(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	q := queue.New(db)
	// Enqueue some jobs to check depth
	for i := 0; i < 7; i++ {
		_, err := q.Enqueue(context.Background(), queue.EnqueueRequest{
			Plugin: "echo", Command: "poll", SubmittedBy: "test",
		})
		if err != nil {
			t.Fatalf("failed to enqueue: %v", err)
		}
	}

	reg := &mockRegistry{
		plugins: map[string]*plugin.Plugin{
			"echo": {Name: "echo"},
		},
	}

	server := setupTestServer(t, db, reg)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	router := server.setupRoutes()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp HealthzResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("expected status ok, got %q", resp.Status)
	}
	if resp.QueueDepth != 7 {
		t.Fatalf("expected queue_depth 7, got %d", resp.QueueDepth)
	}
	if resp.PluginsLoaded != 1 {
		t.Fatalf("expected plugins_loaded 1, got %d", resp.PluginsLoaded)
	}
	if resp.UptimeSeconds < 0 {
		t.Fatalf("expected non-negative uptime_seconds")
	}
}

func TestHandleSchedulerJobs_Authorized(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reg := &mockRegistry{plugins: map[string]*plugin.Plugin{}}
	runtimeCfg := config.Defaults()
	runtimeCfg.Plugins = map[string]config.PluginConf{
		"echo": {
			Enabled: true,
			Schedules: []config.ScheduleConfig{
				{Every: "5m"},
				{Cron: "*/15 * * * *"},
				{After: 2 * time.Hour},
			},
		},
	}

	q := queue.New(db)
	cs := state.NewContextStore(db)
	server := New(Config{
		Listen:        "localhost:8080",
		Tokens:        []auth.TokenConfig{{Token: "test-key-123", Scopes: []string{"*"}}},
		RuntimeConfig: runtimeCfg,
	}, q, reg, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), events.NewHub(10), slog.Default())

	req := httptest.NewRequest(http.MethodGet, "/scheduler/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp SchedulerJobsResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(resp.Jobs) != 3 {
		t.Fatalf("expected 3 scheduler jobs, got %d", len(resp.Jobs))
	}
	for _, job := range resp.Jobs {
		if job.Plugin != "echo" {
			t.Fatalf("expected plugin echo, got %q", job.Plugin)
		}
		if job.NextRunAt == nil {
			t.Fatalf("expected next_run_at for %s mode=%s", job.ScheduleID, job.Mode)
		}
	}
}

type streamWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	buf    bytes.Buffer
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: make(http.Header)}
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	w.status = statusCode
	w.mu.Unlock()
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *streamWriter) Flush() {}

func (w *streamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func (w *streamWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func TestHandleEvents_Unauthorized(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rr := httptest.NewRecorder()
	router := server.setupRoutes()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rr.Code)
	}
}

func TestHandleEvents_ReplaysBufferedEvents(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})
	server.events.Publish("test_event", map[string]any{"k": "v"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/events", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-key-123")

	w := newStreamWriter()
	router := server.setupRoutes()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(w, req)
		close(done)
	}()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(w.String(), "event: test_event\n") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(w.String(), "event: test_event\n") {
		t.Fatalf("expected SSE event in stream, got: %q", w.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatalf("stream did not exit after context cancel")
	}
}

func TestHandleGetJob_Success(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	q := queue.New(db)
	jobID, err := q.Enqueue(context.Background(), queue.EnqueueRequest{
		Plugin: "echo", Command: "poll", SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}

	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/job/"+jobID, nil)
	req.Header.Set("Authorization", "Bearer test-key-123")

	rr := httptest.NewRecorder()
	router := server.setupRoutes()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rr.Code)
	}

	var resp JobStatusResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.JobID != jobID {
		t.Errorf("expected job_id %s, got %s", jobID, resp.JobID)
	}
	if resp.Plugin != "echo" {
		t.Errorf("expected plugin echo, got %s", resp.Plugin)
	}
}

func TestHandleGetJob_NotFound(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/job/unknown", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")

	rr := httptest.NewRecorder()
	router := server.setupRoutes()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rr.Code)
	}

	var resp ErrorResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Error != "job not found" {
		t.Errorf("expected error 'job not found', got %s", resp.Error)
	}
}

func TestHandleListJobs_SuccessWithFilters(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	q := queue.New(db)
	// Enqueue a job and complete it to test filters
	jobID, err := q.Enqueue(context.Background(), queue.EnqueueRequest{
		Plugin: "withings", Command: "poll", SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue: %v", err)
	}
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("failed to dequeue: %v", err)
	}
	if err := q.CompleteWithResult(context.Background(), jobID, queue.StatusSucceeded, json.RawMessage(`{}`), nil, nil); err != nil {
		t.Fatalf("failed to complete: %v", err)
	}

	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/jobs?plugin=withings&command=poll&status=ok&limit=5", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var resp JobListResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
	if len(resp.Jobs) != 1 {
		t.Fatalf("jobs len = %d, want 1", len(resp.Jobs))
	}
	if resp.Jobs[0].JobID != jobID {
		t.Fatalf("job_id = %s, want %s", resp.Jobs[0].JobID, jobID)
	}
	if resp.Jobs[0].Status != "succeeded" {
		t.Fatalf("status = %s, want succeeded", resp.Jobs[0].Status)
	}
}

func TestHandleListJobs_InvalidLimit(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/jobs?limit=0", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleListJobs_InvalidStatus(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/jobs?status=bogus", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandlePluginTrigger_DirectExecution(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reg := &mockRegistry{
		plugins: map[string]*plugin.Plugin{
			"echo": {
				Name: "echo",
				Commands: plugin.Commands{
					{Name: "health", Type: plugin.CommandTypeRead},
				},
			},
		},
	}

	server := setupTestServer(t, db, reg)

	req := httptest.NewRequest(http.MethodPost, "/plugin/echo/health", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")

	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rr.Code)
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Verify DB state
	q := queue.New(db)
	job, err := q.GetJobByID(context.Background(), resp.JobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if job.Plugin != "echo" || job.Command != "health" {
		t.Errorf("unexpected job: %+v", job)
	}
}

func TestHandlePipelineTrigger_ExplicitExecution(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reg := &mockRegistry{}
	rt := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			if name == "test-pipe" {
				return &router.PipelineInfo{
					Name:    "test-pipe",
					Trigger: "file.read",
				}
			}
			return nil
		},
	}

	server := setupTestServer(t, db, reg)
	server.router = rt

	body := bytes.NewBufferString(`{"payload": {"input": "val"}}`)
	req := httptest.NewRequest(http.MethodPost, "/pipeline/test-pipe", body)
	req.Header.Set("Authorization", "Bearer test-key-123")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rr.Code)
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Verify DB state
	var payload []byte
	err := db.QueryRow("SELECT payload FROM job_queue WHERE id = ?", resp.JobID).Scan(&payload)
	if err != nil {
		t.Fatalf("failed to query job payload: %v", err)
	}

	var event protocol.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("expected handle payload to be protocol.Event: %v", err)
	}
	if event.Type != "file.read" {
		t.Fatalf("expected explicit pipeline event type file.read, got %q", event.Type)
	}
	if got := event.Payload["input"]; got != "val" {
		t.Fatalf("expected payload input=val, got %v", got)
	}
}

func TestHandlePipelineTrigger_FanoutCreatesPerDispatchContext(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	rt := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			return &router.PipelineInfo{
				Name:        "test-pipe",
				Trigger:     "file.read",
				EntryStepID: "fallback",
			}
		},
		getEntryDispatchesFunc: func(pipelineName string, event protocol.Event) ([]router.Dispatch, error) {
			return []router.Dispatch{
				{Plugin: "echo", Command: "handle", Event: event, StepID: "step-a"},
				{Plugin: "fabric", Command: "handle", Event: event, StepID: "step-b"},
			}, nil
		},
	}

	server := setupTestServer(t, db, &mockRegistry{})
	server.router = rt

	req := httptest.NewRequest(http.MethodPost, "/pipeline/test-pipe", bytes.NewBufferString(`{"payload":{"x":1}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rr.Code)
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Verify multiple jobs enqueued (fanout)
	q := queue.New(db)
	_, total, err := q.ListJobs(context.Background(), queue.ListJobsFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 jobs, got %d", total)
	}

	// Check contexts and close rows before reading from context store.
	rows, err := db.Query("SELECT event_context_id FROM job_queue")
	if err != nil {
		t.Fatalf("query event_context_ids: %v", err)
	}

	var contextIDs []string
	for rows.Next() {
		var ctxID sql.NullString
		if err := rows.Scan(&ctxID); err != nil {
			_ = rows.Close()
			t.Fatalf("scan ctxID: %v", err)
		}
		if !ctxID.Valid || ctxID.String == "" {
			_ = rows.Close()
			t.Fatalf("job has no context")
		}
		contextIDs = append(contextIDs, ctxID.String)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("rows err: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rows: %v", err)
	}
	if len(contextIDs) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(contextIDs))
	}

	cs := state.NewContextStore(db)
	var pipelineInstanceID string
	var rootContextID string
	for _, contextID := range contextIDs {
		ctx, err := cs.Get(context.Background(), contextID)
		if err != nil {
			t.Fatalf("Get context %s: %v", contextID, err)
		}
		if ctx.PipelineName != "test-pipe" {
			t.Errorf("unexpected pipeline name %s", ctx.PipelineName)
		}
		// Trigger payload remains immediate request data; it should not become
		// durable baggage without explicit claims.
		var accumulated map[string]any
		if err := json.Unmarshal(ctx.AccumulatedJSON, &accumulated); err != nil {
			t.Fatalf("unmarshal AccumulatedJSON: %v", err)
		}
		if _, exists := accumulated["x"]; exists {
			t.Errorf("unexpected trigger payload key 'x' in AccumulatedJSON, got %v", accumulated)
		}
		gotPipelineInstanceID := state.PipelineInstanceIDFromAccumulated(ctx.AccumulatedJSON)
		if gotPipelineInstanceID == "" {
			t.Fatalf("expected pipeline instance id in context %s", contextID)
		}
		if pipelineInstanceID == "" {
			pipelineInstanceID = gotPipelineInstanceID
		} else if gotPipelineInstanceID != pipelineInstanceID {
			t.Fatalf("pipeline instance id mismatch: got %q want %q", gotPipelineInstanceID, pipelineInstanceID)
		}
		if ctx.ParentID == nil || *ctx.ParentID == "" {
			t.Fatalf("expected entry context %s to have shared root parent", contextID)
		}
		if rootContextID == "" {
			rootContextID = *ctx.ParentID
		} else if *ctx.ParentID != rootContextID {
			t.Fatalf("entry contexts parent ids differ: got %q want %q", *ctx.ParentID, rootContextID)
		}
	}

	rootCtx, err := cs.Get(context.Background(), rootContextID)
	if err != nil {
		t.Fatalf("Get root context %s: %v", rootContextID, err)
	}
	if rootCtx.StepID != "" {
		t.Fatalf("root context step_id = %q, want empty", rootCtx.StepID)
	}
	if got := state.PipelineInstanceIDFromAccumulated(rootCtx.AccumulatedJSON); got != pipelineInstanceID {
		t.Fatalf("root pipeline instance id = %q, want %q", got, pipelineInstanceID)
	}
}

func TestHandlePipelineTrigger_RootBaggageClaimsDurableContext(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	rt := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			return &router.PipelineInfo{
				Name:        "explicit-root",
				Trigger:     "web.url.detected",
				EntryStepID: "fetch",
			}
		},
		getEntryDispatchesFunc: func(pipelineName string, event protocol.Event) ([]router.Dispatch, error) {
			return []router.Dispatch{{Plugin: "web_fetch", Command: "handle", Event: event, StepID: "fetch"}}, nil
		},
		getNodeFunc: func(pipelineName string, stepID string) (dsl.Node, bool) {
			if pipelineName != "explicit-root" || stepID != "fetch" {
				return dsl.Node{}, false
			}
			return dsl.Node{
				ID:   "fetch",
				Kind: dsl.NodeKindUses,
				Uses: "web_fetch",
				Baggage: &dsl.BaggageSpec{Mappings: map[string]string{
					"web.url": "payload.url",
				}},
			}, true
		},
	}

	server := setupTestServer(t, db, &mockRegistry{})
	server.router = rt

	req := httptest.NewRequest(http.MethodPost, "/pipeline/explicit-root", bytes.NewBufferString(`{"payload":{"url":"http://example.test","message":"local only","status":"pending"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body=%s", rr.Code, rr.Body.String())
	}

	var contextID string
	if err := db.QueryRow("SELECT event_context_id FROM job_queue").Scan(&contextID); err != nil {
		t.Fatalf("query event_context_id: %v", err)
	}
	cs := state.NewContextStore(db)
	eventCtx, err := cs.Get(context.Background(), contextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(): %v", err)
	}

	var accumulated map[string]any
	if err := json.Unmarshal(eventCtx.AccumulatedJSON, &accumulated); err != nil {
		t.Fatalf("unmarshal AccumulatedJSON: %v", err)
	}
	if _, exists := accumulated["url"]; exists {
		t.Fatalf("root url leaked into explicit baggage context: %+v", accumulated)
	}
	if _, exists := accumulated["message"]; exists {
		t.Fatalf("root message leaked into explicit baggage context: %+v", accumulated)
	}
	web, ok := accumulated["web"].(map[string]any)
	if !ok {
		t.Fatalf("web baggage = %#v, want object", accumulated["web"])
	}
	if web["url"] != "http://example.test" {
		t.Fatalf("web.url = %#v, want http://example.test", web["url"])
	}
	if got := state.PipelineInstanceIDFromAccumulated(eventCtx.AccumulatedJSON); got == "" {
		t.Fatal("expected pipeline instance id in accumulated context")
	}
}

func TestHandlePipelineTrigger_RootBaggageMissingSourceRejectsTrigger(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	rt := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			return &router.PipelineInfo{
				Name:        "explicit-root",
				Trigger:     "web.url.detected",
				EntryStepID: "fetch",
			}
		},
		getEntryDispatchesFunc: func(pipelineName string, event protocol.Event) ([]router.Dispatch, error) {
			return []router.Dispatch{{Plugin: "web_fetch", Command: "handle", Event: event, StepID: "fetch"}}, nil
		},
		getNodeFunc: func(pipelineName string, stepID string) (dsl.Node, bool) {
			return dsl.Node{
				ID:   "fetch",
				Kind: dsl.NodeKindUses,
				Uses: "web_fetch",
				Baggage: &dsl.BaggageSpec{Mappings: map[string]string{
					"web.url": "payload.url",
				}},
			}, true
		},
	}

	server := setupTestServer(t, db, &mockRegistry{})
	server.router = rt

	req := httptest.NewRequest(http.MethodPost, "/pipeline/explicit-root", bytes.NewBufferString(`{"payload":{"message":"missing url"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "root baggage claims failed") || !strings.Contains(rr.Body.String(), "path not found") {
		t.Fatalf("response body = %s, want root baggage path not found", rr.Body.String())
	}

	q := queue.New(db)
	_, total, err := q.ListJobs(context.Background(), queue.ListJobsFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if total != 0 {
		t.Fatalf("jobs enqueued = %d, want 0", total)
	}

	var contextCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM event_context").Scan(&contextCount); err != nil {
		t.Fatalf("count event_context: %v", err)
	}
	if contextCount != 0 {
		t.Fatalf("event_context rows = %d, want 0", contextCount)
	}
}

func TestHandlePipelineTrigger_SyncFanoutRequiresAsync(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	rt := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			return &router.PipelineInfo{
				Name:          "test-pipe",
				Trigger:       "file.read",
				ExecutionMode: "synchronous",
			}
		},
		getEntryDispatchesFunc: func(pipelineName string, event protocol.Event) ([]router.Dispatch, error) {
			return []router.Dispatch{
				{Plugin: "echo", Command: "handle", Event: event, StepID: "step-a"},
				{Plugin: "fabric", Command: "handle", Event: event, StepID: "step-b"},
			}, nil
		},
	}

	server := setupTestServer(t, db, &mockRegistry{})
	server.router = rt

	req := httptest.NewRequest(http.MethodPost, "/pipeline/test-pipe", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleSystemReload_OK(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reloaded := false
	reloadFn := func(ctx context.Context) (ReloadResponse, error) {
		reloaded = true
		return ReloadResponse{Status: "ok", ReloadedAt: "2026-03-02T00:00:00Z"}, nil
	}

	server := setupTestServer(t, db, &mockRegistry{})
	server.reloadFunc = reloadFn

	req := httptest.NewRequest(http.MethodPost, "/system/reload", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	resp := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	var payload ReloadResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload.Status != "ok" {
		t.Fatalf("expected status ok, got %q", payload.Status)
	}
	if !reloaded {
		t.Fatal("reload function was not called")
	}
}

func TestHandleSystemReload_Error(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reloadFn := func(ctx context.Context) (ReloadResponse, error) {
		return ReloadResponse{Status: "error", Message: "locked"}, errors.New("locked")
	}

	server := setupTestServer(t, db, &mockRegistry{})
	server.reloadFunc = reloadFn

	req := httptest.NewRequest(http.MethodPost, "/system/reload", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	resp := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(resp, req)

	if resp.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", resp.Code)
	}
	if !strings.Contains(resp.Body.String(), "locked") {
		t.Fatalf("expected error response, got: %s", resp.Body.String())
	}
}

func TestServerWaitServeStoppedAfterCancel(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})
	server.config.Listen = "127.0.0.1:0"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := server.WaitServeStopped(waitCtx); err != nil {
		t.Fatalf("wait for serve stop: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server Start did not return")
	}
}

// TestPhase2DirectPluginEnqueueHonorsMaxAttempts reproduces P2-02: a direct
// API trigger (/plugin/<name>/<command>) must stamp the plugin-configured
// retry.max_attempts onto the enqueued job, rather than letting the queue
// fall back to its global default. Live pentest evidence:
// fetch.retry.max_attempts: 1 was silently demoted to max_attempts=4.
func TestPhase2DirectPluginEnqueueHonorsMaxAttempts(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reg := &mockRegistry{
		plugins: map[string]*plugin.Plugin{
			"echo": {
				Name: "echo",
				Commands: plugin.Commands{
					{Name: "health", Type: plugin.CommandTypeRead},
				},
			},
		},
	}
	server := setupTestServer(t, db, reg)
	server.config.RuntimeConfig = &config.Config{
		Plugins: map[string]config.PluginConf{
			"echo": {
				Enabled: true,
				Retry:   &config.RetryConfig{MaxAttempts: 1},
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/plugin/echo/health", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var maxAttempts int
	if err := db.QueryRow("SELECT max_attempts FROM job_queue WHERE id = ?", resp.JobID).Scan(&maxAttempts); err != nil {
		t.Fatalf("query max_attempts: %v", err)
	}
	if maxAttempts != 1 {
		t.Fatalf("direct-plugin enqueue stamped MaxAttempts=%d, want 1 (from plugin config) — P2-02 regression", maxAttempts)
	}
}

// TestPhase2PipelineAPIEnqueueHonorsMaxAttempts reproduces P2-02 on the
// pipeline-API entry path (POST /pipeline/<name>) — every dispatched plugin
// must receive its plugin-configured retry.max_attempts at enqueue time.
func TestPhase2PipelineAPIEnqueueHonorsMaxAttempts(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	rt := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			if name == "p2-pipe" {
				return &router.PipelineInfo{Name: "p2-pipe", Trigger: "file.read"}
			}
			return nil
		},
		getEntryDispatchesFunc: func(_ string, event protocol.Event) ([]router.Dispatch, error) {
			return []router.Dispatch{
				{Plugin: "echo", Command: "handle", Event: event, StepID: "step-a"},
			}, nil
		},
	}
	server := setupTestServer(t, db, &mockRegistry{})
	server.router = rt
	server.config.RuntimeConfig = &config.Config{
		Plugins: map[string]config.PluginConf{
			"echo": {
				Enabled: true,
				Retry:   &config.RetryConfig{MaxAttempts: 2},
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/pipeline/p2-pipe", bytes.NewBufferString(`{"payload":{"x":1}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	var maxAttempts int
	if err := db.QueryRow("SELECT max_attempts FROM job_queue WHERE id = ?", resp.JobID).Scan(&maxAttempts); err != nil {
		t.Fatalf("query max_attempts: %v", err)
	}
	if maxAttempts != 2 {
		t.Fatalf("pipeline-API enqueue stamped MaxAttempts=%d, want 2 (from plugin config) — P2-02 regression", maxAttempts)
	}
}

// TestPhase2DirectPluginEnqueueFallsBackToDefaultsWithoutPluginConfig asserts
// the no-plugin-config path falls back to the global default rather than
// stamping zero (which would let the queue's own default of 4 apply).
func TestPhase2DirectPluginEnqueueFallsBackToDefaultsWithoutPluginConfig(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	reg := &mockRegistry{
		plugins: map[string]*plugin.Plugin{
			"echo": {
				Name:     "echo",
				Commands: plugin.Commands{{Name: "health", Type: plugin.CommandTypeRead}},
			},
		},
	}
	server := setupTestServer(t, db, reg)
	server.config.RuntimeConfig = &config.Config{
		Plugins: map[string]config.PluginConf{}, // empty — no per-plugin retry config
	}

	req := httptest.NewRequest(http.MethodPost, "/plugin/echo/health", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := config.DefaultPluginConf().Retry.MaxAttempts
	var maxAttempts int
	if err := db.QueryRow("SELECT max_attempts FROM job_queue WHERE id = ?", resp.JobID).Scan(&maxAttempts); err != nil {
		t.Fatalf("query max_attempts: %v", err)
	}
	if maxAttempts != want {
		t.Fatalf("default-config enqueue stamped MaxAttempts=%d, want default %d", maxAttempts, want)
	}
}
