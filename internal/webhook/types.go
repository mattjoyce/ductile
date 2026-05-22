package webhook

import (
	"context"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/queue"
)

// JobQueuer defines the interface for enqueueing webhook-triggered jobs.
type JobQueuer interface {
	Enqueue(ctx context.Context, req queue.EnqueueRequest) (string, error)
}

// Config holds webhook server configuration. Plugins is a snapshot of plugin
// retry config (keyed by plugin name) populated by FromGlobalConfig so the
// webhook ingress can stamp operator-configured MaxAttempts onto each enqueued
// job. Rebuilt on config reload — never stale. P2-02.
type Config struct {
	Listen    string                       `yaml:"listen"`
	Endpoints []EndpointConfig             `yaml:"endpoints"`
	Plugins   map[string]config.PluginConf `yaml:"-"`
}

// EndpointConfig defines a single webhook endpoint.
type EndpointConfig struct {
	// Path is the URL path for this webhook (e.g., "/webhook/github")
	Path string `yaml:"path"`

	// Plugin is the target plugin name
	Plugin string `yaml:"plugin"`

	// Command is the plugin command to execute (default: "handle")
	Command string `yaml:"command"`

	// Secret is the resolved HMAC secret for signature verification.
	Secret string `yaml:"-"`

	// SecretRef references a secret in tokens.yaml
	SecretRef string `yaml:"secret_ref,omitempty"`

	// SignatureHeader is the HTTP header containing the HMAC signature
	// Examples: "X-Hub-Signature-256" (GitHub), "X-Gitlab-Token" (GitLab)
	SignatureHeader string `yaml:"signature_header"`

	// MaxBodySize is the maximum allowed request body size in bytes (default: 1MB)
	MaxBodySize int64 `yaml:"max_body_size,omitempty"`
}

// TriggerResponse is the JSON response for successful webhook triggers.
type TriggerResponse struct {
	JobID string `json:"job_id"`
}

// ErrorResponse is the JSON response for webhook errors.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Default values
const (
	DefaultMaxBodySize = 1048576 // 1 MB
	DefaultCommand     = "handle"
)
