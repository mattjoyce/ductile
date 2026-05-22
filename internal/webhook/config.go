package webhook

import (
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// FromGlobalConfig converts config.WebhooksConfig to webhook.Config.
// Resolves secret references and parses max body sizes. plugins is a snapshot
// of the parent runtime config's plugins map so the webhook ingress can
// resolve per-plugin retry policy at enqueue time (P2-02). Pass nil to skip
// per-plugin retry resolution — the queue's own default then applies.
func FromGlobalConfig(wc *config.WebhooksConfig, tokens map[string]string, plugins map[string]config.PluginConf) (Config, error) {
	if wc == nil {
		return Config{}, fmt.Errorf("webhooks config is nil")
	}

	cfg := Config{
		Listen:    wc.Listen,
		Endpoints: make([]EndpointConfig, len(wc.Endpoints)),
		Plugins:   plugins,
	}

	for i, ep := range wc.Endpoints {
		if ep.SecretRef == "" {
			return Config{}, fmt.Errorf("webhook endpoint %q: secret_ref is required", ep.Path)
		}
		resolvedSecret, ok := tokens[ep.SecretRef]
		if !ok {
			return Config{}, fmt.Errorf("webhook endpoint %q: secret_ref %q not found in tokens", ep.Path, ep.SecretRef)
		}
		secret := resolvedSecret

		// Parse max body size (e.g., "1MB", "2048576")
		maxBodySize, err := parseMaxBodySize(ep.MaxBodySize)
		if err != nil {
			return Config{}, fmt.Errorf("webhook endpoint %q: invalid max_body_size %q: %w", ep.Path, ep.MaxBodySize, err)
		}

		cfg.Endpoints[i] = EndpointConfig{
			Path:            ep.Path,
			Plugin:          ep.Plugin,
			Command:         DefaultCommand, // Can be extended in config if needed
			Secret:          secret,
			SecretRef:       ep.SecretRef,
			SignatureHeader: ep.SignatureHeader,
			MaxBodySize:     maxBodySize,
		}
	}

	return cfg, nil
}

// parseMaxBodySize parses size strings like "1MB", "2048576", "1048576" to bytes.
// Returns DefaultMaxBodySize if empty.
func parseMaxBodySize(size string) (int64, error) {
	if size == "" {
		return DefaultMaxBodySize, nil
	}
	return config.ParseByteSize(size)
}
