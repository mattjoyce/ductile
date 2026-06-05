package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

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
	logger    *slog.Logger
}

func newPluginIdentityVerifier(registry *plugin.Registry, configDir string, nonceSrc fingerprintNonceSource, logger *slog.Logger) *pluginIdentityVerifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &pluginIdentityVerifier{registry: registry, configDir: configDir, nonceSrc: nonceSrc, logger: logger}
}

// VerifyIdentity implements dispatch.PluginVerifier. Compose-time re-hashing
// (LoadChecksums + two keyed-BLAKE3 file hashes) is a per-spawn cost on the hot
// dispatch path; a debug-level timing line makes it observable rather than
// inferred, with no overhead at normal verbosity.
func (v *pluginIdentityVerifier) VerifyIdentity(name string) (err error) {
	start := time.Now()
	defer func() {
		outcome := "ok"
		if err != nil {
			outcome = "denied"
		}
		v.logger.Debug("plugin fingerprint re-verified at spawn",
			"plugin", name, "outcome", outcome, "duration", time.Since(start))
	}()

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
