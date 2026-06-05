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

// seedVault writes a keyed vault (age.key + vault.age) into dir holding the given
// secrets, so a Load() in dir resolves their secret_refs now that the vault is the
// sole secret source (epic #48). Pair it with a config secrets: block:
//
//	secrets:
//	  age_key_file: age.key
//	  vault_file: vault.age
func seedVault(t *testing.T, dir string, kv map[string]string) {
	t.Helper()
	kr := writeTestKey(t, dir)
	v, _, err := vault.Init(filepath.Join(dir, "vault.age"), kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	for name, val := range kv {
		if err := v.Store().SetSecret(name, val, nil, vault.PatternManual, time.Now()); err != nil {
			t.Fatalf("seed secret %q: %v", name, err)
		}
	}
	if err := v.Save(); err != nil {
		t.Fatalf("vault save: %v", err)
	}
}

// TestProjectVaultSecretsRoundTrip proves projectVaultSecrets resolves a granted
// secret from a real encrypted vault blob into cfg.ResolvedSecrets. The vault is
// the sole source (epic #48) — there is no tokens.yaml table to merge with.
func TestProjectVaultSecretsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	vaultPath := filepath.Join(dir, "vault.age")
	v, _, err := vault.Init(vaultPath, kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	// Grant to a consumer principal: the projection serves gateway/consumer
	// (webhook/relay) consumers. Plugin-scoped secrets are delivered at spawn and
	// are excluded — see TestActiveVaultSecretsExcludesPluginScoped.
	if err := v.Store().RegisterPrincipal("relaysvc", vault.KindConsumer); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := v.Store().SetSecret("relay_hmac", "VAULT-VAL", []string{"relaysvc"}, vault.PatternManual, time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg := &Config{}
	_, warnings, err := projectVaultSecrets(cfg, dir, kr) // default path <dir>/vault.age
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("no warnings expected, got %v", warnings)
	}
	if cfg.ResolvedSecrets["relay_hmac"] != "VAULT-VAL" {
		t.Errorf("vault secret not projected, got %q", cfg.ResolvedSecrets["relay_hmac"])
	}
}

// TestProjectVaultSecretsNoVaultIsNoOp — no vault file yet: nil owner, no secrets.
func TestProjectVaultSecretsNoVaultIsNoOp(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	cfg := &Config{}
	owner, warnings, err := projectVaultSecrets(cfg, dir, kr) // no vault.age present
	if err != nil {
		t.Fatalf("missing vault should no-op, got: %v", err)
	}
	if owner != nil || len(warnings) != 0 || len(cfg.ResolvedSecrets) != 0 {
		t.Errorf("expected nil owner + empty secrets, got owner=%v secrets=%+v warnings=%v", owner, cfg.ResolvedSecrets, warnings)
	}
}

// TestProjectVaultSecretsKeylessIsNoOp — a keyless caller (static config validate /
// CLI tools) cannot decrypt the vault, so it projects no secrets.
func TestProjectVaultSecretsKeylessIsNoOp(t *testing.T) {
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

	cfg := &Config{}
	owner, warnings, err := projectVaultSecrets(cfg, dir, &secrets.Keyring{}) // empty keyring
	if err != nil {
		t.Fatalf("keyless should no-op, got: %v", err)
	}
	if owner != nil || len(warnings) != 0 || len(cfg.ResolvedSecrets) != 0 {
		t.Errorf("keyless caller should project nothing, got owner=%v secrets=%+v", owner, cfg.ResolvedSecrets)
	}
}

// TestProjectVaultSecretsReturnsOwner — slice 2 (epic #48): projection returns the
// vault owner it decrypted so the daemon reuses that single decryption as its live
// owner instead of decrypting the blob a second time at runtime construction
// (#43 redundant decrypt).
func TestProjectVaultSecretsReturnsOwner(t *testing.T) {
	dir := t.TempDir()
	kr := writeTestKey(t, dir)

	vaultPath := filepath.Join(dir, "vault.age")
	v, _, err := vault.Init(vaultPath, kr, time.Now())
	if err != nil {
		t.Fatalf("vault init: %v", err)
	}
	if err := v.Store().SetSecret("relay_hmac", "VAULT-VAL", nil, vault.PatternManual, time.Now()); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	owner, _, err := projectVaultSecrets(&Config{}, dir, kr)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if owner == nil {
		t.Fatal("expected a non-nil owner to reuse as the live vault; got nil")
	}
	store, err := owner.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if sec, ok := store.Secret("relay_hmac"); !ok || sec.Value != "VAULT-VAL" {
		t.Errorf("returned owner does not serve the secret; ok=%v sec=%+v", ok, sec)
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
// loader returns a nil owner rather than failing, mirroring the graft.
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

	owner, err := loadVaultOwner(dir, &Config{}, &secrets.Keyring{})
	if err != nil {
		t.Fatalf("keyless should be (nil,nil): %v", err)
	}
	if owner != nil {
		t.Errorf("keyless caller must get nil owner, got %v", owner)
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
