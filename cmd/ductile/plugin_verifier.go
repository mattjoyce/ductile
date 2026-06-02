package main

import (
	"fmt"
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
)

// fingerprintNonceSource supplies the vault-held keyed-hash nonce. Satisfied by
// *vault.Vault; narrowed here so the verifier depends only on what it needs.
type fingerprintNonceSource interface {
	FingerprintNonce() ([]byte, error)
}

// pluginIdentityVerifier re-verifies a plugin's live bytes against its recorded
// keyed fingerprint at spawn (ADR §3.3). It satisfies dispatch.PluginVerifier.
//
// It reads .checksums fresh per call — the single source of truth for the locked
// fingerprint; forging it requires the keyed nonce, so re-reading is safe and
// picks up a legitimate re-lock — resolves the plugin's current bytes via the
// live registry, and keys the hash with the vault nonce. Fail-closed: an
// unreadable .checksums, a plugin with no recorded fingerprint, an undiscoverable
// plugin, a missing nonce, or any hash mismatch all deny.
type pluginIdentityVerifier struct {
	registry  *plugin.Registry
	configDir string
	nonceSrc  fingerprintNonceSource
}

func newPluginIdentityVerifier(registry *plugin.Registry, configDir string, nonceSrc fingerprintNonceSource) *pluginIdentityVerifier {
	return &pluginIdentityVerifier{registry: registry, configDir: configDir, nonceSrc: nonceSrc}
}

// VerifyIdentity implements dispatch.PluginVerifier.
func (v *pluginIdentityVerifier) VerifyIdentity(name string) error {
	manifest, err := config.LoadChecksums(v.configDir)
	if err != nil {
		return fmt.Errorf("plugin %q: cannot read attestation (.checksums): %w", name, err)
	}
	recorded, ok := manifest.FindPluginFingerprint(name)
	if !ok {
		return fmt.Errorf("plugin %q has no recorded fingerprint; run 'ductile plugin lock %s' before it can receive secrets", name, name)
	}

	p, ok := v.registry.Get(name)
	if !ok {
		return fmt.Errorf("plugin %q is configured for secrets but was not discovered for re-verification", name)
	}

	nonce, err := v.nonceSrc.FingerprintNonce()
	if err != nil {
		return fmt.Errorf("plugin %q: %w", name, err)
	}

	current := config.ResolvedPlugin{
		Name:           name,
		ManifestPath:   filepath.Join(p.Path, "manifest.yaml"),
		EntrypointPath: p.Entrypoint,
	}
	return config.VerifyResolvedPluginFingerprint(recorded, current, nonce)
}
