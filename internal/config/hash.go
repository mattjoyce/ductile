package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattjoyce/ductile/internal/fsown"
	"github.com/zeebo/blake3"
	"gopkg.in/yaml.v3"
)

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

// ComputeBlake3Hash computes the BLAKE3 hash of a file.
func ComputeBlake3Hash(filePath string) (string, error) {
	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	hash := blake3.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// ComputeKeyedBlake3Hash computes the keyed BLAKE3 hash of a file using a
// 32-byte key — the vault fingerprint nonce. Keyed hashing makes a plugin
// fingerprint unforgeable without the key: an attacker who cannot read the
// vault cannot produce a matching .checksums entry (closes ADR Threat B for a
// non-key-holder). The key MUST be exactly 32 bytes; a wrong-length key is a
// hard error rather than a silent fall-through to an unkeyed digest.
func ComputeKeyedBlake3Hash(filePath string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("keyed hash: key must be 32 bytes, got %d", len(key))
	}
	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	h, err := blake3.NewKeyed(key)
	if err != nil {
		return "", fmt.Errorf("keyed hash: %w", err)
	}
	_, _ = h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFileHash verifies a file against an expected BLAKE3 hash.
func VerifyFileHash(filePath, expectedHash string) error {
	actualHash, err := ComputeBlake3Hash(filePath)
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
// Writes v2 manifests with absolute path keys.
func GenerateChecksumsWithReport(configDir string, scopeFiles []string, dryRun bool) (*HashUpdateReport, error) {
	manifest := ChecksumManifest{
		Version:     2,
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

		hash, err := ComputeBlake3Hash(filePath)
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

// writeChecksumsAtomic marshals the manifest and writes it to path via a
// temp file + fsync + rename so a crash mid-write cannot corrupt the existing
// .checksums file. Perms are 0600 (contains expected hashes).
func writeChecksumsAtomic(path string, manifest ChecksumManifest) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("failed to marshal checksums: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".checksums.tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create checksums temp file: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to write checksums temp: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to chmod checksums temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("failed to fsync checksums temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("failed to close checksums temp: %w", err)
	}
	// #167: inherit the intended owner before publishing. Under `sudo config
	// lock` on a privsep install the temp file is root-owned; without this the
	// renamed manifest is unreadable by the service account and the next boot
	// fails admission.
	if err := fsown.ApplyToTemp(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("failed to rename checksums: %w", err)
	}
	return nil
}

// LoadChecksums reads the .checksums file from a config directory.
// Only v2 manifests (absolute path keys) are supported.
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

	if manifest.Version != 2 {
		return nil, fmt.Errorf("unsupported checksums version: %d", manifest.Version)
	}

	return &manifest, nil
}

// GenerateChecksumsFromDiscovery computes BLAKE3 hashes for all discovered files
// and writes a v2 manifest with absolute path keys.
func GenerateChecksumsFromDiscovery(files *ConfigFiles, dryRun bool) error {
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

	if dryRun {
		return nil
	}

	checksumPath := filepath.Join(files.Root, ".checksums")
	return writeChecksumsAtomic(checksumPath, manifest)
}
