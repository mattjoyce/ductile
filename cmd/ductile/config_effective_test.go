package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEffectiveFixture writes a config dir with one plugin "birda" that sets an
// explicit timeouts.handle but leaves retry untouched, so the effective view has
// both an explicit and a default field to tag. A manifest + entrypoint are written
// because LoadRaw validates the configured plugin set.
func writeEffectiveFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfg := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  max_workers: 8
plugins:
  birda:
    enabled: true
    timeouts:
      handle: 300s
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	pdir := filepath.Join(dir, "plugins", "birda")
	if err := os.MkdirAll(pdir, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: birda
version: 0.1.0
protocol: 2
entrypoint: birda
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(pdir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pdir, "birda"), []byte("#!/bin/sh\necho birda\n"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	return dir
}

// TestConfigShowEffectiveTagsExplicitAndDefault is the #71 acceptance: the
// explicit handle is tagged explicit, and the omitted max_attempts resolves to
// its default (4) tagged default — not the misleading 0 the un-resolved struct
// would show for a partial block.
func TestConfigShowEffectiveTagsExplicitAndDefault(t *testing.T) {
	dir := writeEffectiveFixture(t)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigShow([]string{"--config-dir", dir, "--effective", "birda"})
	})
	if code != 0 {
		t.Fatalf("config show --effective failed (code=%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "timeouts.handle: 5m0s (explicit)") {
		t.Errorf("expected explicit handle line, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "retry.max_attempts: 4 (default)") {
		t.Errorf("expected default max_attempts line, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "retry.backoff_base: 30s (default)") {
		t.Errorf("partial retry block must still default backoff_base, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "parallelism: 8 (default)") {
		t.Errorf("parallelism default should be max_workers (8), got:\n%s", stdout)
	}
}

// TestConfigShowEffectiveJSON checks the --json shape: each field is
// {value, source}.
func TestConfigShowEffectiveJSON(t *testing.T) {
	dir := writeEffectiveFixture(t)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigShow([]string{"--config-dir", dir, "--effective", "birda", "--json"})
	})
	if code != 0 {
		t.Fatalf("config show --effective --json failed (code=%d): %s", code, stderr)
	}
	var out map[string]map[string]struct {
		Value  any    `json:"value"`
		Source string `json:"source"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	birda, ok := out["birda"]
	if !ok {
		t.Fatalf("expected birda in JSON output: %s", stdout)
	}
	if birda["timeouts.handle"].Source != "explicit" {
		t.Errorf("timeouts.handle source = %q, want explicit", birda["timeouts.handle"].Source)
	}
	if birda["retry.max_attempts"].Source != "default" {
		t.Errorf("retry.max_attempts source = %q, want default", birda["retry.max_attempts"].Source)
	}
}

// TestConfigShowDefaultUnaffectedByEffective is the anti-criterion: plain
// `config show` (no --effective) must not emit the explicit/default tags.
func TestConfigShowDefaultUnaffectedByEffective(t *testing.T) {
	dir := writeEffectiveFixture(t)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigShow([]string{"--config-dir", dir, "plugins"})
	})
	if code != 0 {
		t.Fatalf("config show plugins failed (code=%d): %s", code, stderr)
	}
	if strings.Contains(stdout, "(explicit)") || strings.Contains(stdout, "(default)") {
		t.Errorf("plain config show must not carry effective tags, got:\n%s", stdout)
	}
}

// TestConfigGetEffectiveSingleField resolves one field with its source tag.
func TestConfigGetEffectiveSingleField(t *testing.T) {
	dir := writeEffectiveFixture(t)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigGet([]string{"--config-dir", dir, "--effective", "birda.timeouts.handle"})
	})
	if code != 0 {
		t.Fatalf("config get --effective failed (code=%d): %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "5m0s (explicit)" {
		t.Errorf("get --effective birda.timeouts.handle = %q, want \"5m0s (explicit)\"", strings.TrimSpace(stdout))
	}
}
