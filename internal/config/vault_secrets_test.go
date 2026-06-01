package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// writeTestKey generates an age identity, writes it 0600, and returns a keyring.
func writeTestKey(t *testing.T, dir string) *secrets.Keyring {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	keyPath := filepath.Join(dir, "age.key")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	return kr
}

func tokenKeyMap(entries []TokenEntry) map[string]string {
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		m[e.Name] = e.Key
	}
	return m
}

// TestMergeVaultSecretsOverridesAndAppends pins the coexistence-window contract:
// the vault is the source of truth, so a name present in both the vault and the
// legacy tokens.yaml table resolves to the *vault* value (and warns), while a
// vault-only secret is appended and a tokens.yaml-only entry is left untouched.
func TestMergeVaultSecretsOverridesAndAppends(t *testing.T) {
	tokens := []TokenEntry{
		{Name: "gh_webhook", Key: "old-from-yaml"}, // collision -> vault wins
		{Name: "relay_a", Key: "ra"},               // yaml-only -> untouched
	}
	vaultSecrets := map[string]string{
		"gh_webhook": "new-from-vault", // overrides the yaml entry
		"withings":   "wv",             // vault-only -> appended
	}

	merged, warnings := mergeVaultSecrets(tokens, vaultSecrets)
	got := tokenKeyMap(merged)

	if got["gh_webhook"] != "new-from-vault" {
		t.Errorf("collision should resolve to the vault value, got %q", got["gh_webhook"])
	}
	if got["withings"] != "wv" {
		t.Errorf("vault-only secret should be grafted in, got %q", got["withings"])
	}
	if got["relay_a"] != "ra" {
		t.Errorf("tokens.yaml-only entry should be untouched, got %q", got["relay_a"])
	}
	if len(merged) != 3 {
		t.Errorf("expected 3 entries (no duplicate for the collision), got %d", len(merged))
	}

	if len(warnings) != 1 {
		t.Fatalf("expected exactly one collision warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "gh_webhook") {
		t.Errorf("warning should name the shadowed entry, got %q", warnings[0])
	}
}

// TestMergeVaultSecretsEmptyIsNoOp — no vault secrets means the token table and
// the (nil) warnings are returned unchanged.
func TestMergeVaultSecretsEmptyIsNoOp(t *testing.T) {
	tokens := []TokenEntry{{Name: "relay_a", Key: "ra"}}

	merged, warnings := mergeVaultSecrets(tokens, nil)

	if len(merged) != 1 || merged[0].Key != "ra" {
		t.Errorf("token table should be unchanged, got %+v", merged)
	}
	if warnings != nil {
		t.Errorf("no collisions means no warnings, got %v", warnings)
	}
}

// TestGraftVaultSecretsRoundTrip proves the wrapper grafts through a real
// encrypted vault blob: genesis + a granted secret on disk, then graft resolves
// it into the legacy table while leaving an existing entry alone.
func TestGraftVaultSecretsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	vaultPath := filepath.Join(dir, "vault.age")
	v, _, err := vault.Init(vaultPath, kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	if err := v.Store().RegisterPrincipal("withings", vault.KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := v.Store().SetSecret("withings_api", "VAULT-VAL", []string{"withings"}, vault.PatternManual, time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := &Config{Tokens: []TokenEntry{{Name: "relay_a", Key: "ra"}}}
	warnings, err := graftVaultSecrets(cfg, dir, kr) // default path <dir>/vault.age
	if err != nil {
		t.Fatalf("graft: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("no collision expected, got %v", warnings)
	}
	got := tokenKeyMap(cfg.Tokens)
	if got["withings_api"] != "VAULT-VAL" {
		t.Errorf("vault secret not grafted, got %q", got["withings_api"])
	}
	if got["relay_a"] != "ra" {
		t.Errorf("existing entry clobbered, got %q", got["relay_a"])
	}
}

// TestGraftVaultSecretsNoVaultIsNoOp — early in the migration window there is no
// vault file; graft must leave the token table untouched.
func TestGraftVaultSecretsNoVaultIsNoOp(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	cfg := &Config{Tokens: []TokenEntry{{Name: "relay_a", Key: "ra"}}}
	warnings, err := graftVaultSecrets(cfg, dir, kr) // no vault.age present
	if err != nil {
		t.Fatalf("missing vault should no-op, got: %v", err)
	}
	if len(warnings) != 0 || len(cfg.Tokens) != 1 {
		t.Errorf("expected untouched table, got %+v warnings=%v", cfg.Tokens, warnings)
	}
}

// TestGraftVaultSecretsKeylessIsNoOp — a keyless caller (static config validate /
// CLI tools) cannot decrypt the vault; it resolves against tokens.yaml only.
func TestGraftVaultSecretsKeylessIsNoOp(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	vaultPath := filepath.Join(dir, "vault.age")
	v, _, err := vault.Init(vaultPath, kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := &Config{Tokens: []TokenEntry{{Name: "relay_a", Key: "ra"}}}
	warnings, err := graftVaultSecrets(cfg, dir, &secrets.Keyring{}) // empty keyring
	if err != nil {
		t.Fatalf("keyless should no-op, got: %v", err)
	}
	if len(warnings) != 0 || len(cfg.Tokens) != 1 {
		t.Errorf("keyless caller should not graft, got %+v", cfg.Tokens)
	}
}

// TestActiveVaultSecretsExcludesRevoked — a revoked secret must never resolve.
func TestActiveVaultSecretsExcludesRevoked(t *testing.T) {
	s := vault.NewStore()
	if err := s.SetSecret("live", "L", nil, vault.PatternManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret("dead", "D", nil, vault.PatternManual, time.Now()); err != nil {
		t.Fatal(err)
	}
	sec, ok := s.Secret("dead")
	if !ok {
		t.Fatal("dead secret missing")
	}
	sec.Status = vault.StatusRevoked

	got := activeVaultSecrets(s)
	if got["live"] != "L" {
		t.Errorf("active secret missing, got %q", got["live"])
	}
	if _, present := got["dead"]; present {
		t.Error("revoked secret must be excluded from resolution")
	}
}

// TestVaultBlind — blind only when a vault exists AND we have no key.
func TestVaultBlind(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{}

	if vaultBlind(dir, cfg, &secrets.Keyring{}) {
		t.Error("no vault file → not blind")
	}

	if err := os.WriteFile(filepath.Join(dir, "vault.age"), []byte("dummy"), 0o600); err != nil {
		t.Fatalf("write vault stub: %v", err)
	}
	if !vaultBlind(dir, cfg, &secrets.Keyring{}) {
		t.Error("vault present + keyless → blind")
	}

	kr := writeTestKey(t, dir)
	if vaultBlind(dir, cfg, kr) {
		t.Error("vault present + keyed → not blind")
	}
}

// TestCheckSecretRefSoftensWhenBlind — a missing secret_ref errors normally but
// only warns (returns nil) when the validator is vault-blind.
func TestCheckSecretRefSoftensWhenBlind(t *testing.T) {
	if err := (&ConfigValidator{tokens: map[string]string{}}).checkSecretRef("missing", "webhook[0]"); err == nil {
		t.Error("non-blind: a missing secret_ref must error")
	}
	if err := (&ConfigValidator{tokens: map[string]string{}, vaultBlind: true}).checkSecretRef("missing", "webhook[0]"); err != nil {
		t.Errorf("vault-blind: a missing secret_ref must not error, got %v", err)
	}
	if err := (&ConfigValidator{tokens: map[string]string{"x": "y"}}).checkSecretRef("x", "webhook[0]"); err != nil {
		t.Errorf("a present secret_ref must pass, got %v", err)
	}
}
