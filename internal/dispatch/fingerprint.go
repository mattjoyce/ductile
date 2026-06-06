package dispatch

import (
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// mostRestrictedAccountName is, by convention in the shipped two-tier model, the
// isolated tier a fingerprint-mismatched plugin is downgraded to (it never shares
// a uid with secret-holding plugins). If a deployment renames or omits it, the
// downgrade has no safe target and the spawn fails closed instead.
const mostRestrictedAccountName = "untrusted"

// AccountDowngraded marks a account identity that was downgraded from its granted
// tier because the plugin failed fingerprint attestation (#93).
const AccountDowngraded AccountSource = "downgraded"

// bindAccountToFingerprint ties a resolved account grant to the plugin's recorded
// fingerprint (PrivSec ADR §4; card #93), reusing the keyed-nonce attestation from
// #12 (the same PluginVerifier the secret path uses). A binary whose bytes changed
// since its grant must NOT inherit the granted (possibly trusted) account identity:
// it is downgraded to the most-restricted tier so a supply-chain swap cannot reach
// a sibling's memory.
//
// This is blast-radius reduction, NOT a code-execution gate (grill B5): it bounds
// what a swapped binary can do, it does not decide whether it runs — the execution
// gate is the registry/.checksums path. The one exception is fail-closed safety:
// if there is no most-restricted tier to downgrade to, the spawn is refused (we
// will not run a swapped binary at its granted identity).
//
// Unconfined resolutions and a nil verifier (attestation not wired) pass through
// unchanged.
func bindAccountToFingerprint(resolved ResolvedAccount, cfg *config.Config, plugin string, verifier PluginVerifier) (ResolvedAccount, error) {
	if !resolved.Confined || verifier == nil {
		return resolved, nil
	}
	if err := verifier.VerifyIdentity(plugin); err == nil {
		return resolved, nil // bytes match the grant: keep it
	} else {
		restricted, ok := mostRestrictedAccount(cfg)
		if !ok {
			return ResolvedAccount{}, fmt.Errorf("%w: plugin %q failed fingerprint attestation and no %q tier exists to downgrade to: %v",
				ErrAccountDropFailed, plugin, mostRestrictedAccountName, err)
		}
		return restricted, nil
	}
}

func mostRestrictedAccount(cfg *config.Config) (ResolvedAccount, bool) {
	w, ok := cfg.Accounts[mostRestrictedAccountName]
	if !ok {
		return ResolvedAccount{}, false
	}
	return ResolvedAccount{
		Name:     mostRestrictedAccountName,
		UID:      w.UID,
		GID:      w.GID,
		StateDir: w.StateDir,
		Confined: true,
		Source:   AccountDowngraded,
	}, true
}
