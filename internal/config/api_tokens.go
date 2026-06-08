package config

import "fmt"

// ResolvedAPIToken is an API bearer token after secret_ref resolution: the
// concrete credential the auth layer compares against, plus its scopes. It is a
// distinct value from APIToken so resolution never overwrites the reference with
// the secret (card #94 — provenance is preserved in cfg.API.Auth.Tokens).
type ResolvedAPIToken struct {
	Token  string
	Scopes []string
}

// ResolveAPITokens turns each configured API auth token into its concrete bearer
// value. API bearer tokens are secrets, so they are VAULT-ONLY (#94, ADR §8.5:
// "if it's a secret, it's in the vault"): every token MUST carry a secret_ref
// that resolves against cfg.ResolvedSecrets (the vault projection built at load).
// There is deliberately no YAML path for an API secret — a literal token value is
// rejected outright (no migration warning, no coexistence window: we are not
// going back).
//
// Fail-closed (Armstrong): a literal value, a missing secret_ref, or a ref that
// is absent or resolves empty is a hard error, so the caller (buildRuntime,
// before the listener opens) refuses to start — the API never authenticates
// against an absent or out-of-vault credential.
func ResolveAPITokens(cfg *Config) ([]ResolvedAPIToken, error) {
	if cfg == nil {
		return nil, fmt.Errorf("api tokens: config is nil")
	}

	resolved := make([]ResolvedAPIToken, 0, len(cfg.API.Auth.Tokens))

	for i, t := range cfg.API.Auth.Tokens {
		if t.Token != "" {
			return nil, fmt.Errorf(
				"api.auth.tokens[%d]: a literal token value is not allowed — API secrets live in the vault; use secret_ref (ADR §8.5, #94)", i)
		}
		if t.SecretRef == "" {
			return nil, fmt.Errorf(
				"api.auth.tokens[%d]: secret_ref is required (API tokens are vault-only, #94)", i)
		}
		secret, ok := cfg.ResolvedSecrets[t.SecretRef]
		if !ok {
			return nil, fmt.Errorf(
				"api.auth.tokens[%d]: secret_ref %q not found in the vault", i, t.SecretRef)
		}
		if secret == "" {
			return nil, fmt.Errorf(
				"api.auth.tokens[%d]: secret_ref %q resolved to an empty value", i, t.SecretRef)
		}
		resolved = append(resolved, ResolvedAPIToken{Token: secret, Scopes: t.Scopes})
	}

	return resolved, nil
}
