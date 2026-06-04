package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// graftVaultSecrets overlays the vault's active secrets onto the legacy
// secret-resolution table (cfg.Tokens) at config load. This is the Rung-2
// coexistence bridge: every existing consumer keeps resolving secret_ref against
// cfg.Tokens, while the vault becomes the source of truth without touching a
// single resolver.
//
// NB: cfg.Tokens is the *legacy, misnamed* table — it holds secrets, not auth
// tokens (see the ADR glossary). This function speaks "secret"; the table keeps
// its deprecated name until tokens.yaml is removed.
//
// Resolution timing here is load-time, which is correct for the gateway's own
// consumers (webhook/relay HMAC secrets). Plugin secrets resolve at *spawn* via
// Compose with live fingerprint re-verification — that is #14's path, not this
// one, so plugin-scoped delivery must not be folded into this graft.
//
// FRESHNESS ASYMMETRY (#27, Ousterhout §2.2): because this graft runs at
// load/reload, a rolled webhook/relay secret_ref only takes effect on the running
// servers after the daemon RELOADS — the graft is frozen at boot. Plugin secrets
// differ: they re-resolve at the next spawn, so a roll is visible on the next job.
// So after rolling a webhook/relay secret, reload to make it live. (See
// OPERATOR_GUIDE.md "Rolling a webhook/relay secret".)
//
// Degradation is deliberate and fail-open *only for visibility, not for secrecy*:
//   - no vault file yet  -> no-op (early in the migration window)
//   - keyless caller     -> no-op (static `config validate` / CLI tools cannot
//     decrypt; vault-only secrets are the daemon's to resolve, per ADR §3.5.1)
//   - present but broken  -> error (fail-closed: a corrupt/owned vault must not
//     be silently skipped)
func graftVaultSecrets(cfg *Config, configDir string, kr *secrets.Keyring) ([]string, error) {
	store, err := vaultStore(configDir, cfg, kr)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, nil // no vault / keyless: resolve against tokens.yaml only
	}

	merged, warnings := mergeVaultSecrets(cfg.Tokens, activeVaultSecrets(store))
	cfg.Tokens = merged
	return warnings, nil
}

// LoadVault resolves the keyring and loads the vault *owner* for the active
// config, or nil when there is no vault to load. It is the runtime's entry point
// for the in-memory vault model: the daemon holds this single owner and routes
// both the spawn-time read path (Compose) and management writes (SetSecret)
// through it.
//
// Degradation mirrors graftVaultSecrets: no vault file or a keyless caller
// yields (nil, nil); a present-but-broken vault is a hard error (fail-closed).
func LoadVault(configDir string, cfg *Config) (*vault.Vault, error) {
	kr, err := resolveKeyring(configDir, cfg)
	if err != nil {
		return nil, err
	}
	return loadVaultOwner(configDir, cfg, kr)
}

// loadVaultOwner loads the vault owner given an already-resolved keyring. It is
// the single home for the vault's load-time degradation rules (see
// graftVaultSecrets for the rationale), shared by the graft and LoadVault.
func loadVaultOwner(configDir string, cfg *Config, kr *secrets.Keyring) (*vault.Vault, error) {
	path := resolveVaultPath(configDir, cfg)
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no vault yet — coexistence window
		}
		return nil, fmt.Errorf("vault: stat %q: %w", path, err)
	}
	if kr == nil || kr.Empty() {
		return nil, nil // keyless: cannot decrypt
	}

	v, err := vault.Load(path, kr)
	if err != nil {
		return nil, err // present + keyed but broken: fail-closed
	}
	return v, nil
}

// vaultStore loads the decrypted Store given an already-resolved keyring, for
// the load-time graft (which needs the pure model, not the guarded owner). It
// returns a Snapshot — an independent deep copy — rather than the live model, so
// the graft never aliases the owner's Store past the read lock.
func vaultStore(configDir string, cfg *Config, kr *secrets.Keyring) (*vault.Store, error) {
	v, err := loadVaultOwner(configDir, cfg, kr)
	if err != nil || v == nil {
		return nil, err
	}
	return v.Snapshot()
}

