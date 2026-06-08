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
// value, mirroring the webhook/relay secret_ref pattern: the reference resolves
// against cfg.ResolvedSecrets (the vault projection built at load), never by
// sniffing or mutating the Token field.
//
// Fail-closed (Armstrong): an unresolvable or empty secret_ref is a hard error,
// so the caller (buildRuntime, before the listener opens) refuses to start — the
// API never authenticates against an empty credential. Each token must name
// EXACTLY ONE source; literal tokens still work but yield a migration warning
// (returned, logged by the caller) so an operator is told to move the secret
// into the vault.
func ResolveAPITokens(cfg *Config) ([]ResolvedAPIToken, []string, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("api tokens: config is nil")
	}

	resolved := make([]ResolvedAPIToken, 0, len(cfg.API.Auth.Tokens))
	var warnings []string

	for i, t := range cfg.API.Auth.Tokens {
		hasRef := t.SecretRef != ""
		hasLiteral := t.Token != ""

		switch {
		case hasRef && hasLiteral:
			return nil, nil, fmt.Errorf(
				"api.auth.tokens[%d]: set exactly one of token or secret_ref, not both", i)
		case hasRef:
			secret, ok := cfg.ResolvedSecrets[t.SecretRef]
			if !ok {
				return nil, nil, fmt.Errorf(
					"api.auth.tokens[%d]: secret_ref %q not found in the vault", i, t.SecretRef)
			}
			if secret == "" {
				return nil, nil, fmt.Errorf(
					"api.auth.tokens[%d]: secret_ref %q resolved to an empty value", i, t.SecretRef)
			}
			resolved = append(resolved, ResolvedAPIToken{Token: secret, Scopes: t.Scopes})
		case hasLiteral:
			warnings = append(warnings, fmt.Sprintf(
				"api.auth.tokens[%d]: literal token value is a secret outside the vault — "+
					"move it to a vault secret and use secret_ref (ADR §8.5, card #94)", i))
			resolved = append(resolved, ResolvedAPIToken{Token: t.Token, Scopes: t.Scopes})
		default:
			return nil, nil, fmt.Errorf(
				"api.auth.tokens[%d]: must set token or secret_ref (got neither — "+
					"an unresolved ${ENV} token lands here)", i)
		}
	}

	return resolved, warnings, nil
}
