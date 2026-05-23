package stopwatch

import (
	"testing"
	"time"
)

// BenchmarkCaptureCycle measures the supervisor-side overhead of timing
// a single invocation: New -> MarkSpawn -> Finish. The plugin spawn
// itself is NOT in this loop and persistence to the DB is measured
// separately (see internal/state benchmarks); we only time the capture.
//
// Acceptance: per-op time must be well under 100µs on a modern host.
func BenchmarkCaptureCycle(b *testing.B) {
	b.ResetTimer()
	for b.Loop() {
		sw := New("p", "s", 1)
		sw.MarkSpawn()
		_ = sw.Finish(time.Now(), StatusOK, 0, nil)
	}
}

// BenchmarkCaptureCycle_ErrorPath ensures error/timeout statuses do not
// pay a different cost than ok — the supervisor measures uniformly.
func BenchmarkCaptureCycle_ErrorPath(b *testing.B) {
	b.ResetTimer()
	for b.Loop() {
		sw := New("p", "s", 1)
		sw.MarkSpawn()
		_ = sw.Finish(time.Now(), StatusTimeout, 0, nil)
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
