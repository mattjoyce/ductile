package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/secrets"
	"github.com/mattjoyce/ductile/internal/vault"
)

// projectVaultSecrets loads the vault owner and projects its active,
// gateway-visible secrets into cfg.ResolvedSecrets — the single secret-resolution
// table for the gateway's own consumers (webhook/relay HMAC). The vault is the
// sole source (epic #48): there is no tokens.yaml and no merge. The owner is
// returned so the daemon reuses this single decryption as its live owner instead
// of decrypting the blob again at runtime construction (#43 redundant decrypt).
//
// Plugin secrets are NOT projected here — they reach their consumer at *spawn* via
// Compose with live fingerprint re-verification (#14). activeVaultSecrets excludes
// plugin-scoped secrets for that reason.
//
// Snapshot-at-load (snapshot-at-reload): a rolled webhook/relay secret takes
// effect on the running servers after the daemon RELOADS; plugin secrets re-resolve
// at the next spawn. (OPERATOR_GUIDE.md "Rolling a webhook/relay secret".)
//
// Degradation is fail-open *for visibility, not secrecy*:
//   - no vault file yet  -> nil owner, no secrets (early in a deploy)
//   - keyless caller     -> nil owner (static `config validate` / CLI tools cannot
//     decrypt; vault-only secrets are the daemon's to resolve, per ADR §3.5.1)
//   - present but broken  -> error (fail-closed: a corrupt/owned vault must not be
//     silently skipped)
func projectVaultSecrets(cfg *Config, configDir string, kr *secrets.Keyring) (*vault.Vault, []string, error) {
	owner, err := loadVaultOwner(configDir, cfg, kr)
	if err != nil {
		return nil, nil, err
	}
	if owner == nil {
		return nil, nil, nil // no vault / keyless
	}

	// Snapshot (an independent deep copy) so we never alias the owner's live Store
	// past the read lock.
	store, err := owner.Snapshot()
	if err != nil {
		return nil, nil, err
	}
	secretsMap, warnings := activeVaultSecrets(store)
	cfg.ResolvedSecrets = secretsMap
	return owner, warnings, nil
}

// LoadVault resolves the keyring and loads the vault *owner* for the active
// config, or nil when there is no vault to load. It is the runtime's entry point
// for the in-memory vault model: the daemon holds this single owner and routes
// both the spawn-time read path (Compose) and management writes (SetSecret)
// through it.
//
// Degradation mirrors projectVaultSecrets: no vault file or a keyless caller
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
// projectVaultSecrets for the rationale), shared by the projection and LoadVault.
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


// activeVaultSecrets projects the store's active, gateway-visible secrets into a
// name->value map for the load-time projection, plus any blast-radius warnings.
// Two exclusions:
//   - revoked secrets — a revoked secret must not resolve.
//   - exclusively plugin-scoped secrets — these reach their consumer at *spawn*
//     via Compose (#14), so projecting them into cfg.ResolvedSecrets would leak
//     them to every gateway/load-time consumer. The projection serves only the
//     gateway's own consumers (webhook/relay HMAC); plugin delivery is the
//     dispatcher's job.
//
// Warn-only blast-radius guard (#41, Hickey-Armstrong Rev2 §1.2): a grant naming
// an UNREGISTERED principal keeps the secret gateway-visible (fail toward
// visibility — the documented Rung-2 choice), but is almost always an operator
// typo that silently widens the secret's reach. We surface it as a loud warning
// rather than silently hiding the secret (which would break a legitimate
// consumer) or silently widening it (the original smell).
func activeVaultSecrets(s *vault.Store) (map[string]string, []string) {
	out := make(map[string]string)
	var warnings []string
	for _, name := range s.SecretNames() {
		sec, ok := s.Secret(name)
		if !ok || sec.Status != vault.StatusActive {
			continue
		}
		if pluginScopedSecret(s, sec) {
			continue
		}
		if grantee := unregisteredGrantee(s, sec); grantee != "" {
			warnings = append(warnings, fmt.Sprintf(
				"secret %q grants to unregistered principal %q; it stays load-time visible to gateway consumers — register the principal or fix the grant",
				name, grantee))
		}
		out[name] = sec.Value
	}
	return out, warnings
}

// unregisteredGrantee returns the first authorized principal of sec that is not
// registered in the store, or "" if every grantee resolves. A dangling grantee
// is the signal that a secret is gateway-visible by accident (a typo) rather than
// by design — see the warn-only guard in activeVaultSecrets.
func unregisteredGrantee(s *vault.Store, sec *vault.Secret) string {
	for _, name := range sec.AuthorizedPrincipals {
		if _, ok := s.Principal(name); !ok {
			return name
		}
	}
	return ""
}

// pluginScopedSecret reports whether a secret is authorized *exclusively* to
// plugin principals — the case where delivery happens at spawn, not load time.
// A secret with no principals (e.g. a value set directly in the vault) is NOT
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
		return false // no vault present → not blind (nothing to read)
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

// logSecretProjectionWarnings surfaces the secret projection's blast-radius
// warnings to the operator. Kept here (not in load) so the text has one home.
func logSecretProjectionWarnings(warnings []string) {
	for _, w := range warnings {
		slog.Warn(w)
	}
}
