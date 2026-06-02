package config

import "fmt"

// shortHash truncates a hex digest to a 12-char prefix for diagnostics — enough
// to disambiguate without dumping the full hash. Shared by the fingerprint verify
// paths.
func shortHash(h string) string {
	if len(h) < 12 {
		return h
	}
	return h[:12]
}

// VerifyResolvedPluginFingerprint re-hashes one plugin's live bytes (keyed with
// the vault fingerprint nonce) and compares them against the recorded
// fingerprint. It is the focused, single-plugin check behind compose-time
// re-verification (ADR §3.3): the gateway calls it right before delivering a
// plugin's secrets, so a binary swapped after the last reload is caught before it
// receives anything.
//
// Fail-closed (Armstrong): a missing/short nonce, an unreadable plugin file, or a
// manifest/entrypoint hash mismatch is a hard error — never a silent pass. The
// nonce length is validated by ComputePluginFingerprint, so an unkeyed downgrade
// cannot slip through here.
func VerifyResolvedPluginFingerprint(recorded PluginFingerprint, current ResolvedPlugin, nonce []byte) error {
	currentFP, err := ComputePluginFingerprint(current, nonce)
	if err != nil {
		return fmt.Errorf("plugin %q: re-verify failed to fingerprint live bytes: %w", current.Name, err)
	}
	if currentFP.ManifestHash != recorded.ManifestHash {
		return fmt.Errorf("plugin %q: manifest hash mismatch at %s (recorded %s, live %s)",
			current.Name, currentFP.ManifestResolvedPath, shortHash(recorded.ManifestHash), shortHash(currentFP.ManifestHash))
	}
	if currentFP.EntrypointHash != recorded.EntrypointHash {
		return fmt.Errorf("plugin %q: entrypoint hash mismatch at %s (recorded %s, live %s)",
			current.Name, currentFP.EntrypointResolvedPath, shortHash(recorded.EntrypointHash), shortHash(currentFP.EntrypointHash))
	}
	return nil
}
