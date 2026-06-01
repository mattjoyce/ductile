package dispatch

import (
	"errors"
	"log/slog"

	"github.com/mattjoyce/ductile/internal/vault"
)

// SecretComposer resolves the secrets a principal may receive. It is satisfied
// by *vault.Store. Defined here, at the point of use, so the dispatch package
// depends on the narrow query it needs — not the whole vault surface.
type SecretComposer interface {
	Compose(principal string) (vault.Composition, error)
}

// composePluginSecrets resolves the secrets to deliver to a plugin at spawn.
//
// Registration as a vault principal is purely about secret *authorization*; a
// plugin's identity and right to run are governed elsewhere (the plugin
// registry + .checksums attestation). The vault is therefore an opt-in overlay,
// and this contract reflects that:
//
//   - composer nil          -> no secrets, no error (vault not wired)
//   - unknown principal      -> no secrets, no error (the plugin is simply not in
//     the secret model; it runs and resolves via its normal config)
//   - registered + revoked   -> error (fail closed: an explicit revocation is a
//     "must not operate" signal and must stop the spawn, not run secret-less)
//   - registered + active    -> the composed secrets; withheld (revoked) grants
//     are surfaced as logged denials, not a fatal error
//
// Any Compose error other than a benign unknown-principal also fails closed — a
// composer that cannot answer is never treated as "no secrets."
func composePluginSecrets(composer SecretComposer, plugin string, logger *slog.Logger) (map[string]string, error) {
	if composer == nil {
		return nil, nil
	}

	comp, err := composer.Compose(plugin)
	if err != nil {
		if errors.Is(err, vault.ErrUnknownPrincipal) {
			return nil, nil // opt-out: plugin is not a vault principal
		}
		return nil, err // revoked principal or any other error: fail closed
	}

	for _, d := range comp.Denials {
		logger.Warn("secret withheld from plugin",
			"plugin", plugin, "secret", d.Secret, "reason", string(d.Reason))
	}
	return comp.Secrets, nil
}
