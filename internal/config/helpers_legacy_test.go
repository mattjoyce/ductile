package config

// Frozen copies of the helper bodies ductile carried before card 153 slice 2
// swapped them for Ashlar's (main at 561be9b). helpers_parity_test.go runs the
// same inputs through these and the live helpers. Do not "fix" them: they are
// the oracle.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func legacyComputeFileHash(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func legacyComputeKeyedFileHash(filePath string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("keyed hash: key must be 32 bytes, got %d", len(key))
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func legacyParseByteSize(size string) (int64, error) {
	size = strings.TrimSpace(size)
	if size == "" {
		return 0, fmt.Errorf("size is empty")
	}
	original := size
	upper := strings.ToUpper(size)
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(upper, "KB"):
		multiplier = 1024
		size = strings.TrimSuffix(upper, "KB")
	case strings.HasSuffix(upper, "MB"):
		multiplier = 1024 * 1024
		size = strings.TrimSuffix(upper, "MB")
	case strings.HasSuffix(upper, "GB"):
		multiplier = 1024 * 1024 * 1024
		size = strings.TrimSuffix(upper, "GB")
	}
	value, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size value %q: not a valid byte size", original)
	}
	if value <= 0 {
		return 0, fmt.Errorf("size %q must be positive", original)
	}
	if multiplier > 1 && value > (math.MaxInt64/multiplier) {
		return 0, fmt.Errorf("size %q too large", original)
	}
	result := value * multiplier
	if result < 0 {
		return 0, fmt.Errorf("size %q too large", original)
	}
	return result, nil
}

func legacyExpandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

func legacyDetectSymlinks(paths []string) ([]SymlinkWarning, error) {
	warnings := make([]SymlinkWarning, 0)
	for _, path := range paths {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve symlinks for %q: %w", path, err)
		}
		if filepath.Clean(resolved) != filepath.Clean(path) {
			warnings = append(warnings, SymlinkWarning{Path: path, Resolved: resolved})
		}
	}
	return warnings, nil
}
