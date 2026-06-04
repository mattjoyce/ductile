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
	// Grant to a consumer principal: the load-time graft serves gateway/consumer
	// (webhook/relay) consumers. Plugin-scoped secrets are delivered at spawn and
	// are excluded from the graft — see TestActiveVaultSecretsExcludesPluginScoped.
	if err := v.Store().RegisterPrincipal("relaysvc", vault.KindConsumer); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := v.Store().SetSecret("relay_hmac", "VAULT-VAL", []string{"relaysvc"}, vault.PatternManual, time.Now()); err != nil {
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
	if got["relay_hmac"] != "VAULT-VAL" {
		t.Errorf("vault secret not grafted, got %q", got["relay_hmac"])
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

	got, _ := activeVaultSecrets(s)
	if got["live"] != "L" {
		t.Errorf("active secret missing, got %q", got["live"])
	}
	if _, present := got["dead"]; present {
		t.Error("revoked secret must be excluded from resolution")
	}
}

// TestLoadVaultPresentAndKeyed — the runtime entry returns the vault owner when
// a keyed vault is present on disk.
func TestLoadVaultPresentAndKeyed(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	vaultPath := filepath.Join(dir, "vault.age")
	v, _, err := vault.Init(vaultPath, kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	if err := v.Store().SetSecret("api", "V", nil, vault.PatternManual, time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := &Config{}
	cfg.Secrets.AgeKeyFile = "age.key" // deterministic: <dir>/age.key

	owner, err := LoadVault(dir, cfg)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if owner == nil {
		t.Fatal("expected a Vault owner, got nil")
	}
	if sec, ok := owner.Store().Secret("api"); !ok || sec.Value != "V" {
		t.Errorf("expected secret api=V, got %+v ok=%v", sec, ok)
	}
}

// TestLoadVaultNoVaultIsNil — no vault file yet (migration window) yields a nil
// owner and no error, so the runtime simply delivers no secrets.
func TestLoadVaultNoVaultIsNil(t *testing.T) {
	dir := t.TempDir()
	writeTestKey(t, dir)

	cfg := &Config{}
	cfg.Secrets.AgeKeyFile = "age.key"

	owner, err := LoadVault(dir, cfg) // no vault.age present
	if err != nil {
		t.Fatalf("missing vault should be (nil,nil): %v", err)
	}
	if owner != nil {
		t.Errorf("expected nil owner, got %v", owner)
	}
}

// TestVaultStoreKeylessIsNil — a keyless caller cannot decrypt; the shared
// loader returns a nil Store rather than failing, mirroring the graft.
func TestVaultStoreKeylessIsNil(t *testing.T) {
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

	store, err := vaultStore(dir, &Config{}, &secrets.Keyring{})
	if err != nil {
		t.Fatalf("keyless should be (nil,nil): %v", err)
	}
	if store != nil {
		t.Errorf("keyless caller must get nil Store, got %v", store)
	}
}

// TestActiveVaultSecretsExcludesPluginScoped — the load-time graft serves
// gateway/consumer (load-time) consumers; a secret delivered exclusively to
// plugin principals reaches its consumer at spawn (Compose, #14), so it must NOT
// be grafted gateway-global. Unscoped (migrated) and gateway/consumer-authorized
// secrets stay; an unconfirmable (orphan) grant fails toward visibility.
func TestActiveVaultSecretsExcludesPluginScoped(t *testing.T) {
	s := vault.NewStore()
	for name, kind := range map[string]string{
		"gwsvc": vault.KindGateway, "plug": vault.KindPlugin, "plug2": vault.KindPlugin,
	} {
		if err := s.RegisterPrincipal(name, kind); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	now := time.Now()
	set := func(name, val string, ps []string) {
		if err := s.SetSecret(name, val, ps, vault.PatternManual, now); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}
	set("gw_hmac", "G", []string{"gwsvc"})             // gateway-scoped -> graft
	set("plug_only", "P", []string{"plug"})            // plugin-only -> NOT grafted
	set("plug_multi", "PM", []string{"plug", "plug2"}) // all plugins -> NOT grafted
	set("shared", "S", []string{"gwsvc", "plug"})      // mixed -> graft (gateway needs it)
	set("unscoped", "U", nil)                          // migrated tokens.yaml value -> graft

	got, _ := activeVaultSecrets(s)
	for _, name := range []string{"gw_hmac", "shared", "unscoped"} {
		if _, ok := got[name]; !ok {
			t.Errorf("%q must remain in the gateway graft", name)
		}
	}
	for _, name := range []string{"plug_only", "plug_multi"} {
		if _, ok := got[name]; ok {
			t.Errorf("%q is plugin-scoped and must NOT be grafted gateway-global", name)
		}
	}

	// Orphan grant (principal no longer registered): we cannot confirm the secret
	// is plugin-only, so it stays gateway-visible — never silently hidden. But the
	// dangling grantee is almost always a typo, so the warn-only blast-radius guard
	// (#41) must surface it loudly while keeping the secret visible.
	s.Secrets["gw_hmac"].AuthorizedPrincipals = []string{"ghost"}
	got, warnings := activeVaultSecrets(s)
	if _, ok := got["gw_hmac"]; !ok {
		t.Error("a grant to an unregistered principal must not hide the secret from the graft")
	}
	var warned bool
	for _, w := range warnings {
		if strings.Contains(w, "gw_hmac") && strings.Contains(w, "ghost") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("expected a warning naming the secret and the unregistered principal, got %v", warnings)
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
