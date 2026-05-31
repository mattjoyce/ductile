package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/secrets"
)

// ageKeyEnvVar overrides the configured/default age key file location. It is the
// secret-zero entry point: the one location resolvable before any config is
// parsed, so an encrypted root config remains decryptable.
const ageKeyEnvVar = "DUCTILE_AGE_KEY_FILE"

// resolveKeyring determines the age key file and loads it. Resolution order:
//  1. DUCTILE_AGE_KEY_FILE environment variable (explicit, wins)
//  2. cfg.Secrets.AgeKeyFile config field (interpolated, relative to configDir)
//  3. default locations (<configDir>/age.key, ~/.config/ductile/age.key)
//
// An explicitly-named key (env or config) that cannot be loaded is a hard error
// — the operator asked for it, so a missing/insecure file must not be silently
// ignored. A default location that simply does not exist yields an empty keyring
// (encryption at rest is off), which is the unconfigured default.
func resolveKeyring(configDir string, cfg *Config) (*secrets.Keyring, error) {
	if path := os.Getenv(ageKeyEnvVar); path != "" {
		kr, err := secrets.LoadKeyringFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ageKeyEnvVar, err)
		}
		return kr, nil
	}

	if cfg != nil && cfg.Secrets.AgeKeyFile != "" {
		path := interpolateEnv(cfg.Secrets.AgeKeyFile)
		if !filepath.IsAbs(path) {
			path = filepath.Join(configDir, path)
		}
		kr, err := secrets.LoadKeyringFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("secrets.age_key_file: %w", err)
		}
		return kr, nil
	}

	for _, candidate := range defaultKeyPaths(configDir) {
		if _, err := os.Stat(candidate); err == nil {
			return secrets.LoadKeyringFromFile(candidate)
		}
	}
	return &secrets.Keyring{}, nil
}

// defaultKeyPaths returns the conventional key file locations checked when no
// key is explicitly configured.
func defaultKeyPaths(configDir string) []string {
	paths := []string{filepath.Join(configDir, "age.key")}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "ductile", "age.key"))
	}
	return paths
}

// readConfigBytes reads a config/token file and, if it is age-encrypted,
// decrypts it in memory with the keyring. Plaintext files pass through
// unchanged. Decryption happens here, before any ${ENV} interpolation or YAML
// parsing, so the rest of the loader never sees ciphertext and plaintext is
// never written back to disk.
func readConfigBytes(kr *secrets.Keyring, path string) ([]byte, error) {
	// #nosec G304 -- config paths are operator-controlled local inputs.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !secrets.IsEncrypted(data) {
		return data, nil
	}
	plaintext, err := kr.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt %q: %w", path, err)
	}
	return plaintext, nil
}
