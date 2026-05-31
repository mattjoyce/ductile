package config

import (
	"fmt"

	"github.com/mattjoyce/ductile/internal/secrets"
	"gopkg.in/yaml.v3"
)

// graftTokens loads token entries from tokens.yaml into cfg, decrypting the file
// first if it is age-encrypted.
func graftTokens(cfg *Config, path string, kr *secrets.Keyring) error {
	data, err := readConfigBytes(kr, path)
	if err != nil {
		return fmt.Errorf("failed to read tokens.yaml: %w", err)
	}
	interpolated := interpolateEnv(string(data))

	var tf TokensFileConfig
	if err := yaml.Unmarshal([]byte(interpolated), &tf); err != nil {
		return fmt.Errorf("failed to parse tokens.yaml: %w", err)
	}
	cfg.Tokens = append(cfg.Tokens, tf.Tokens...)
	return nil
}
