package api

import (
	"encoding/json"
	"time"
)

// TriggerRequest is the JSON body for POST /plugin/{plugin}/{command}.
type TriggerRequest struct {
	Payload json.RawMessage `json:"payload,omitempty"`
}

// TriggerResponse is returned on successful job enqueue
type TriggerResponse struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	Plugin  string `json:"plugin"`
	Command string `json:"command"`
}

// JobStatusResponse is returned by GET /job/{job_id}
type JobStatusResponse struct {
	JobID       string          `json:"job_id"`
	Status      string          `json:"status"`
	Plugin      string          `json:"plugin"`
	Command     string          `json:"command"`
	Result      json.RawMessage `json:"result,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// JobListResponse is returned by GET /jobs.
type JobListResponse struct {
	Jobs  []JobListItem `json:"jobs"`
	Total int           `json:"total"`
}

// JobListItem is one row in GET /jobs.
type JobListItem struct {
	JobID       string     `json:"job_id"`
	Plugin      string     `json:"plugin"`
	Command     string     `json:"command"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Attempt     int        `json:"attempt"`
}

// JobLogListResponse is returned by GET /job-logs.
type JobLogListResponse struct {
	Logs  []JobLogItem `json:"logs"`
	Total int          `json:"total"`
}

// JobLogItem is one row in GET /job-logs.
type JobLogItem struct {
	JobID       string          `json:"job_id"`
	LogID       string          `json:"log_id"`
	Plugin      string          `json:"plugin"`
	Command     string          `json:"command"`
	Status      string          `json:"status"`
	Attempt     int             `json:"attempt"`
	SubmittedBy string          `json:"submitted_by"`
	CreatedAt   time.Time       `json:"created_at"`
	CompletedAt time.Time       `json:"completed_at"`
	LastError   *string         `json:"last_error,omitempty"`
	Stderr      *string         `json:"stderr,omitempty"`
	Result      json.RawMessage `json:"result,omitempty"`
}

// SyncResponse is returned for successful synchronous pipeline executions.
type SyncResponse struct {
	JobID      string          `json:"job_id"`
	Status     string          `json:"status"`
	DurationMs int64           `json:"duration_ms"`
	Result     json.RawMessage `json:"result,omitempty"` // Root job result
	Tree       []JobResultData `json:"tree"`             // All jobs in tree
}

// JobResultData represents a single job's result in a tree.
type JobResultData struct {
	JobID       string          `json:"job_id"`
	ParentJobID *string         `json:"parent_job_id,omitempty"`
	Plugin      string          `json:"plugin"`
	Command     string          `json:"command"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	LastError   *string         `json:"last_error,omitempty"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
}

// TimeoutResponse is returned when a sync pipeline exceeds its timeout.
type TimeoutResponse struct {
	JobID           string `json:"job_id"`
	Status          string `json:"status"`
	TimeoutExceeded bool   `json:"timeout_exceeded"`
	Message         string `json:"message"`
}

// ErrorResponse is returned on errors
type ErrorResponse struct {
	Error string `json:"error"`
}

// BaggageOverlimitResponse is returned with HTTP 413 when a pipeline ingress
// request's baggage would exceed the configured event_context size cap. The
// `bytes_actual` and `bytes_limit` fields give callers a precise hint without
// requiring them to parse the error message (P2-04).
type BaggageOverlimitResponse struct {
	Error       string `json:"error"`
	BytesActual int64  `json:"bytes_actual"`
	BytesLimit  int64  `json:"bytes_limit"`
}

// RootResponse is returned by GET / — a human/agent-readable index of the gateway.
type RootResponse struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	Discovery     map[string]string `json:"discovery"`
}

// HealthzResponse is returned by GET /healthz.
type HealthzResponse struct {
	Status        string `json:"status"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	QueueDepth    int    `json:"queue_depth"`
	PluginsLoaded int    `json:"plugins_loaded"`
	// PluginsCircuitOpen is reserved for circuit breaker observability.
	// MVP: always 0 until circuit breaker state is plumbed into the API server.
	PluginsCircuitOpen int    `json:"plugins_circuit_open"`
	ConfigPath         string `json:"config_path,omitempty"`
	BinaryPath         string `json:"binary_path,omitempty"`
	Version            string `json:"version,omitempty"`
	// Posture is the gateway boot posture ("gateway" or "management-only"). It
	// lets a health probe distinguish a fully-serving gateway from the
	// vault-operable / ductile-closed bootstrap posture (#130).
	Posture string `json:"posture,omitempty"`
}

// PluginListResponse is returned by GET /plugins.
type PluginListResponse struct {
	Plugins []PluginSummary `json:"plugins"`
}

// PluginSummary provides a high-level overview of a plugin.
type PluginSummary struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description,omitempty"`
	Commands    []string `json:"commands"`
}

// ReloadResponse is returned by POST /system/reload.
type ReloadResponse struct {
	Status     string `json:"status"`
	ReloadedAt string `json:"reloaded_at,omitempty"`
	Message    string `json:"message,omitempty"`
}

// SkillsIndexResponse is returned by GET /skills.
type SkillsIndexResponse struct {
	Skills []SkillSummary `json:"skills"`
}

// SkillSummary describes one operator-facing skill entry.
type SkillSummary struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"` // plugin | pipeline
	Description   string `json:"description,omitempty"`
	Endpoint      string `json:"endpoint"`
	Tier          string `json:"tier,omitempty"` // READ | WRITE (plugin skills)
	Plugin        string `json:"plugin,omitempty"`
	Command       string `json:"command,omitempty"`
	Pipeline      string `json:"pipeline,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	ExecutionMode string `json:"execution_mode,omitempty"` // asynchronous | synchronous
	TimeoutSecs   int64  `json:"timeout_secs,omitempty"`
}

// PluginDetailResponse is returned by GET /plugin/{name}.
type PluginDetailResponse struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description,omitempty"`
	Protocol    int             `json:"protocol"`
	Commands    []PluginCommand `json:"commands"`
}

// PluginCommand describes a single command supported by a plugin.
type PluginCommand struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Description  string `json:"description,omitempty"`
	InputSchema  any    `json:"input_schema,omitempty"`
	OutputSchema any    `json:"output_schema,omitempty"`
}

// SchedulerJobsResponse is returned by GET /scheduler/jobs.
type SchedulerJobsResponse struct {
	Jobs []SchedulerJob `json:"jobs"`
}

// SchedulerJob is one scheduler entry preview row.
type SchedulerJob struct {
	Plugin     string     `json:"plugin"`
	ScheduleID string     `json:"schedule_id"`
	Command    string     `json:"command"`
	Mode       string     `json:"mode"` // every | cron | at | after
	Status     string     `json:"status"`
	Reason     string     `json:"reason,omitempty"`
	Timezone   string     `json:"timezone,omitempty"`
	NextRunAt  *time.Time `json:"next_run_at,omitempty"`
}
