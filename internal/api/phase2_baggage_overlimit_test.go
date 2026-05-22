package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
)

// TestPhase2BaggageOverLimitReturnsHTTP413NoRow characterizes P2-04:
// pipeline-API ingress with baggage claims that would produce an
// accumulated_json larger than the configured 1 MiB cap must return HTTP 413
// with a structured `bytes_actual`/`bytes_limit` hint AND must NOT persist
// any event_context row. Before the admission boundary fix the handler
// returned an opaque 500 with `{"error":"failed to create event context"}`.
func TestPhase2BaggageOverLimitReturnsHTTP413NoRow(t *testing.T) {
	t.Parallel()

	type overlimitCase struct {
		name      string
		dataBytes int
	}

	cases := []overlimitCase{
		{name: "1024 KB", dataBytes: 1024 * 1024},
		{name: "2048 KB", dataBytes: 2048 * 1024},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			db := setupTestDB(t)
			pipelineInfo := &router.PipelineInfo{
				Name:          "overlimit-test",
				EntryStepID:   "entry",
				ExecutionMode: "asynchronous",
			}
			reg := &mockRegistry{}
			r := &mockRouter{
				getPipelineByNameFunc: func(name string) *router.PipelineInfo {
					if name == pipelineInfo.Name {
						return pipelineInfo
					}
					return nil
				},
				getEntryDispatchesFunc: func(name string, _ protocol.Event) ([]router.Dispatch, error) {
					return []router.Dispatch{{
						Plugin:       "sink",
						Command:      "handle",
						PipelineName: name,
						StepID:       "entry",
					}}, nil
				},
				getNodeFunc: func(_, _ string) (dsl.Node, bool) {
					return dsl.Node{
						ID:   "entry",
						Kind: dsl.NodeKindUses,
						Uses: "sink",
						Baggage: &dsl.BaggageSpec{
							Mappings: map[string]string{"data": "payload.data"},
						},
					}, true
				},
			}

			server := setupTestServerWithRouter(t, db, reg, r)

			payload := strings.Repeat("a", tc.dataBytes)
			body, err := json.Marshal(map[string]any{
				"payload": map[string]any{
					"data": payload,
				},
			})
			if err != nil {
				t.Fatalf("marshal request body: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/pipeline/overlimit-test", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-key-123")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()

			server.setupRoutes().ServeHTTP(rr, req)

			if rr.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413; body=%s", rr.Code, rr.Body.String())
			}

			var resp BaggageOverlimitResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v; body=%s", err, rr.Body.String())
			}
			if resp.Error == "" {
				t.Fatalf("response missing error field; body=%s", rr.Body.String())
			}
			if resp.BytesLimit != int64(state.DefaultMaxContextBytes) {
				t.Fatalf("bytes_limit = %d, want %d", resp.BytesLimit, state.DefaultMaxContextBytes)
			}
			if resp.BytesActual <= resp.BytesLimit {
				t.Fatalf("bytes_actual = %d, expected > bytes_limit (%d)", resp.BytesActual, resp.BytesLimit)
			}

			if got := countAPIEventContexts(t, db); got != 0 {
				t.Fatalf("event_context rows = %d, want 0 (no row should be written for rejected over-limit baggage)", got)
			}
		})
	}
}

// TestPhase2BaggageUnderLimitStillSucceeds confirms the admission gate is
// not over-conservative: a payload comfortably under 1 MiB still routes
// through the existing path and writes its event_context rows.
func TestPhase2BaggageUnderLimitStillSucceeds(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	pipelineInfo := &router.PipelineInfo{
		Name:          "underlimit-test",
		EntryStepID:   "entry",
		ExecutionMode: "asynchronous",
	}
	reg := &mockRegistry{}
	r := &mockRouter{
		getPipelineByNameFunc: func(name string) *router.PipelineInfo {
			if name == pipelineInfo.Name {
				return pipelineInfo
			}
			return nil
		},
		getEntryDispatchesFunc: func(name string, _ protocol.Event) ([]router.Dispatch, error) {
			return []router.Dispatch{{
				Plugin:       "sink",
				Command:      "handle",
				PipelineName: name,
				StepID:       "entry",
			}}, nil
		},
		getNodeFunc: func(_, _ string) (dsl.Node, bool) {
			return dsl.Node{
				ID:   "entry",
				Kind: dsl.NodeKindUses,
				Uses: "sink",
				Baggage: &dsl.BaggageSpec{
					Mappings: map[string]string{"data": "payload.data"},
				},
			}, true
		},
	}

	server := setupTestServerWithRouter(t, db, reg, r)

	body, err := json.Marshal(map[string]any{
		"payload": map[string]any{"data": strings.Repeat("a", 1024)},
	})
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/pipeline/underlimit-test", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key-123")
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	if got := countAPIEventContexts(t, db); got != 2 {
		// One pipeline-instance root + one entry context.
		t.Fatalf("event_context rows = %d, want 2 (root + entry)", got)
	}
}

func countAPIEventContexts(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM event_context;`).Scan(&n); err != nil {
		t.Fatalf("count event_context: %v", err)
	}
	return n
}
