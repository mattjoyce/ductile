package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// ConfigValidator validates cross-file references in multi-file config mode.
type ConfigValidator struct {
	config *Config
	tokens map[string]string
	// vaultBlind is true when a vault exists but this validation run is keyless
	// (e.g. static `config validate` / a CLI tool), so vault-only secrets are not
	// in tokens. A secret_ref we cannot resolve then becomes a warning, not an
	// error — authoritative resolution is the daemon's, which holds the key
	// (ADR §3.5.1; resolves the whole-store-encryption vs static-validate fork).
	vaultBlind bool
	// bootstrap is true in the from-scratch bootstrap condition (api enabled,
	// zero api tokens, vault present — DecideBootPosture's own predicate, #138).
	// An unminted secret_ref then warns instead of hard-erroring: the management
	// posture this config boots into exists precisely to mint it. The moment an
	// api token is configured (gateway activation) the condition is false and
	// resolution is strict again.
	bootstrap bool
}

// checkSecretRef verifies a secret_ref resolves. Missing is a hard error in the
// normal case, but only a warning when vaultBlind (the secret may be vault-only
// and invisible without the key). where is a human label for attribution.
func (v *ConfigValidator) checkSecretRef(ref, where string) error {
	if _, exists := v.tokens[ref]; exists {
		return nil
	}
	if v.vaultBlind {
		slog.Warn("secret_ref not resolvable without the vault key; assuming it is vault-resident "+
			"(validate with the key or via the daemon to confirm)", "secret_ref", ref, "at", where)
		return nil
	}
	if v.bootstrap {
		slog.Warn("secret_ref not minted yet — the management posture this config boots into exists to mint it: "+
			"boot, mint over the management socket, then reload to activate (#138)", "secret_ref", ref, "at", where)
		return nil
	}
	return fmt.Errorf("%s: secret_ref %q not found in the vault", where, ref)
}

// ValidateCrossReferences checks that all cross-file references are valid.
func (v *ConfigValidator) ValidateCrossReferences() error {
	// Validate routes reference valid plugins
	if err := v.validateRoutes(); err != nil {
		return err
	}

	// Validate webhooks reference valid plugins and tokens
	if err := v.validateWebhooks(); err != nil {
		return err
	}

	// Validate relay config references valid tokens.
	if err := v.validateRelay(); err != nil {
		return err
	}

	// Validate API auth tokens reference valid vault secrets.
	if err := v.validateAPITokens(); err != nil {
		return err
	}

	// Validate plugin configs with _ref suffixes reference valid tokens
	if err := v.validatePluginTokenRefs(); err != nil {
		return err
	}

	return nil
}

// validateRoutes checks that route from/to fields reference enabled plugins.
func (v *ConfigValidator) validateRoutes() error {
	if len(v.config.Routes) == 0 {
		return nil
	}

	for i, route := range v.config.Routes {
		// Validate 'from' plugin exists
		if _, exists := v.config.Plugins[route.From]; !exists {
			return fmt.Errorf("route[%d]: 'from' plugin %q does not exist", i, route.From)
		}

		// Validate 'to' plugin exists
		if _, exists := v.config.Plugins[route.To]; !exists {
			return fmt.Errorf("route[%d]: 'to' plugin %q does not exist", i, route.To)
		}

		// Enabled-state warnings are runtime concerns; cross-reference validation only checks existence.
	}

	return nil
}

// validateWebhooks checks that webhook endpoints reference valid plugins and secrets.
func (v *ConfigValidator) validateWebhooks() error {
	if v.config.Webhooks == nil {
		return nil
	}

	for i, endpoint := range v.config.Webhooks.Endpoints {
		// Validate plugin exists
		if _, exists := v.config.Plugins[endpoint.Plugin]; !exists {
			return fmt.Errorf("webhook[%d] (%s): plugin %q does not exist",
				i, endpoint.Path, endpoint.Plugin)
		}

		if endpoint.SecretRef == "" {
			return fmt.Errorf("webhook[%d] (%s): secret_ref is required",
				i, endpoint.Path)
		}

		if err := v.checkSecretRef(endpoint.SecretRef, fmt.Sprintf("webhook[%d] (%s)", i, endpoint.Path)); err != nil {
			return err
		}

		// Validate required fields
		if endpoint.SignatureHeader == "" {
			return fmt.Errorf("webhook[%d] (%s): signature_header is required",
				i, endpoint.Path)
		}
	}

	return nil
}

func (v *ConfigValidator) validateRelay() error {
	for i, instance := range v.config.RelayInstances {
		if strings.TrimSpace(instance.SecretRef) == "" {
			return fmt.Errorf("instances[%d] (%s): secret_ref is required", i, instance.Name)
		}
		if err := v.checkSecretRef(instance.SecretRef, fmt.Sprintf("instances[%d] (%s)", i, instance.Name)); err != nil {
			return err
		}
	}

	if v.config.RemoteIngress == nil {
		return nil
	}
	for i, peer := range v.config.RemoteIngress.TrustedPeers {
		if strings.TrimSpace(peer.SecretRef) == "" {
			return fmt.Errorf("remote_ingress.peers[%d] (%s): secret_ref is required", i, peer.Name)
		}
		if err := v.checkSecretRef(peer.SecretRef, fmt.Sprintf("remote_ingress.peers[%d] (%s)", i, peer.Name)); err != nil {
			return err
		}
	}

	return nil
}

// validateAPITokens checks that each API auth token is vault-only (#94, ADR §8.5):
// a literal token value is rejected outright and secret_ref is mandatory and must
// resolve. Mirrors the always-run validate() and ResolveAPITokens so the
// cross-reference pass gives the precise error rather than deferring to load.
// Resolution failure is warn-when-blind (the daemon, holding the key, is the
// authoritative check — see checkSecretRef).
func (v *ConfigValidator) validateAPITokens() error {
	for i, token := range v.config.API.Auth.Tokens {
		if token.Token != "" {
			return fmt.Errorf("api.auth.tokens[%d]: a literal token value is not allowed — API secrets live in the vault; use secret_ref (ADR §8.5, #94)", i)
		}
		if strings.TrimSpace(token.SecretRef) == "" {
			return fmt.Errorf("api.auth.tokens[%d]: secret_ref is required (API tokens are vault-only, #94)", i)
		}
		if err := v.checkSecretRef(token.SecretRef, fmt.Sprintf("api.auth.tokens[%d]", i)); err != nil {
			return err
		}
	}
	return nil
}

// validatePluginTokenRefs checks plugin config fields ending with _ref reference valid tokens.
func (v *ConfigValidator) validatePluginTokenRefs() error {
	for pluginName, plugin := range v.config.Plugins {
		if plugin.Config == nil {
			continue
		}

		for key, value := range plugin.Config {
			// Check if key ends with _ref
			if strings.HasSuffix(key, "_ref") {
				strValue, ok := value.(string)
				if !ok {
					return fmt.Errorf("plugin %q: config field %q must be a string",
						pluginName, key)
				}

				// Validate the referenced secret exists (vault).
				if err := v.checkSecretRef(strValue, fmt.Sprintf("plugin %q config field %q", pluginName, key)); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
