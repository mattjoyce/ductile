package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// cfgFromSources builds a Config carrying the given files as SourceFiles, so
// StrictDecodeWarnings can be exercised without the full loader.
func cfgFromSources(t *testing.T, files map[string]string) *Config {
	t.Helper()
	cfg := &Config{SourceFiles: make(map[string]*yaml.Node)}
	for name, body := range files {
		var node yaml.Node
		if err := yaml.Unmarshal([]byte(body), &node); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		cfg.SourceFiles[name] = &node
	}
	return cfg
}

func TestStrictDecodeWarningsCleanConfig(t *testing.T) {
	cfg := cfgFromSources(t, map[string]string{
		"config.yaml": "service:\n  tick_interval: 60s\n  log_level: info\nstate:\n  path: /tmp/x.db\n",
	})
	if w := StrictDecodeWarnings(cfg); len(w) != 0 {
		t.Fatalf("clean config produced warnings: %v", w)
	}
}

// TestStrictDecodeErrorClean proves the hard-gate companion (#26 flip) passes a
// config with no dropped keys.
func TestStrictDecodeErrorClean(t *testing.T) {
	cfg := cfgFromSources(t, map[string]string{
		"config.yaml": "service:\n  tick_interval: 60s\n  log_level: info\nstate:\n  path: /tmp/x.db\n",
	})
	if err := StrictDecodeError(cfg); err != nil {
		t.Fatalf("clean config produced a gate error: %v", err)
	}
}

// TestStrictDecodeErrorNamesDroppedKey proves a silently-dropped key becomes a
// hard error (warn-then-fail per validate_config_on_boot) that names the key.
func TestStrictDecodeErrorNamesDroppedKey(t *testing.T) {
	cfg := cfgFromSources(t, map[string]string{
		"config.yaml": "service:\n  tick_interval: 60s\n  log_levle: info\nstate:\n  path: /tmp/x.db\n",
	})
	err := StrictDecodeError(cfg)
	if err == nil {
		t.Fatal("dropped key did not produce a gate error")
	}
	if !strings.Contains(err.Error(), "log_levle") {
		t.Errorf("gate error should name the dropped key; got: %v", err)
	}
}

func TestStrictDecodeWarningsUnknownTopLevelKey(t *testing.T) {
	cfg := cfgFromSources(t, map[string]string{
		"config.yaml": "service:\n  tick_interval: 60s\nbogus_section:\n  typo: true\n",
	})
	w := StrictDecodeWarnings(cfg)
	if len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), "bogus_section") {
		t.Fatalf("expected a warning naming bogus_section, got %v", w)
	}
}

func TestStrictDecodeWarningsUnknownNestedKey(t *testing.T) {
	// The #17/#26 case: a typo'd key nested under a section (here service) is the
	// most insidious — it looks valid. The strict pass must catch it.
	cfg := cfgFromSources(t, map[string]string{
		"config.yaml": "service:\n  tick_intervl: 60s\n",
	})
	w := StrictDecodeWarnings(cfg)
	if len(w) == 0 || !strings.Contains(strings.Join(w, "\n"), "tick_intervl") {
		t.Fatalf("expected a warning for the nested typo tick_intervl, got %v", w)
	}
}

func TestStrictDecodeWarningsSkipsPipelinesScope(t *testing.T) {
	// pipelines.yaml carries `pipelines:` (Config has it as yaml:"-", loaded by a
	// dedicated path), so it must NOT be reported as an unknown Config key.
	cfg := cfgFromSources(t, map[string]string{
		"pipelines.yaml": "pipelines:\n  - name: p\n    event: thing\n",
	})
	if w := StrictDecodeWarnings(cfg); len(w) != 0 {
		t.Fatalf("pipelines scope file should be skipped, got warnings: %v", w)
	}
}
