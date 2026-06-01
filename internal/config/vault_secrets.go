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
// Degradation is deliberate and fail-open *only for visibility, not for secrecy*:
//   - no vault file yet  -> no-op (early in the migration window)
//   - keyless caller     -> no-op (static `config validate` / CLI tools cannot
//     decrypt; vault-only secrets are the daemon's to resolve, per ADR §3.5.1)
//   - present but broken  -> error (fail-closed: a corrupt/owned vault must not
//     be silently skipped)
func graftVaultSecrets(cfg *Config, configDir string, kr *secrets.Keyring) ([]string, error) {
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
		return nil, nil // keyless: cannot decrypt; resolve against tokens.yaml only
	}

	v, err := vault.Load(path, kr)
	if err != nil {
		return nil, err // present + keyed but broken: fail-closed
	}

	merged, warnings := mergeVaultSecrets(cfg.Tokens, activeVaultSecrets(v.Store()))
	cfg.Tokens = merged
	return warnings, nil
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

// activeVaultSecrets projects the store's active secrets into a name->value map.
// Revoked secrets are excluded — a revoked secret must not resolve.
func activeVaultSecrets(s *vault.Store) map[string]string {
	out := make(map[string]string)
	for _, name := range s.SecretNames() {
		if sec, ok := s.Secret(name); ok && sec.Status == vault.StatusActive {
			out[name] = sec.Value
		}
	}
	return out
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
