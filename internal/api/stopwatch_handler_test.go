package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
)

// mockStopwatchReader satisfies StopwatchReader for handler tests so
// percentile aggregation is exercised without a real SQLite table.
type mockStopwatchReader struct {
	rowsFunc func(ctx context.Context, plugin string, since time.Time) ([]state.StopwatchAggregationRow, error)
	gotPlugin string
	gotSince  time.Time
}

func (m *mockStopwatchReader) StopwatchRowsForPlugin(ctx context.Context, plugin string, since time.Time) ([]state.StopwatchAggregationRow, error) {
	m.gotPlugin = plugin
	m.gotSince = since
	if m.rowsFunc != nil {
		return m.rowsFunc(ctx, plugin, since)
	}
	return nil, nil
}

// setupServerWithStopwatch wires a Server with a caller-supplied
// stopwatch reader (and a configurable token list so include_subs
// scope-gating can be exercised).
func setupServerWithStopwatch(t *testing.T, sw StopwatchReader, tokens []auth.TokenConfig) *Server {
	t.Helper()
	db := setupTestDB(t)
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	cfg := Config{
		Listen: "localhost:8080",
		Tokens: tokens,
	}
	return New(cfg, q, &mockRegistry{}, &mockRouter{}, &mockWaiter{}, cs,
		state.NewAdmitter(q, state.DefaultMaxContextBytes), sw, hub, slog.Default())
}

func TestPercentileNs(t *testing.T) {
	t.Parallel()
	// Ten samples: 10ms..100ms in dur_ns (10_000_000 .. 100_000_000).
	durs := []int64{
		10_000_000, 20_000_000, 30_000_000, 40_000_000, 50_000_000,
		60_000_000, 70_000_000, 80_000_000, 90_000_000, 100_000_000,
	}
	// Nearest-rank: p50 -> ceil(0.5*10)=5 -> idx 4 -> 50ms.
	if got := percentileNs(durs, 0.50); got != 50_000_000 {
		t.Errorf("p50 = %d, want 50_000_000", got)
	}
	// p95 -> ceil(0.95*10)=10 -> idx 9 -> 100ms.
	if got := percentileNs(durs, 0.95); got != 100_000_000 {
		t.Errorf("p95 = %d, want 100_000_000", got)
	}
	// p99 -> same as p95 with n=10.
	if got := percentileNs(durs, 0.99); got != 100_000_000 {
		t.Errorf("p99 = %d, want 100_000_000", got)
	}
	if got := percentileNs(nil, 0.5); got != 0 {
		t.Errorf("empty slice = %d, want 0", got)
	}
	// Input must not be mutated.
	mutCheck := []int64{30, 10, 20}
	_ = percentileNs(mutCheck, 0.5)
	if mutCheck[0] != 30 || mutCheck[1] != 10 || mutCheck[2] != 20 {
		t.Errorf("percentileNs mutated input: %v", mutCheck)
	}
}

func TestParseStopwatchWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"5m", 5 * time.Minute, true},
		{"1h", time.Hour, true},
		{"24h", 24 * time.Hour, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"30d", 30 * 24 * time.Hour, true},
		{"", 0, false},
		{"7m", 0, false},
		{"30s", 0, false},
		{"1d", 0, false},
		{"14d", 0, false},
	}
	for _, c := range cases {
		got, ok := parseStopwatchWindow(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseStopwatchWindow(%q) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestAggregateStopwatch_PerStepPercentiles(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)

	rows := []state.StopwatchAggregationRow{}
	// Step "fetch": 10 samples evenly spaced through the window,
	// durations 10ms..100ms.
	for i := 0; i < 10; i++ {
		rows = append(rows, state.StopwatchAggregationRow{
			StepID:     "fetch",
			DurNs:      int64((i + 1) * 10_000_000),
			RecordedAt: since.Add(time.Duration(i+1) * 5 * time.Minute),
		})
	}
	// Step "transform": 4 samples, all 200ms.
	for i := 0; i < 4; i++ {
		rows = append(rows, state.StopwatchAggregationRow{
			StepID:     "transform",
			DurNs:      200_000_000,
			RecordedAt: since.Add(time.Duration(i+1) * 10 * time.Minute),
		})
	}

	resp := aggregateStopwatch("withings", "1h", time.Hour, now, since, rows, false)

	if resp.Plugin != "withings" {
		t.Errorf("Plugin = %q, want withings", resp.Plugin)
	}
	if resp.Window != "1h" {
		t.Errorf("Window = %q, want 1h", resp.Window)
	}
	if len(resp.Steps) != 2 {
		t.Fatalf("Steps len = %d, want 2", len(resp.Steps))
	}

	stepByID := map[string]StopwatchStepStats{}
	for _, s := range resp.Steps {
		stepByID[s.Step] = s
	}

	fetch := stepByID["fetch"]
	if fetch.SampleCount != 10 {
		t.Errorf("fetch.SampleCount = %d, want 10", fetch.SampleCount)
	}
	if fetch.P50Ms != 50 {
		t.Errorf("fetch.P50Ms = %d, want 50", fetch.P50Ms)
	}
	if fetch.P95Ms != 100 {
		t.Errorf("fetch.P95Ms = %d, want 100", fetch.P95Ms)
	}
	if fetch.P99Ms != 100 {
		t.Errorf("fetch.P99Ms = %d, want 100", fetch.P99Ms)
	}
	if len(fetch.TrendP95Ms) != stopwatchTrendBuckets {
		t.Errorf("fetch.TrendP95Ms len = %d, want %d", len(fetch.TrendP95Ms), stopwatchTrendBuckets)
	}
	// 10 samples across stopwatchTrendBuckets buckets (1h/60 = 1 min
	// each). Samples spaced at +5, +10, +15...+50 minutes land in
	// distinct buckets and must yield a non-zero p95 somewhere.
	nonZero := 0
	for _, v := range fetch.TrendP95Ms {
		if v > 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Errorf("fetch.TrendP95Ms all zero: %v", fetch.TrendP95Ms)
	}

	transform := stepByID["transform"]
	if transform.SampleCount != 4 {
		t.Errorf("transform.SampleCount = %d, want 4", transform.SampleCount)
	}
	if transform.P50Ms != 200 || transform.P95Ms != 200 || transform.P99Ms != 200 {
		t.Errorf("transform percentiles = %d/%d/%d, want all 200",
			transform.P50Ms, transform.P95Ms, transform.P99Ms)
	}
}

func TestAggregateStopwatch_LongWindowsBucketBound(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		window string
		dur    time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"1h", time.Hour},
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
	}
	for _, c := range cases {
		since := now.Add(-c.dur)
		// 20 samples spread evenly across the window so at least one
		// trend bucket is non-zero for every window length.
		rows := make([]state.StopwatchAggregationRow, 0, 20)
		for i := 0; i < 20; i++ {
			rows = append(rows, state.StopwatchAggregationRow{
				StepID:     "poll",
				DurNs:      int64((i + 1) * 10_000_000),
				RecordedAt: since.Add(time.Duration(i+1) * c.dur / 21),
			})
		}

		resp := aggregateStopwatch("p", c.window, c.dur, now, since, rows, false)
		if len(resp.Steps) != 1 {
			t.Fatalf("window %s: Steps len = %d, want 1", c.window, len(resp.Steps))
		}
		trend := resp.Steps[0].TrendP95Ms
		// Acceptance: bucket count bounded 50-200 regardless of window.
		if len(trend) < 50 || len(trend) > 200 {
			t.Errorf("window %s: trend len = %d, want within [50,200]", c.window, len(trend))
		}
		nonZero := 0
		for _, v := range trend {
			if v > 0 {
				nonZero++
			}
		}
		if nonZero == 0 {
			t.Errorf("window %s: trend_p95_ms all zero with samples present", c.window)
		}
	}
}

