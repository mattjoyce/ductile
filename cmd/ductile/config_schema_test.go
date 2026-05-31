package main

import (
	"os"
	"path/filepath"
	"testing"
)

const validConfigYAML = `
service:
  name: ductile
  tick_interval: 60s
state:
  path: /tmp/ductile.db
plugin_roots:
  - ./plugins
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestRunConfigSchemaListAndDump(t *testing.T) {
	if code := runConfigSchema(nil); code != 0 {
		t.Fatalf("schema list exit = %d, want 0", code)
	}
	if code := runConfigSchema([]string{"--name", "config"}); code != 0 {
		t.Fatalf("schema dump exit = %d, want 0", code)
	}
	if code := runConfigSchema([]string{"--name", "nonexistent"}); code != 1 {
		t.Fatalf("schema dump of unknown exit = %d, want 1", code)
	}
}

func TestRunConfigValidateGood(t *testing.T) {
	if code := runConfigValidate([]string{"--file", writeTemp(t, validConfigYAML)}); code != 0 {
		t.Fatalf("validate good config exit = %d, want 0", code)
	}
}

func TestRunConfigValidateBad(t *testing.T) {
	bad := validConfigYAML + "bogus_key: true\n"
	if code := runConfigValidate([]string{"--file", writeTemp(t, bad)}); code != 1 {
		t.Fatalf("validate bad config exit = %d, want 1", code)
	}
}

func TestRunConfigValidateMissingFile(t *testing.T) {
	if code := runConfigValidate(nil); code != 1 {
		t.Fatalf("validate without --file exit = %d, want 1", code)
	}
}

// TestRunConfigValidateNeedsNoKey proves validation is a static, unprivileged
// path: with no age key configured (env cleared) it still validates, because it
// never Load()s or decrypts.
func TestRunConfigValidateNeedsNoKey(t *testing.T) {
	t.Setenv("DUCTILE_AGE_KEY_FILE", "")
	if code := runConfigValidate([]string{"--file", writeTemp(t, validConfigYAML)}); code != 0 {
		t.Fatalf("validate exit = %d with no key; should not require one", code)
	}
}
