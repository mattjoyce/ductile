package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrChecksumsLegacy marks a .checksums manifest written by an older ductile
// (version 2, BLAKE3). It is refused; one 'ductile config lock' migrates it.
var ErrChecksumsLegacy = errors.New("checksums were written by an older ductile (BLAKE3, version 2); run 'ductile config lock' to re-lock under SHA-256")

// HashUpdateFileResult captures checksum generation outcome for a scope file.
type HashUpdateFileResult struct {
	Filename string
	Path     string
	Exists   bool
	Hash     string
}

// HashUpdateReport captures checksum generation details for a config directory.
type HashUpdateReport struct {
	ConfigDir    string
	ChecksumPath string
	Written      bool
	Files        []HashUpdateFileResult
}

// ComputeFileHash returns the SHA-256 of a file as "sha256:<hex>".
func ComputeFileHash(filePath string) (string, error) {
	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

// ComputeKeyedFileHash computes the HMAC-SHA256 of a file using a
// 32-byte key — the vault fingerprint nonce. Keyed hashing makes a plugin
// fingerprint unforgeable without the key: an attacker who cannot read the
// vault cannot produce a matching .checksums entry (closes ADR Threat B for a
// non-key-holder). The key MUST be exactly 32 bytes; a wrong-length key is a
// hard error rather than a silent fall-through to an unkeyed digest.
func ComputeKeyedFileHash(filePath string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("keyed hash: key must be 32 bytes, got %d", len(key))
	}
	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFileHash verifies a file against an expected SHA-256 hash.
func VerifyFileHash(filePath, expectedHash string) error {
	actualHash, err := ComputeFileHash(filePath)
	if err != nil {
		return fmt.Errorf("failed to compute hash: %w", err)
	}

	if actualHash != expectedHash {
		return fmt.Errorf("hash mismatch for %s: expected %s, got %s",
			filepath.Base(filePath), expectedHash, actualHash)
	}

	return nil
}

// GenerateChecksumsWithReport computes scope file hashes and optionally writes .checksums.
// When dryRun is true, it computes hashes and returns report details without writing files.
// Writes v3 manifests with absolute path keys.
func GenerateChecksumsWithReport(configDir string, scopeFiles []string, dryRun bool) (*HashUpdateReport, error) {
	manifest := ChecksumManifest{
		Version:     3,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Hashes:      make(map[string]string),
	}

	report := &HashUpdateReport{
		ConfigDir:    configDir,
		ChecksumPath: filepath.Join(configDir, ".checksums"),
		Written:      false,
		Files:        make([]HashUpdateFileResult, 0, len(scopeFiles)),
	}

	absDir, err := filepath.Abs(configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config dir %q: %w", configDir, err)
	}

	// Compute hash for each scope file
	for _, filename := range scopeFiles {
		filePath := filepath.Join(absDir, filename)

		// Skip if file doesn't exist (optional files)
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			report.Files = append(report.Files, HashUpdateFileResult{
				Filename: filename,
				Path:     filePath,
				Exists:   false,
				Hash:     "",
			})
			continue
		}

		hash, err := ComputeFileHash(filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to hash %s: %w", filename, err)
		}

		manifest.Hashes[filePath] = hash
		report.Files = append(report.Files, HashUpdateFileResult{
			Filename: filename,
			Path:     filePath,
			Exists:   true,
			Hash:     hash,
		})
	}

	if dryRun {
		return report, nil
	}

	if err := writeChecksumsAtomic(report.ChecksumPath, manifest); err != nil {
		return nil, err
	}
	report.Written = true

	return report, nil
}

// writeChecksumsAtomic marshals the manifest and publishes it at path.
//
// The temp → fsync → chown → rename body used to live here in full. It now
// delegates to WriteFileAtomic, which is the same sequence: consolidating was the
// stated point of #177, and two copies of this that agree today are two copies
// that can disagree tomorrow. Perms stay 0600 — the manifest carries expected
// hashes.
func writeChecksumsAtomic(path string, manifest ChecksumManifest) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal checksums: %w", err)
	}
	return WriteFileAtomic(path, data, 0600)
}

// LoadChecksums reads the .checksums file from a config directory.
// Only v3 manifests (absolute path keys, SHA-256 values) are supported; a v2
// manifest is refused with ErrChecksumsLegacy so one `config lock` can migrate it.
func LoadChecksums(configDir string) (*ChecksumManifest, error) {
	checksumPath := filepath.Join(configDir, ".checksums")

	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		// The wrap keeps errors.Is(err, fs.ErrNotExist) true so callers can tell
		// "never locked" from "locked but unreadable" (#167). Flattening the
		// cause here is what let a permission failure surface as a missing file.
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checksums file not found (run 'ductile config lock'): %w", err)
		}
		return nil, fmt.Errorf("failed to read checksums: %w", err)
	}

	var manifest ChecksumManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse checksums: %w", err)
	}

	if manifest.Version == 2 {
		return nil, fmt.Errorf("%s: %w", checksumPath, ErrChecksumsLegacy)
	}
	if manifest.Version != 3 {
		return nil, fmt.Errorf("unsupported checksums version: %d", manifest.Version)
	}

	return &manifest, nil
}

// LegacyPluginNames returns the plugin names recorded in a version 2 .checksums
// manifest, sorted. It exists only for the `config lock` migration: a version 3
// manifest is not legacy and is refused.
func LegacyPluginNames(configDir string) ([]string, error) {
	checksumPath := filepath.Join(configDir, ".checksums")

	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(checksumPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read checksums: %w", err)
	}

	var manifest ChecksumManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse checksums: %w", err)
	}
	if manifest.Version != 2 {
		return nil, fmt.Errorf("%s: not a legacy version 2 checksums manifest (got version %d)", checksumPath, manifest.Version)
	}

	names := make([]string, 0, len(manifest.PluginFingerprints))
	for _, fp := range manifest.PluginFingerprints {
		names = append(names, fp.Name)
	}
	sort.Strings(names)
	return names, nil
}

// GenerateChecksumsFromDiscovery computes SHA-256 hashes for all discovered files
// and writes a v3 manifest with absolute path keys.
func GenerateChecksumsFromDiscovery(files *ConfigFiles, dryRun bool) error {
	manifest := ChecksumManifest{
		Version:     3,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Hashes:      make(map[string]string),
	}

	for _, path := range files.AllFiles() {
		hash, err := ComputeFileHash(path)
		if err != nil {
			return fmt.Errorf("failed to hash %s: %w", path, err)
		}
		manifest.Hashes[path] = hash
	}

	if dryRun {
		return nil
	}

	checksumPath := filepath.Join(files.Root, ".checksums")
	return writeChecksumsAtomic(checksumPath, manifest)
}