func TestAggregateStopwatch_SubsHonorsScope(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	since := now.Add(-time.Hour)

	subsJSON := []byte(`[
		{"name":"fetch.http_get","dur_ns":30000000},
		{"name":"fetch.body_read","dur_ns":10000000}
	]`)
	rows := []state.StopwatchAggregationRow{
		{StepID: "fetch", DurNs: 50_000_000, RecordedAt: since.Add(time.Minute), SubsJSON: subsJSON},
		{StepID: "fetch", DurNs: 70_000_000, RecordedAt: since.Add(2 * time.Minute), SubsJSON: subsJSON},
	}

	// include_subs=false: subs omitted regardless of payload presence.
	respOff := aggregateStopwatch("p", "1h", time.Hour, now, since, rows, false)
	if len(respOff.Steps) != 1 {
		t.Fatalf("Steps len = %d, want 1", len(respOff.Steps))
	}
	if len(respOff.Steps[0].Subs) != 0 {
		t.Errorf("subs leaked with include_subs=false: %v", respOff.Steps[0].Subs)
	}

	// include_subs=true: both sub-spans aggregated and sorted.
	respOn := aggregateStopwatch("p", "1h", time.Hour, now, since, rows, true)
	if len(respOn.Steps) != 1 {
		t.Fatalf("Steps len = %d, want 1", len(respOn.Steps))
	}
	subs := respOn.Steps[0].Subs
	if len(subs) != 2 {
		t.Fatalf("Subs len = %d, want 2", len(subs))
	}
	if subs[0].Name != "fetch.body_read" || subs[1].Name != "fetch.http_get" {
		t.Errorf("Subs not sorted alphabetically: %v", subs)
	}
	for _, sub := range subs {
		if sub.SampleCount != 2 {
			t.Errorf("sub %q SampleCount = %d, want 2", sub.Name, sub.SampleCount)
		}
	}
	bodyRead := subs[0]
	if bodyRead.P50Ms != 10 || bodyRead.P95Ms != 10 || bodyRead.P99Ms != 10 {
		t.Errorf("body_read percentiles = %d/%d/%d, want all 10",
			bodyRead.P50Ms, bodyRead.P95Ms, bodyRead.P99Ms)
	}
}

func TestHandleStopwatch_HappyPath(t *testing.T) {
	t.Parallel()
	reader := &mockStopwatchReader{
		rowsFunc: func(_ context.Context, plugin string, since time.Time) ([]state.StopwatchAggregationRow, error) {
			// Five rows for one step.
			out := make([]state.StopwatchAggregationRow, 0, 5)
			for i := 1; i <= 5; i++ {
				out = append(out, state.StopwatchAggregationRow{
					StepID:     "poll",
					DurNs:      int64(i * 20_000_000),
					RecordedAt: since.Add(time.Duration(i) * time.Minute),
				})
			}
			return out, nil
		},
	}
	server := setupServerWithStopwatch(t, reader, []auth.TokenConfig{
		{Token: "test-key-123", Scopes: []string{"*"}},
	})

	req := httptest.NewRequest(http.MethodGet, "/stopwatch/withings?window=1h", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if reader.gotPlugin != "withings" {
		t.Errorf("reader.gotPlugin = %q, want withings", reader.gotPlugin)
	}
	if time.Since(reader.gotSince) < time.Hour-time.Minute || time.Since(reader.gotSince) > time.Hour+time.Minute {
		t.Errorf("reader.gotSince = %v, want ~1h ago", reader.gotSince)
	}

	var resp StopwatchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Plugin != "withings" || resp.Window != "1h" {
		t.Errorf("Plugin/Window = %q/%q, want withings/1h", resp.Plugin, resp.Window)
	}
	if len(resp.Steps) != 1 || resp.Steps[0].Step != "poll" {
		t.Fatalf("Steps = %v, want one [poll] step", resp.Steps)
	}
	if resp.Steps[0].SampleCount != 5 {
		t.Errorf("SampleCount = %d, want 5", resp.Steps[0].SampleCount)
	}
}

func TestHandleStopwatch_LongWindowHappyPath(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		window string
		want   time.Duration
	}{
		{"7d", 7 * 24 * time.Hour},
		{"30d", 30 * 24 * time.Hour},
	} {
		reader := &mockStopwatchReader{
			rowsFunc: func(_ context.Context, _ string, since time.Time) ([]state.StopwatchAggregationRow, error) {
				return []state.StopwatchAggregationRow{{
					StepID:     "poll",
					DurNs:      20_000_000,
					RecordedAt: since.Add(time.Hour),
				}}, nil
			},
		}
		server := setupServerWithStopwatch(t, reader, []auth.TokenConfig{
			{Token: "test-key-123", Scopes: []string{"*"}},
		})

		req := httptest.NewRequest(http.MethodGet, "/stopwatch/withings?window="+tc.window, nil)
		req.Header.Set("Authorization", "Bearer test-key-123")
		rr := httptest.NewRecorder()
		server.setupRoutes().ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("window %s: status = %d, want 200; body=%s", tc.window, rr.Code, rr.Body.String())
		}
		lookback := time.Since(reader.gotSince)
		if lookback < tc.want-time.Minute || lookback > tc.want+time.Minute {
			t.Errorf("window %s: lookback = %v, want ~%v", tc.window, lookback, tc.want)
		}
		var resp StopwatchResponse
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("window %s: decode: %v", tc.window, err)
		}
		if resp.Window != tc.window {
			t.Errorf("window %s: resp.Window = %q", tc.window, resp.Window)
		}
		if len(resp.Steps) != 1 || len(resp.Steps[0].TrendP95Ms) != stopwatchTrendBuckets {
			t.Errorf("window %s: unexpected steps/trend: %+v", tc.window, resp.Steps)
		}
	}
}

