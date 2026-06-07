package dispatch

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/mattjoyce/ductile/internal/vault"
)

// SecretComposer resolves the secrets a principal may receive. It is satisfied
// by *vault.Store. Defined here, at the point of use, so the dispatch package
// depends on the narrow query it needs — not the whole vault surface.
type SecretComposer interface {
	Compose(principal string) (vault.Composition, error)
}

// PluginVerifier re-verifies a plugin's live bytes against its recorded keyed
// fingerprint at spawn (ADR §3.3), closing the runtime-swap window for the secret
// path: a binary swapped after the last reload is caught before its secrets are
// delivered. Defined at the point of use; satisfied by a runtime adapter that
// wraps the plugin registry, .checksums, and the vault nonce. A nil verifier
// disables the gate (back-compat).
type PluginVerifier interface {
	// VerifyIdentity returns nil when the plugin's live bytes match its recorded
	// attestation, or an error to fail closed (mismatch / no recorded
	// fingerprint / unreadable bytes / no nonce).
	VerifyIdentity(plugin string) error
}

// ErrFingerprintMismatch marks a compose-time attestation failure. composePlugin-
// Secrets wraps the verifier's error with it so the dispatcher's audit fact carries
// the fingerprint_mismatch reason and downstream alerting (#25) can branch with
// errors.Is. Its text IS the vault DenialFingerprintMismatch reason.
var ErrFingerprintMismatch = errors.New(string(vault.DenialFingerprintMismatch))

// ErrVaultPrincipalRequired marks a plugin declared `requires_vault: true` whose
// vault principal is unknown/unregistered. It closes the fail-open seam (#108):
// without the declaration, an unknown principal opts out silently (fine for a
// keyless plugin, a footgun for one that should receive secrets). With it, the
// spawn fails CLOSED and loud — a misnamed/missing principal can no longer run
// the plugin secret-less by accident.
var ErrVaultPrincipalRequired = errors.New("vault: plugin requires vault secrets but its principal is unknown/unregistered")

// composePluginSecrets resolves the secrets to deliver to a plugin at spawn.
//
// Registration as a vault principal is purely about secret *authorization*; a
// plugin's identity and right to run are governed elsewhere (the plugin
// registry + .checksums attestation). The vault is therefore an opt-in overlay,
// and this contract reflects that:
//
//   - composer nil          -> no secrets, no error (vault not wired)
//   - unknown principal, requiresVault=false -> no secrets, no error (the plugin
//     is simply not in the secret model; it runs and resolves via its normal config)
//   - unknown principal, requiresVault=true  -> error (fail closed, #108: the plugin
//     DECLARED it needs vault secrets, so a missing/misnamed principal is a
//     misconfiguration, not a silent opt-out)
//   - registered + revoked   -> error (fail closed: an explicit revocation is a
//     "must not operate" signal and must stop the spawn, not run secret-less)
//   - registered + active    -> the composed secrets; withheld (revoked) grants
//     are surfaced as logged denials, not a fatal error
//
// Any Compose error other than a benign unknown-principal also fails closed — a
// composer that cannot answer is never treated as "no secrets."
// principal is the vault principal the plugin composes its secrets under — the
// plugin name by default, or plugins.<name>.vault_principal when set (#107: lets a
// snake_case plugin map to a kebab principal the vault will accept). Attestation
// (verifier) still uses the plugin name, since it gates the plugin's own bytes.
func composePluginSecrets(composer SecretComposer, verifier PluginVerifier, plugin, principal string, requiresVault bool, logger *slog.Logger) (map[string]string, error) {
	if composer == nil {
		if requiresVault {
			return nil, fmt.Errorf("%w: %q (principal %q, no vault wired)", ErrVaultPrincipalRequired, plugin, principal)
		}
		return nil, nil
	}

	comp, err := composer.Compose(principal)
	if err != nil {
		if errors.Is(err, vault.ErrUnknownPrincipal) {
			if requiresVault {
				// #108: declared requires_vault → an unknown principal fails CLOSED
				// and loud, instead of silently opting out and running secret-less.
				return nil, fmt.Errorf("%w: plugin %q → principal %q", ErrVaultPrincipalRequired, plugin, principal)
			}
			logger.Debug("plugin is not a vault principal; running keyless (opt-out)", "plugin", plugin, "principal", principal)
			return nil, nil // opt-out: plugin is not a vault principal
		}
		return nil, err // revoked principal or any other error: fail closed
	}

	// The plugin IS a vault principal about to receive secrets. Re-verify its live
	// bytes against the recorded keyed fingerprint right before delivery (§3.3) —
	// closing the window where a binary swapped since the last reload would be
	// handed this principal's secrets. Fail closed; a non-principal never reaches
	// here, so attestation gates only the secret path. A nil verifier disables the
	// gate (deployments not wired for attestation behave as before).
	if verifier != nil {
		if vErr := verifier.VerifyIdentity(plugin); vErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrFingerprintMismatch, vErr)
		}
	}

	for _, d := range comp.Denials {
		logger.Warn("secret withheld from plugin",
			"plugin", plugin, "secret", d.Secret, "reason", string(d.Reason))
	}
	return comp.Secrets, nil
}
