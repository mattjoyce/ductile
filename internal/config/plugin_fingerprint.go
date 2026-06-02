package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolvedPlugin describes a configured plugin after the caller has resolved it
// through the live plugin registry. The config package cannot import the plugin
// package (plugin → config would be circular), so the caller — the ductile CLI
// or boot path — builds []ResolvedPlugin from the registry and passes it in.
//
// ManifestPath and EntrypointPath MUST be absolute. Fingerprinting records both
// the configured absolute path and the symlink-resolved path used for hashing.
type ResolvedPlugin struct {
	Name           string
	Enabled        bool
	Uses           string // empty for non-aliases; alias base name otherwise
	ManifestPath   string
	EntrypointPath string
}

// ComputePluginFingerprint reads the resolved plugin's manifest and entrypoint
// files, hashes each with keyed BLAKE3 (using the vault fingerprint nonce), and
// returns a PluginFingerprint. Missing or unreadable files produce a hard error
// — the operator cannot lock a plugin whose bytes cannot be read.
//
// Attestation is keyed-or-nothing (operator decision, ADR §3.2 amended): the
// nonce MUST be the 32-byte vault fingerprint nonce. A missing/short nonce is a
// hard error, never a silent fall-through to an unkeyed digest — so a fingerprint
// can never be forged by anyone who cannot read the vault.
func ComputePluginFingerprint(rp ResolvedPlugin, nonce []byte) (PluginFingerprint, error) {
	if rp.Name == "" {
		return PluginFingerprint{}, fmt.Errorf("plugin name is required")
	}
	if len(nonce) != 32 {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: attestation requires the 32-byte vault fingerprint nonce (got %d bytes); ensure the vault is initialized and loaded", rp.Name, len(nonce))
	}
	if !filepath.IsAbs(rp.ManifestPath) {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: manifest path must be absolute, got %q", rp.Name, rp.ManifestPath)
	}
	if !filepath.IsAbs(rp.EntrypointPath) {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: entrypoint path must be absolute, got %q", rp.Name, rp.EntrypointPath)
	}

	manifestResolved, err := filepath.EvalSymlinks(rp.ManifestPath)
	if err != nil {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: resolve manifest %s: %w", rp.Name, rp.ManifestPath, err)
	}
	entrypointResolved, err := filepath.EvalSymlinks(rp.EntrypointPath)
	if err != nil {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: resolve entrypoint %s: %w", rp.Name, rp.EntrypointPath, err)
	}
	if info, err := os.Stat(entrypointResolved); err != nil {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: stat entrypoint %s: %w", rp.Name, entrypointResolved, err)
	} else if info.Mode()&0111 == 0 {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: entrypoint is not executable: %s", rp.Name, entrypointResolved)
	}

	manifestHash, err := ComputeKeyedBlake3Hash(manifestResolved, nonce)
	if err != nil {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: hash manifest %s: %w", rp.Name, manifestResolved, err)
	}
	entrypointHash, err := ComputeKeyedBlake3Hash(entrypointResolved, nonce)
	if err != nil {
		return PluginFingerprint{}, fmt.Errorf("plugin %q: hash entrypoint %s: %w", rp.Name, entrypointResolved, err)
	}

	return PluginFingerprint{
		Name:                   rp.Name,
		Enabled:                rp.Enabled,
		Uses:                   rp.Uses,
		ManifestPath:           rp.ManifestPath,
		ManifestResolvedPath:   manifestResolved,
		ManifestHash:           manifestHash,
		EntrypointPath:         rp.EntrypointPath,
		EntrypointResolvedPath: entrypointResolved,
		EntrypointHash:         entrypointHash,
	}, nil
}

// GenerateChecksumsWithPlugins computes BLAKE3 hashes for every discovered
// config file AND every resolved configured plugin, then writes a v2 manifest
// embedding plugin_fingerprints sorted by Name.
//
// When dryRun is true, no file is written but all hashing still runs so the
// caller can surface hash-time errors before commit.
func GenerateChecksumsWithPlugins(files *ConfigFiles, plugins []ResolvedPlugin, nonce []byte, dryRun bool) error {
	fingerprints := make([]PluginFingerprint, 0, len(plugins))
	for _, rp := range plugins {
		fp, err := ComputePluginFingerprint(rp, nonce)
		if err != nil {
			return err
		}
		fingerprints = append(fingerprints, fp)
	}
	// GenerateChecksumsWithFingerprints sorts the set and owns the manifest write,
	// so this function is only responsible for turning bytes into fingerprints.
	return GenerateChecksumsWithFingerprints(files, fingerprints, dryRun)
}