// mergeVaultSecrets overlays vault secret values onto the legacy token table.
// The vault is the source of truth: on a name present in both, the vault value
// wins and a warning names the shadowed tokens.yaml entry (the migration
// invariant is "if it is a secret, it is in the vault" — the tokens.yaml dupe
// should be removed). Pure: no I/O, deterministic ordering.
func mergeVaultSecrets(tokens []TokenEntry, vaultSecrets map[string]string) ([]TokenEntry, []string) {
	if len(vaultSecrets) == 0 {
		return tokens, nil
	}

	idx := make(map[string]int, len(tokens))
	for i, t := range tokens {
		idx[t.Name] = i
	}
	merged := append([]TokenEntry(nil), tokens...)

	names := make([]string, 0, len(vaultSecrets))
	for n := range vaultSecrets {
		names = append(names, n)
	}
	sort.Strings(names)

	var warnings []string
	for _, name := range names {
		value := vaultSecrets[name]
		if i, ok := idx[name]; ok {
			warnings = append(warnings, fmt.Sprintf(
				"secret %q is in both the vault and tokens.yaml; using the vault value — remove the tokens.yaml entry", name))
			merged[i].Key = value
			continue
		}
		merged = append(merged, TokenEntry{Name: name, Key: value})
	}
	return merged, warnings
}

// activeVaultSecrets projects the store's active, gateway-visible secrets into a
// name->value map for the load-time graft. Two exclusions:
//   - revoked secrets — a revoked secret must not resolve.
//   - exclusively plugin-scoped secrets — these reach their consumer at *spawn*
//     via Compose (#14), so grafting them into cfg.Tokens would leak them to
//     every gateway/load-time consumer. The graft serves only the gateway's own
//     consumers (webhook/relay HMAC); plugin delivery is the dispatcher's job.
func activeVaultSecrets(s *vault.Store) map[string]string {
	out := make(map[string]string)
	for _, name := range s.SecretNames() {
		sec, ok := s.Secret(name)
		if !ok || sec.Status != vault.StatusActive {
			continue
		}
		if pluginScopedSecret(s, sec) {
			continue
		}
		out[name] = sec.Value
	}
	return out
}

// pluginScopedSecret reports whether a secret is authorized *exclusively* to
// plugin principals — the case where delivery happens at spawn, not load time.
// A secret with no principals (e.g. a migrated tokens.yaml value) is NOT
// plugin-scoped: gateway consumers resolve it via secret_ref. A grant to an
// unregistered or non-plugin principal also keeps it gateway-visible — we never
// hide a secret on a grant we cannot confirm is plugin-only (fail toward the
// load-time consumer).
func pluginScopedSecret(s *vault.Store, sec *vault.Secret) bool {
	if len(sec.AuthorizedPrincipals) == 0 {
		return false
	}
	for _, name := range sec.AuthorizedPrincipals {
		p, ok := s.Principal(name)
		if !ok || p.Kind != vault.KindPlugin {
			return false
		}
	}
	return true
}

// vaultBlind reports whether a vault exists but this run cannot read it (no
// key). In that state vault-only secrets are invisible, so a secret_ref the
// validator cannot resolve is a warning, not an error — the daemon (which holds
// the key) is the authoritative validator. No vault present => not blind.
func vaultBlind(configDir string, cfg *Config, kr *secrets.Keyring) bool {
	path := resolveVaultPath(configDir, cfg)
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false // no vault → tokens.yaml is authoritative
	}
	return kr == nil || kr.Empty()
}

// resolveVaultPath resolves the vault blob location: the configured path
// (interpolated, relative to configDir) or the default <configDir>/vault.age.
// ResolveVaultPath exposes the resolved vault blob path for local key-touching
// ops (e.g. `vault rotate-key`) that operate on the blob directly rather than
// through the loaded owner.
func ResolveVaultPath(configDir string, cfg *Config) string {
	return resolveVaultPath(configDir, cfg)
}

func resolveVaultPath(configDir string, cfg *Config) string {
	if cfg != nil && cfg.Secrets.VaultFile != "" {
		p := interpolateEnv(cfg.Secrets.VaultFile)
		if !filepath.IsAbs(p) {
			p = filepath.Join(configDir, p)
		}
		return p
	}
	return filepath.Join(configDir, "vault.age")
}

// logGraftWarnings surfaces coexistence-window collision warnings to the
// operator. Kept here (not in load) so the warning text has one home.
func logGraftWarnings(warnings []string) {
	for _, w := range warnings {
		slog.Warn(w)
	}
}
