package dispatch

import (
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// mostRestrictedWorkerName is, by convention in the shipped two-tier model, the
// isolated tier a fingerprint-mismatched plugin is downgraded to (it never shares
// a uid with secret-holding plugins). If a deployment renames or omits it, the
// downgrade has no safe target and the spawn fails closed instead.
const mostRestrictedWorkerName = "untrusted"

// WorkerDowngraded marks a worker identity that was downgraded from its granted
// tier because the plugin failed fingerprint attestation (#93).
const WorkerDowngraded WorkerSource = "downgraded"

// bindWorkerToFingerprint ties a resolved worker grant to the plugin's recorded
// fingerprint (PrivSec ADR §4; card #93), reusing the keyed-nonce attestation from
// #12 (the same PluginVerifier the secret path uses). A binary whose bytes changed
// since its grant must NOT inherit the granted (possibly trusted) worker identity:
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
func bindWorkerToFingerprint(resolved ResolvedWorker, cfg *config.Config, plugin string, verifier PluginVerifier) (ResolvedWorker, error) {
	if !resolved.Confined || verifier == nil {
		return resolved, nil
	}
	if err := verifier.VerifyIdentity(plugin); err == nil {
		return resolved, nil // bytes match the grant: keep it
	} else {
		restricted, ok := mostRestrictedWorker(cfg)
		if !ok {
			return ResolvedWorker{}, fmt.Errorf("%w: plugin %q failed fingerprint attestation and no %q tier exists to downgrade to: %v",
				ErrWorkerDropFailed, plugin, mostRestrictedWorkerName, err)
		}
		return restricted, nil
	}
}

func mostRestrictedWorker(cfg *config.Config) (ResolvedWorker, bool) {
	w, ok := cfg.Workers[mostRestrictedWorkerName]
	if !ok {
		return ResolvedWorker{}, false
	}
	return ResolvedWorker{
		Name:     mostRestrictedWorkerName,
		UID:      w.UID,
		GID:      w.GID,
		StateDir: w.StateDir,
		Confined: true,
		Source:   WorkerDowngraded,
	}, true
}
