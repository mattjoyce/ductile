package stopwatch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRecord_JSONShape_StableFieldOrder(t *testing.T) {
	rec := Record{
		PluginID:      "withings",
		StepName:      "fetch",
		Attempt:       1,
		EnterWallNs:   1000,
		ExitWallNs:    2000,
		DurNs:         1000,
		RuntimePreNs:  10,
		RuntimePostNs: 5,
		Status:        StatusOK,
		Subs:          []map[string]any{},
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"plugin_id":"withings","step_name":"fetch","attempt":1,"enter_wall_ns":1000,"exit_wall_ns":2000,"dur_ns":1000,"runtime_pre_ns":10,"runtime_post_ns":5,"status":"ok","subs":[]}`
	if string(b) != want {
		t.Errorf("JSON shape drifted\nwant: %s\n got: %s", want, string(b))
	}
}

func TestNew_CapturesEnterWallImmediately(t *testing.T) {
	before := time.Now().UnixNano()
	sw := New("p", "s", 1)
	after := time.Now().UnixNano()

	got := sw.enterWall.UnixNano()
	if got < before || got > after {
		t.Errorf("enterWall not in [%d, %d]: %d", before, after, got)
	}
}

func TestFinish_DurUsesMonotonicNotWall(t *testing.T) {
	sw := New("p", "s", 1)
	sw.MarkSpawn()
	// Sleep just enough to be observable but well under the test budget.
	time.Sleep(2 * time.Millisecond)
	rec := sw.Finish(time.Now(), StatusOK, 0, nil)

	if rec.DurNs <= 0 {
		t.Errorf("dur_ns must be positive: %d", rec.DurNs)
	}
	if rec.DurNs < 1_000_000 { // at least ~1ms given we slept 2ms
		t.Errorf("dur_ns too small for 2ms sleep: %d", rec.DurNs)
	}
}

func TestFinish_RuntimePreCapturesPreSpawnGap(t *testing.T) {
	sw := New("p", "s", 1)
	time.Sleep(1 * time.Millisecond)
	sw.MarkSpawn()
	rec := sw.Finish(time.Now(), StatusOK, 0, nil)

	if rec.RuntimePreNs <= 0 {
		t.Errorf("runtime_pre_ns must be positive when MarkSpawn was delayed: %d", rec.RuntimePreNs)
	}
}

func TestFinish_NilStopwatchReturnsCaptureError(t *testing.T) {
	var sw *Stopwatch
	rec := sw.Finish(time.Now(), StatusOK, 0, nil)
	if rec.Status != StatusCaptureError {
		t.Errorf("nil stopwatch must yield capture_error, got %q", rec.Status)
	}
	if rec.Subs == nil {
		t.Errorf("Subs must be non-nil even on capture_error")
	}
}

func TestFinish_SubsCappedAt32(t *testing.T) {
	sw := New("p", "s", 1)
	sw.MarkSpawn()
	big := make([]map[string]any, 100)
	for i := range big {
		big[i] = map[string]any{"name": "x"}
	}
	rec := sw.Finish(time.Now(), StatusOK, 0, big)
	if len(rec.Subs) != MaxSubsPerRecord {
		t.Errorf("subs not capped: got %d want %d", len(rec.Subs), MaxSubsPerRecord)
	}
}

func TestFinish_NilSubsBecomesEmptySlice(t *testing.T) {
	sw := New("p", "s", 1)
	sw.MarkSpawn()
	rec := sw.Finish(time.Now(), StatusOK, 0, nil)
	if rec.Subs == nil {
		t.Errorf("subs must never be nil after Finish")
	}
	if len(rec.Subs) != 0 {
		t.Errorf("subs should be empty: got %v", rec.Subs)
	}
}

func TestSubsFromResponse_HandlesNil(t *testing.T) {
	out := SubsFromResponse(nil, nil)
	if out == nil || len(out) != 0 {
		t.Errorf("nil input must yield empty slice, got %v", out)
	}
}

func TestSubsFromResponse_HandlesMalformed(t *testing.T) {
	cases := []any{
		"not-a-list",
		42,
		map[string]any{"not": "a list"},
		[]any{"not-a-map", 7, nil},
	}
	for _, c := range cases {
		out := SubsFromResponse(c, nil)
		if out == nil {
			t.Errorf("must never return nil for input %v", c)
		}
	}
}

func TestSubsFromResponse_ParsesValidShape(t *testing.T) {
	in := []any{
		map[string]any{"name": "db_query", "dur_ns": 100},
		map[string]any{"name": "http_call", "dur_ns": 50},
	}
	out := SubsFromResponse(in, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 subs, got %d", len(out))
	}
	if out[0]["name"] != "db_query" {
		t.Errorf("first sub-span lost name field")
	}
}

func TestSubsFromResponse_CapsAndDoesNotPanic(t *testing.T) {
	big := make([]any, 100)
	for i := range big {
		big[i] = map[string]any{"name": "x"}
	}
	out := SubsFromResponse(big, nil)
	if len(out) != MaxSubsPerRecord {
		t.Errorf("not capped: got %d", len(out))
	}
}
