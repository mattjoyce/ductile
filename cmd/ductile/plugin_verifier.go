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

// clearpassSpawn describes one spawn the verifier let through on a fingerprint
// mismatch because the plugin's clearpass window was open (card #144). It is
// handed to the verifier's reporter so the runtime can log it, publish it and
// record it; the verifier itself only decides.
type clearpassSpawn struct {
	Plugin   string
	Recorded config.PluginFingerprint
	Live     config.PluginFingerprint
	Window   config.Clearpass
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
//
// The one deliberate relaxation is a clearpass: a mismatch inside an open window
// passes and is reported, never silently. The window must carry a seal under
// the same vault nonce that keys the fingerprint; an unsealed or tampered
// record is ignored and named in the denial. Outside the window the strict rule
// stands, and an expired window that the closer has not yet committed is named
// in the denial so the operator sees why.
type pluginIdentityVerifier struct {
	registry  *plugin.Registry
	configDir string
	nonceSrc  fingerprintNonceSource
	logger    *slog.Logger
	now       func() time.Time
	// onClearpass is called once per tolerated spawn. Nil means tolerate quietly
	// at the verifier level; the runtime always wires a reporter.
	onClearpass func(clearpassSpawn)
}

func newPluginIdentityVerifier(registry *plugin.Registry, configDir string, nonceSrc fingerprintNonceSource, logger *slog.Logger) *pluginIdentityVerifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &pluginIdentityVerifier{registry: registry, configDir: configDir, nonceSrc: nonceSrc, logger: logger, now: time.Now}
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
	live, err := config.ComputePluginFingerprint(current, nonce)
	if err != nil {
		return fmt.Errorf("plugin %q: re-verify failed to fingerprint live bytes: %w", name, err)
	}
	mismatch := config.MatchPluginFingerprint(recorded, live)
	if mismatch == nil {
		return nil
	}

	now := v.now()
	switch {
	case recorded.Clearpass != nil && !recorded.Clearpass.Verify(recorded, nonce):
		return fmt.Errorf("%w; a clearpass is recorded but its seal does not verify under the vault nonce, so it is ignored (re-open it with 'ductile plugin clearpass %s --for <d>' or lock with 'ductile plugin lock %s')",
			mismatch, name, name)
	case recorded.Clearpass.Open(now):
		if v.onClearpass != nil {
			v.onClearpass(clearpassSpawn{Plugin: name, Recorded: recorded, Live: live, Window: *recorded.Clearpass})
		}
		return nil
	case recorded.Clearpass.Expired(now):
		return fmt.Errorf("%w; clearpass expired at %s and is not yet committed (run 'ductile plugin clearpass %s --close' or 'ductile plugin lock %s')",
			mismatch, recorded.Clearpass.Until, name, name)
	default:
		return mismatch
	}
}
