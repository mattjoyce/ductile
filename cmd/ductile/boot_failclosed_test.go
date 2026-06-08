package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// Boot fail-closed characterization (#119). PR #94 made API bearer tokens
// vault-only: a literal token is a secret living OUTSIDE the vault, so the
// runtime must refuse to start rather than open an authenticated surface backed
// by a plaintext credential. `ductile system start` reaches this via
// config.LoadWithVault (runtime.go), which is the boot seam an operating agent
// actually triggers — and the refusal must be LEGIBLE (name the offending field
// and point at the fix) so an unsupervised agent can read why it won't boot.
//
// The unit invariant lives in internal/config (TestResolveAPITokens_LiteralIsRejected);
// these tests pin the boot seam and its legibility, the property that silently
// killed the docker fixture tier when it went unverified.

// writeBootConfig writes a minimal config dir (config.yaml including api.yaml)
// and returns the config.yaml path. apiYAML supplies the api: block under test.
func writeBootConfig(t *testing.T, apiYAML string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("config.yaml",
		"log_level: error\n"+
			"service:\n  name: boot-failclosed\n"+
			"state:\n  path: ./state/ductile.db\n"+
			"include:\n  - api.yaml\n")
	write("api.yaml", apiYAML)
	return filepath.Join(dir, "config.yaml")
}

// TestBootRejectsLiteralAPIToken — the core #94 boot invariant: a config with a
// literal API bearer token must fail to load (boot fails closed), and the error
// must name the offending field and point at secret_ref.
func TestBootRejectsLiteralAPIToken(t *testing.T) {
	configPath := writeBootConfig(t, ""+
		"api:\n"+
		"  enabled: true\n"+
		"  listen: \"127.0.0.1:0\"\n"+
		"  auth:\n"+
		"    tokens:\n"+
		"      - token: \"literal-not-allowed\"\n"+
		"        scopes: [\"*\"]\n")

	_, _, err := config.LoadWithVault(configPath)
	if err == nil {
		t.Fatal("boot must fail closed: a literal API token opens an authenticated surface backed by a plaintext credential")
	}
	// Legibility: an unsupervised agent must be able to read WHICH field is wrong
	// and HOW to fix it, not just "validation failed".
	msg := err.Error()
	if !strings.Contains(msg, "api.auth.tokens") {
		t.Errorf("refusal must name the offending field (api.auth.tokens), got: %v", err)
	}
	if !strings.Contains(msg, "secret_ref") {
		t.Errorf("refusal must point at the fix (secret_ref), got: %v", err)
	}
}

// TestBootAcceptsSecretRefAPIToken — the discriminating control: the rule rejects
// LITERAL tokens, not all tokens. A secret_ref-backed token must NOT trip the
// literal-token refusal at load (an unresolvable ref is a warning here, resolved
// against the vault at runtime — never the hard literal-token error).
func TestBootAcceptsSecretRefAPIToken(t *testing.T) {
	configPath := writeBootConfig(t, ""+
		"api:\n"+
		"  enabled: true\n"+
		"  listen: \"127.0.0.1:0\"\n"+
		"  auth:\n"+
		"    tokens:\n"+
		"      - secret_ref: ductile-api-admin\n"+
		"        scopes: [\"*\"]\n")

	_, _, err := config.LoadWithVault(configPath)
	if err != nil && strings.Contains(err.Error(), "literal token") {
		t.Fatalf("secret_ref token must not trip the literal-token refusal, got: %v", err)
	}
}
