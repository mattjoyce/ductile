package config

import "testing"

// TestMaxAttemptsForPluginResolvesPluginValue asserts the plugin-configured
// retry.max_attempts wins over defaults when set. P2-02.
func TestMaxAttemptsForPluginResolvesPluginValue(t *testing.T) {
	pc := PluginConf{Retry: &RetryConfig{MaxAttempts: 1}}
	if got := MaxAttemptsForPlugin(pc); got != 1 {
		t.Fatalf("MaxAttemptsForPlugin = %d, want 1 (from plugin config)", got)
	}
}

// TestMaxAttemptsForPluginFallsBackToDefaults asserts the resolver returns the
// global default when the plugin has no Retry config. P2-02.
func TestMaxAttemptsForPluginFallsBackToDefaults(t *testing.T) {
	pc := PluginConf{}
	want := DefaultPluginConf().Retry.MaxAttempts
	if got := MaxAttemptsForPlugin(pc); got != want {
		t.Fatalf("MaxAttemptsForPlugin = %d, want default %d", got, want)
	}
}

// TestMaxAttemptsForPluginZeroPluginValueFallsBackToDefaults asserts a zero
// plugin value is treated as unset and falls through to defaults. P2-02.
func TestMaxAttemptsForPluginZeroPluginValueFallsBackToDefaults(t *testing.T) {
	pc := PluginConf{Retry: &RetryConfig{MaxAttempts: 0}}
	want := DefaultPluginConf().Retry.MaxAttempts
	if got := MaxAttemptsForPlugin(pc); got != want {
		t.Fatalf("MaxAttemptsForPlugin = %d, want default %d", got, want)
	}
}
