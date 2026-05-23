package stopwatch

import (
	"testing"
	"time"
)

// BenchmarkCaptureCycle measures the supervisor-side overhead of timing
// a single invocation: New -> MarkSpawn -> Finish -> Attach. The plugin
// spawn itself is NOT in this loop; we only time the wrap.
//
// Acceptance: per-op time must be well under 100µs on a modern host.
func BenchmarkCaptureCycle(b *testing.B) {
	ctx := map[string]any{}
	b.ResetTimer()
	for b.Loop() {
		sw := New("p", "s", 1)
		sw.MarkSpawn()
		rec := sw.Finish(time.Now(), StatusOK, 0, nil)
		Attach(ctx, rec)
	}
}

// BenchmarkCaptureCycle_ErrorPath ensures error/timeout statuses do not
// pay a different cost than ok — the supervisor measures uniformly.
func BenchmarkCaptureCycle_ErrorPath(b *testing.B) {
	ctx := map[string]any{}
	b.ResetTimer()
	for b.Loop() {
		sw := New("p", "s", 1)
		sw.MarkSpawn()
		rec := sw.Finish(time.Now(), StatusTimeout, 0, nil)
		Attach(ctx, rec)
	}
}

// BenchmarkSubsFromResponse_Capped measures defensive parsing of the
// plugin's sub-span list at the cap boundary.
func BenchmarkSubsFromResponse_Capped(b *testing.B) {
	big := make([]any, MaxSubsPerRecord*2)
	for i := range big {
		big[i] = map[string]any{"name": "x", "dur_ns": int64(i)}
	}
	b.ResetTimer()
	for b.Loop() {
		_ = SubsFromResponse(big, nil)
	}
}