// VerifyPluginFingerprints compares recorded PluginFingerprint entries against
// the current configured plugin set and a live registry snapshot keyed by plugin
// Name. Returns an *IntegrityResult the caller merges with VerifyIntegrity's
// result.
//
// Classification (matches the red-teamed invariant — identity is bytes, not path):
//
//   - Currently enabled plugin, manifest or entrypoint hash mismatch → hard error.
//   - Currently disabled plugin, any mismatch → warning (operator may still be rebuilding unrelated bytes).
//   - Recorded plugin no longer present in configuredPlugins → warning (stale, run lock).
//   - Configured plugin missing from currentPlugins → error if enabled, warning if disabled.
//   - Configured plugin missing from fingerprints → error if enabled, warning if disabled.
//   - Bytes match but path changed → informational warning (not a security event).
//   - File read error: error if enabled, warning if disabled.
//
// Empty fingerprints slice returns a passing result with no findings so that
// deployments that have not opted into plugin fingerprinting see unchanged
// behavior.
//
// Every diagnostic message names the plugin, the path, short hash prefixes,
// and the literal recovery command so operators can always recover.
func VerifyPluginFingerprints(fingerprints []PluginFingerprint, configuredPlugins map[string]bool, currentPlugins map[string]ResolvedPlugin, nonce []byte) *IntegrityResult {
	result := &IntegrityResult{Passed: true}
	if len(fingerprints) == 0 {
		// Not attesting at all: a deployment with no recorded fingerprints opts
		// out and runs (vault-less is fine here — there is nothing to verify).
		return result
	}
	// Fail closed (Armstrong): recorded fingerprints are keyed, so verification
	// REQUIRES the vault nonce. A missing/short nonce must not silently pass —
	// that would be the downgrade attack (delete the vault, verify unkeyed).
	if len(nonce) != 32 {
		result.Passed = false
		result.Errors = append(result.Errors, fmt.Sprintf(
			"plugin attestation requires the 32-byte vault fingerprint nonce (got %d bytes); ensure the vault is initialized and loaded before verification",
			len(nonce)))
		return result
	}

	addFinding := func(msg string, enabled bool) {
		if enabled {
			result.Passed = false
			result.Errors = append(result.Errors, msg)
		} else {
			result.Warnings = append(result.Warnings, msg)
		}
	}

	locked := make(map[string]PluginFingerprint, len(fingerprints))
	for _, fp := range fingerprints {
		locked[fp.Name] = fp
		currentEnabled, stillConfigured := configuredPlugins[fp.Name]
		if !stillConfigured {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q recorded in .checksums but no longer configured; run 'ductile config lock' to clear stale entry",
				fp.Name))
			continue
		}

		current, resolved := currentPlugins[fp.Name]
		if !resolved {
			addFinding(fmt.Sprintf(
				"plugin %q is configured but was not discovered; run 'ductile plugin lock <name>' after restoring or removing the plugin",
				fp.Name), currentEnabled)
			continue
		}
		if fp.Enabled != currentEnabled {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q enabled state changed since lock (was %t, now %t); run 'ductile plugin lock <name>' to refresh the record",
				fp.Name, fp.Enabled, currentEnabled))
		}
		if fp.Uses != current.Uses {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q uses target changed since lock (was %q, now %q); run 'ductile plugin lock <name>' to refresh the record",
				fp.Name, fp.Uses, current.Uses))
		}

		currentFP, err := ComputePluginFingerprint(current, nonce)
		if err != nil {
			addFinding(fmt.Sprintf(
				"plugin %q: failed to fingerprint current plugin: %v; run 'ductile plugin lock <name>' after investigating",
				fp.Name, err), currentEnabled)
			continue
		}

		if currentFP.ManifestHash != fp.ManifestHash {
			addFinding(fmt.Sprintf(
				"plugin %q: manifest hash mismatch at %s (expected %s, got %s); run 'ductile plugin lock <name>' after reviewing the change",
				fp.Name, currentFP.ManifestResolvedPath, shortHash(fp.ManifestHash), shortHash(currentFP.ManifestHash)),
				currentEnabled)
		} else if current.ManifestPath != fp.ManifestPath {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q: manifest path changed but bytes match (was %s, now %s); run 'ductile plugin lock <name>' to refresh the record",
				fp.Name, fp.ManifestPath, current.ManifestPath))
		} else if fp.ManifestResolvedPath != "" && currentFP.ManifestResolvedPath != fp.ManifestResolvedPath {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q: manifest resolved path changed but bytes match (was %s, now %s); run 'ductile plugin lock <name>' to refresh the record",
				fp.Name, fp.ManifestResolvedPath, currentFP.ManifestResolvedPath))
		}

		if currentFP.EntrypointHash != fp.EntrypointHash {
			addFinding(fmt.Sprintf(
				"plugin %q: entrypoint hash mismatch at %s (expected %s, got %s); run 'ductile plugin lock <name>' after reviewing the change",
				fp.Name, currentFP.EntrypointResolvedPath, shortHash(fp.EntrypointHash), shortHash(currentFP.EntrypointHash)),
				currentEnabled)
		} else if current.EntrypointPath != fp.EntrypointPath {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q: entrypoint path changed but bytes match (was %s, now %s); run 'ductile plugin lock <name>' to refresh the record",
				fp.Name, fp.EntrypointPath, current.EntrypointPath))
		} else if fp.EntrypointResolvedPath != "" && currentFP.EntrypointResolvedPath != fp.EntrypointResolvedPath {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"plugin %q: entrypoint resolved path changed but bytes match (was %s, now %s); run 'ductile plugin lock <name>' to refresh the record",
				fp.Name, fp.EntrypointResolvedPath, currentFP.EntrypointResolvedPath))
		}
	}

	for name, enabled := range configuredPlugins {
		if _, ok := locked[name]; ok {
			continue
		}
		addFinding(fmt.Sprintf(
			"plugin %q is configured but missing from .checksums plugin_fingerprints; run 'ductile plugin lock <name>' to authorize it",
			name), enabled)
	}

	return result
}
