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
	out := SubsFromResponse(nil, 0, nil)
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
		out := SubsFromResponse(c, 0, nil)
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
	out := SubsFromResponse(in, 0, nil)
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
	out := SubsFromResponse(big, 0, nil)
	if len(out) != MaxSubsPerRecord {
		t.Errorf("not capped: got %d", len(out))
	}
}

func TestSubsFromResponse_HonorsManifestCapWithinHardUpper(t *testing.T) {
	// A manifest declaring max_subs=64 should keep the first 64 spans.
	big := make([]any, 200)
	for i := range big {
		big[i] = map[string]any{"name": "x"}
	}
	out := SubsFromResponse(big, 64, nil)
	if len(out) != 64 {
		t.Errorf("manifest cap=64 not honored: got %d", len(out))
	}
}

func TestSubsFromResponse_AcceptsHardUpperExactly(t *testing.T) {
	big := make([]any, MaxSubsHardUpper*2)
	for i := range big {
		big[i] = map[string]any{"name": "x"}
	}
	out := SubsFromResponse(big, MaxSubsHardUpper, nil)
	if len(out) != MaxSubsHardUpper {
		t.Errorf("hard-upper cap not honored: got %d want %d", len(out), MaxSubsHardUpper)
	}
}

func TestSubsFromResponse_RejectsAboveHardUpperFallsBackToDefault(t *testing.T) {
	// A maxSubs above the hard upper is treated as garbage and falls back
	// to the default; manifest validation is the front-line defense, this
	// is belt-and-braces in the runtime.
	big := make([]any, 200)
	for i := range big {
		big[i] = map[string]any{"name": "x"}
	}
	out := SubsFromResponse(big, MaxSubsHardUpper+1, nil)
	if len(out) != MaxSubsPerRecord {
		t.Errorf("above-hard-upper should fall back to default: got %d", len(out))
	}
}

func TestSubsFromResponse_DropsEntireListWhenByteCapExceeded(t *testing.T) {
	// 8 entries × 4 KiB each = 32 KiB, well over MaxSubsBytesPerRecord
	// (16 KiB). Each entry is under any plausible per-entry cap; the
	// count cap (32) is also not hit. Only the byte cap fires.
	big := make([]any, 8)
	bigString := make([]byte, 4*1024)
	for i := range bigString {
		bigString[i] = 'x'
	}
	for i := range big {
		big[i] = map[string]any{
			"name":   "test.bloat",
			"dur_ns": 1,
			"junk":   string(bigString),
		}
	}

	out := SubsFromResponse(big, 0, nil)
	if len(out) != 0 {
		t.Errorf("expected entire subs list dropped when byte cap exceeded; got %d entries", len(out))
	}
}

func TestSubsFromResponse_AcceptsListUnderByteCap(t *testing.T) {
	// 8 small entries — well under both caps. Should pass through.
	small := make([]any, 8)
	for i := range small {
		small[i] = map[string]any{"name": "small", "dur_ns": 100}
	}
	out := SubsFromResponse(small, 0, nil)
	if len(out) != 8 {
		t.Errorf("expected 8 entries under byte cap; got %d", len(out))
	}
}

func TestSubsFromResponse_ByteCapFiresAfterCountCap(t *testing.T) {
	// 100 entries, each tiny. Count cap (32) takes 32 of them; the
	// resulting 32 entries should be well under the byte cap and pass.
	many := make([]any, 100)
	for i := range many {
		many[i] = map[string]any{"name": "small", "dur_ns": int64(i)}
	}
	out := SubsFromResponse(many, 0, nil)
	if len(out) != MaxSubsPerRecord {
		t.Errorf("count cap should fire first; got %d entries, want %d", len(out), MaxSubsPerRecord)
	}
}

func TestResolveMaxSubs(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{-1, MaxSubsPerRecord},
		{0, MaxSubsPerRecord},
		{1, 1},
		{32, 32},
		{MaxSubsHardUpper, MaxSubsHardUpper},
		{MaxSubsHardUpper + 1, MaxSubsPerRecord},
		{10000, MaxSubsPerRecord},
	}
	for _, c := range cases {
		got := resolveMaxSubs(c.in)
		if got != c.want {
			t.Errorf("resolveMaxSubs(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
