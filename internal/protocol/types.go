package protocol

import "time"

// Request represents the protocol v2 request envelope sent to plugins via stdin.
type Request struct {
	Protocol int            `json:"protocol"`
	JobID    string         `json:"job_id"`
	Command  string         `json:"command"` // poll | handle | health | init | custom
	Config   map[string]any `json:"config"`
	// Secrets carries the plugin's composed, authorized secrets, delivered over
	// stdin at spawn (never env, never argv). Distinct from Config: secrets are
	// authorization-scoped per principal, not configuration. Empty/absent when
	// the plugin is not a vault principal.
	Secrets    map[string]string `json:"secrets,omitempty"`
	State      map[string]any    `json:"state"`
	Payload    map[string]any    `json:"payload,omitempty"`
	Context    map[string]any    `json:"context,omitempty"`
	Event      *Event            `json:"event,omitempty"` // only for handle command
	DeadlineAt time.Time         `json:"deadline_at"`
}

// Response represents the protocol v2 response envelope received from plugins via stdout.
type Response struct {
	Status       string         `json:"status"` // ok | error
	Error        string         `json:"error,omitempty"`
	Result       string         `json:"result,omitempty"` // human-readable summary
	Events       []Event        `json:"events,omitempty"`
	StateUpdates map[string]any `json:"state_updates,omitempty"`
	Logs         []LogEntry     `json:"logs,omitempty"`

	// StopwatchSubs carries optional sub-span timing entries the plugin
	// chose to emit. The dispatcher parses defensively and caps at 32 per
	// step; malformed shapes are dropped silently. Plugins that do not
	// instrument internal phases leave this empty.
	StopwatchSubs []map[string]any `json:"ductile_stopwatch_subs,omitempty"`
}

// ResponseCompat carries protocol-v2 compatibility fields that remain on the
// wire but are not part of the core response model.
type ResponseCompat struct {
	Retry *bool
}

// Event represents an event emitted by a plugin or received via webhook.
type Event struct {
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	DedupeKey string         `json:"dedupe_key,omitempty"`

	// Injected by core (not set by plugins)
	Source    string    `json:"source,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	EventID   string    `json:"event_id,omitempty"`
}

// LogEntry represents a log message from a plugin.
type LogEntry struct {
	Level   string `json:"level"` // info | warn | error | debug
	Message string `json:"message"`
}