func TestHandleStopwatch_InvalidWindowReturns400(t *testing.T) {
	t.Parallel()
	server := setupServerWithStopwatch(t, &mockStopwatchReader{}, []auth.TokenConfig{
		{Token: "test-key-123", Scopes: []string{"*"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/stopwatch/withings?window=7m", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleStopwatch_NilReaderReturns503(t *testing.T) {
	t.Parallel()
	server := setupServerWithStopwatch(t, nil, []auth.TokenConfig{
		{Token: "test-key-123", Scopes: []string{"*"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/stopwatch/withings", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleStopwatch_RequiresAuth(t *testing.T) {
	t.Parallel()
	server := setupServerWithStopwatch(t, &mockStopwatchReader{}, []auth.TokenConfig{
		{Token: "test-key-123", Scopes: []string{"*"}},
	})
	req := httptest.NewRequest(http.MethodGet, "/stopwatch/withings", nil)
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleStopwatch_IncludeSubsScopeGate(t *testing.T) {
	t.Parallel()
	subsJSON := []byte(`[{"name":"fetch.http_get","dur_ns":30000000}]`)
	mkRows := func(since time.Time) []state.StopwatchAggregationRow {
		return []state.StopwatchAggregationRow{{
			StepID:     "fetch",
			DurNs:      50_000_000,
			RecordedAt: since.Add(time.Minute),
			SubsJSON:   subsJSON,
		}}
	}

	// Token with only jobs:status:ro — base stats allowed, sub-spans
	// must be omitted because subs_json is plugin-supplied unvalidated
	// content (HAZARD comment on state.RecordStopwatch).
	srvNoResult := setupServerWithStopwatch(t,
		&mockStopwatchReader{rowsFunc: func(_ context.Context, _ string, since time.Time) ([]state.StopwatchAggregationRow, error) {
			return mkRows(since), nil
		}},
		[]auth.TokenConfig{{Token: "ro-only", Scopes: []string{"jobs:status:ro"}}},
	)
	req := httptest.NewRequest(http.MethodGet, "/stopwatch/p?include_subs=true", nil)
	req.Header.Set("Authorization", "Bearer ro-only")
	rr := httptest.NewRecorder()
	srvNoResult.setupRoutes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp StopwatchResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Steps) != 1 {
		t.Fatalf("Steps = %v, want 1", resp.Steps)
	}
	if len(resp.Steps[0].Subs) != 0 {
		t.Errorf("subs leaked without jobs:result:ro: %v", resp.Steps[0].Subs)
	}

	// Token with wildcard — sub-spans included.
	srvFull := setupServerWithStopwatch(t,
		&mockStopwatchReader{rowsFunc: func(_ context.Context, _ string, since time.Time) ([]state.StopwatchAggregationRow, error) {
			return mkRows(since), nil
		}},
		[]auth.TokenConfig{{Token: "full", Scopes: []string{"*"}}},
	)
	req2 := httptest.NewRequest(http.MethodGet, "/stopwatch/p?include_subs=true", nil)
	req2.Header.Set("Authorization", "Bearer full")
	rr2 := httptest.NewRecorder()
	srvFull.setupRoutes().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr2.Code, rr2.Body.String())
	}
	var resp2 StopwatchResponse
	if err := json.NewDecoder(rr2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Steps) != 1 || len(resp2.Steps[0].Subs) != 1 {
		t.Errorf("expected one sub with wildcard token; got %v", resp2.Steps)
	}
}
