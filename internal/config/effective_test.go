package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoadRawSkipsPluginDefaults: LoadRaw must leave a plugin's omitted blocks
// nil (so the effective view can tell explicit from inherited), whereas Load
// folds the default blocks in.
func TestLoadRawSkipsPluginDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := `
plugin_roots:
  - plugins
service:
  max_workers: 8
plugins:
  birda:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	raw, err := LoadRaw(dir)
	if err != nil {
		t.Fatalf("LoadRaw: %v", err)
	}
	if raw.Plugins["birda"].Retry != nil {
		t.Errorf("LoadRaw must leave omitted retry block nil, got %+v", raw.Plugins["birda"].Retry)
	}
	if raw.Plugins["birda"].Parallelism != 0 {
		t.Errorf("LoadRaw must leave omitted parallelism zero, got %d", raw.Plugins["birda"].Parallelism)
	}

	merged, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if merged.Plugins["birda"].Retry == nil {
		t.Error("Load must fold the default retry block in (non-nil)")
	}
}

// TestEffectivePluginConfAllDefaults: a plugin with no override blocks resolves
// every field to the DefaultPluginConf value, tagged default — except parallelism,
// whose default is service.max_workers (mergePluginDefaults semantics).
func TestEffectivePluginConfAllDefaults(t *testing.T) {
	def := DefaultPluginConf()
	raw := PluginConf{Enabled: true}

	eff, src := EffectivePluginConf(raw, 8)

	cases := []struct {
		key  string
		got  any
		want any
	}{
		{"retry.max_attempts", eff.Retry.MaxAttempts, def.Retry.MaxAttempts},
		{"retry.backoff_base", eff.Retry.BackoffBase, def.Retry.BackoffBase},
		{"timeouts.poll", eff.Timeouts.Poll, def.Timeouts.Poll},
		{"timeouts.handle", eff.Timeouts.Handle, def.Timeouts.Handle},
		{"timeouts.health", eff.Timeouts.Health, def.Timeouts.Health},
		{"timeouts.init", eff.Timeouts.Init, def.Timeouts.Init},
		{"circuit_breaker.threshold", eff.CircuitBreaker.Threshold, def.CircuitBreaker.Threshold},
		{"circuit_breaker.reset_after", eff.CircuitBreaker.ResetAfter, def.CircuitBreaker.ResetAfter},
		{"max_outstanding_polls", eff.MaxOutstandingPolls, def.MaxOutstandingPolls},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s value = %v, want %v", c.key, c.got, c.want)
		}
		if src[c.key] != SourceDefault {
			t.Errorf("%s source = %q, want default", c.key, src[c.key])
		}
	}
	if eff.Parallelism != 8 {
		t.Errorf("parallelism value = %d, want max_workers (8)", eff.Parallelism)
	}
	if src["parallelism"] != SourceDefault {
		t.Errorf("parallelism source = %q, want default", src["parallelism"])
	}
}

// TestEffectivePluginConfPartialBlock: the heart of #71 — a partial block
// (max_attempts set, backoff omitted) must tag only the set field explicit and
// still resolve the omitted sibling to its default, matching the per-field runtime
// resolver. Pre-#71 the rendered struct showed backoff as 0s here.
func TestEffectivePluginConfPartialBlock(t *testing.T) {
	def := DefaultPluginConf()
	raw := PluginConf{
		Enabled: true,
		Retry:   &RetryConfig{MaxAttempts: 6},
		Timeouts: &TimeoutsConfig{
			Handle:    300 * time.Second,
			Overrides: map[string]time.Duration{"cpu": 15 * time.Second},
		},
	}

	eff, src := EffectivePluginConf(raw, 4)

	if eff.Retry.MaxAttempts != 6 || src["retry.max_attempts"] != SourceExplicit {
		t.Errorf("max_attempts = %d/%s, want 6/explicit", eff.Retry.MaxAttempts, src["retry.max_attempts"])
	}
	if eff.Retry.BackoffBase != def.Retry.BackoffBase || src["retry.backoff_base"] != SourceDefault {
		t.Errorf("backoff_base = %v/%s, want %v/default", eff.Retry.BackoffBase, src["retry.backoff_base"], def.Retry.BackoffBase)
	}
	if eff.Timeouts.Handle != 300*time.Second || src["timeouts.handle"] != SourceExplicit {
		t.Errorf("handle = %v/%s, want 300s/explicit", eff.Timeouts.Handle, src["timeouts.handle"])
	}
	if eff.Timeouts.Poll != def.Timeouts.Poll || src["timeouts.poll"] != SourceDefault {
		t.Errorf("poll = %v/%s, want %v/default", eff.Timeouts.Poll, src["timeouts.poll"], def.Timeouts.Poll)
	}
	if eff.Timeouts.Overrides["cpu"] != 15*time.Second || src["timeouts.cpu"] != SourceExplicit {
		t.Errorf("override cpu = %v/%s, want 15s/explicit", eff.Timeouts.Overrides["cpu"], src["timeouts.cpu"])
	}
}

// TestEffectivePluginConfExplicitParallelism: an operator-set parallelism wins
// over the max_workers default and is tagged explicit.
func TestEffectivePluginConfExplicitParallelism(t *testing.T) {
	eff, src := EffectivePluginConf(PluginConf{Parallelism: 3}, 8)
	if eff.Parallelism != 3 || src["parallelism"] != SourceExplicit {
		t.Errorf("parallelism = %d/%s, want 3/explicit", eff.Parallelism, src["parallelism"])
	}
}

// TestEffectivePluginConfMatchesRuntimeResolver: the effective max_attempts must
// equal MaxAttemptsForPlugin (the canonical runtime resolver) for both the unset
// and set cases — the view must not drift from what actually runs.
func TestEffectivePluginConfMatchesRuntimeResolver(t *testing.T) {
	for _, raw := range []PluginConf{
		{Enabled: true},
		{Enabled: true, Retry: &RetryConfig{MaxAttempts: 9}},
	} {
		eff, _ := EffectivePluginConf(raw, 8)
		if eff.Retry.MaxAttempts != MaxAttemptsForPlugin(raw) {
			t.Errorf("effective max_attempts %d != MaxAttemptsForPlugin %d for %+v",
				eff.Retry.MaxAttempts, MaxAttemptsForPlugin(raw), raw.Retry)
		}
	}
}

// TestEffectivePluginConfUnsetEqualsDefaults pins the view's unset-case resolution
// to the single canonical default source (DefaultPluginConf) for every field, so a
// change to a default value can never silently disagree with what the view reports.
// The runtime resolvers (dispatcher getTimeout/computeRetryDelay, scheduler breaker)
// resolve unset values from this same DefaultPluginConf — card #75.
func TestEffectivePluginConfUnsetEqualsDefaults(t *testing.T) {
	const maxWorkers = 8
	def := DefaultPluginConf()
	eff, src := EffectivePluginConf(PluginConf{Enabled: true}, maxWorkers)

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"retry.max_attempts", eff.Retry.MaxAttempts, def.Retry.MaxAttempts},
		{"retry.backoff_base", eff.Retry.BackoffBase, def.Retry.BackoffBase},
		{"timeouts.poll", eff.Timeouts.Poll, def.Timeouts.Poll},
		{"timeouts.handle", eff.Timeouts.Handle, def.Timeouts.Handle},
		{"timeouts.health", eff.Timeouts.Health, def.Timeouts.Health},
		{"timeouts.init", eff.Timeouts.Init, def.Timeouts.Init},
		{"circuit_breaker.threshold", eff.CircuitBreaker.Threshold, def.CircuitBreaker.Threshold},
		{"circuit_breaker.reset_after", eff.CircuitBreaker.ResetAfter, def.CircuitBreaker.ResetAfter},
		{"max_outstanding_polls", eff.MaxOutstandingPolls, def.MaxOutstandingPolls},
		// parallelism's default is service.max_workers, not DefaultPluginConf().Parallelism.
		{"parallelism", eff.Parallelism, maxWorkers},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("unset %s = %v, want default %v", c.field, c.got, c.want)
		}
		if c.field != "parallelism" && src[c.field] != SourceDefault {
			t.Errorf("unset %s provenance = %q, want %q", c.field, src[c.field], SourceDefault)
		}
	}
}
