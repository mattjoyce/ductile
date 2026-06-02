package config

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/zeebo/blake3"
)

// sortPluginFingerprintsByName sorts a fingerprint slice in place by Name — the
// canonical order recorded in .checksums and hashed by PluginFingerprintsCode.
func sortPluginFingerprintsByName(fps []PluginFingerprint) {
	sort.Slice(fps, func(i, j int) bool { return fps[i].Name < fps[j].Name })
}

// PreservePluginFingerprints carries forward recorded plugin fingerprints for
// plugins that are still configured and drops entries for plugins no longer in
// the configured set. It NEVER recomputes a hash from disk — the bytes are
// carried forward verbatim. This is the projection a routine `config lock`
// applies instead of re-blessing whatever bytes are currently on disk (ADR §3.1,
// closes Threat A — lock-laundering). The result is sorted by Name.
func PreservePluginFingerprints(existing []PluginFingerprint, configured map[string]bool) []PluginFingerprint {
	out := make([]PluginFingerprint, 0, len(existing))
	for _, fp := range existing {
		if _, ok := configured[fp.Name]; ok {
			out = append(out, fp)
		}
	}
	sortPluginFingerprintsByName(out)
	return out
}

// MergePluginFingerprint replaces the entry whose Name matches fp, or appends fp
// when no such entry exists, leaving every other entry untouched. Re-attesting
// plugin A can never sweep in a swapped plugin B (ADR §3.1). The result is sorted
// by Name.
func MergePluginFingerprint(existing []PluginFingerprint, fp PluginFingerprint) []PluginFingerprint {
	out := make([]PluginFingerprint, 0, len(existing)+1)
	replaced := false
	for _, e := range existing {
		if e.Name == fp.Name {
			out = append(out, fp)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, fp)
	}
	sortPluginFingerprintsByName(out)
	return out
}

// PluginFingerprintsCode derives the 5-character alphanumeric confirmation code
// for `ductile plugin lock --all`. It is the first 5 hex chars of a BLAKE3 digest
// over the proposed set's identity tuples (sorted by Name), so it is order-
// independent and bound to exactly the bytes being authorized: any change to any
// plugin's manifest/entrypoint hash yields a different code, and a stale code
// from an earlier preview will no longer match (TOCTOU guard).
func PluginFingerprintsCode(set []PluginFingerprint) string {
	sorted := make([]PluginFingerprint, len(set))
	copy(sorted, set)
	sortPluginFingerprintsByName(sorted)

	h := blake3.New()
	for _, fp := range sorted {
		// NUL-delimited so field boundaries cannot be forged by concatenation.
		_, _ = h.Write([]byte(fp.Name + "\x00"))
		_, _ = h.Write([]byte(fp.Uses + "\x00"))
		_, _ = h.Write([]byte(fp.ManifestHash + "\x00"))
		_, _ = h.Write([]byte(fp.EntrypointHash + "\x00"))
		if fp.Enabled {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	return digest[:5]
}

// DiffPluginFingerprints classifies the move from a recorded fingerprint set to a
// proposed one for the `--all` preview. changed = present in both with a differing
// manifest/entrypoint hash; added = only in proposed; removed = only in existing.
// All three are sorted by Name.
func DiffPluginFingerprints(existing, proposed []PluginFingerprint) (changed, added, removed []string) {
	prev := make(map[string]PluginFingerprint, len(existing))
	for _, fp := range existing {
		prev[fp.Name] = fp
	}
	next := make(map[string]PluginFingerprint, len(proposed))
	for _, fp := range proposed {
		next[fp.Name] = fp
	}
	for name, np := range next {
		old, ok := prev[name]
		if !ok {
			added = append(added, name)
			continue
		}
		if old.ManifestHash != np.ManifestHash || old.EntrypointHash != np.EntrypointHash {
			changed = append(changed, name)
		}
	}
	for name := range prev {
		if _, ok := next[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(added)
	sort.Strings(removed)
	return changed, added, removed
}

// WritePluginFingerprints rewrites .checksums in configDir with the given plugin
// fingerprint set while PRESERVING the existing config-file hashes. It is the
// writer for `ductile plugin lock` (attestation), which only ever changes the
// plugin_fingerprints section. The directory must already be locked (`config
// lock`) so there are config-file hashes to preserve; an absent .checksums is a
// hard error directing the operator to lock the config first. The set is written
// sorted by Name; an empty set clears the section (the prune-all case).
func WritePluginFingerprints(configDir string, fingerprints []PluginFingerprint, dryRun bool) error {
	manifest, err := LoadChecksums(configDir)
	if err != nil {
		return err
	}
	sorted := make([]PluginFingerprint, len(fingerprints))
	copy(sorted, fingerprints)
	sortPluginFingerprintsByName(sorted)
	manifest.PluginFingerprints = nil
	if len(sorted) > 0 {
		manifest.PluginFingerprints = sorted
	}
	manifest.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	if dryRun {
		return nil
	}
	return writeChecksumsAtomic(filepath.Join(configDir, ".checksums"), *manifest)
}

// GenerateChecksumsWithFingerprints writes a v2 manifest embedding BLAKE3 hashes
// for every discovered config file plus a PRECOMPUTED plugin fingerprint set. It
// hashes no plugin bytes itself — the decoupled writer used by `config lock`
// (which preserves the prior set) and by the commit step of `plugin lock`. The
// fingerprint slice is written sorted by Name; an empty slice omits the section.
func GenerateChecksumsWithFingerprints(files *ConfigFiles, fingerprints []PluginFingerprint, dryRun bool) error {
	manifest := ChecksumManifest{
		Version:     2,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Hashes:      make(map[string]string),
	}
	for _, path := range files.AllFiles() {
		hash, err := ComputeBlake3Hash(path)
		if err != nil {
			return fmt.Errorf("failed to hash %s: %w", path, err)
		}
		manifest.Hashes[path] = hash
	}
	sorted := make([]PluginFingerprint, len(fingerprints))
	copy(sorted, fingerprints)
	sortPluginFingerprintsByName(sorted)
	if len(sorted) > 0 {
		manifest.PluginFingerprints = sorted
	}
	if dryRun {
		return nil
	}
	checksumPath := filepath.Join(files.Root, ".checksums")
	return writeChecksumsAtomic(checksumPath, manifest)
}
