package config

import (
	"runtime"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the complete ductile configuration.
type Config struct {
	Include         []string               `yaml:"include,omitempty"` // Multi-file mode: files to merge
	EnvironmentVars EnvironmentVarsConfig  `yaml:"environment_vars,omitempty"`
	Service         ServiceConfig          `yaml:"service"`
	State           StateConfig            `yaml:"state"`
	Database        StateConfig            `yaml:"database,omitempty"` // Alias for user intuition
	Secrets         SecretsConfig          `yaml:"secrets,omitempty"`
	API             APIConfig              `yaml:"api,omitempty"`
	PluginRoots     []string               `yaml:"plugin_roots,omitempty"`
	TCCPaths        []string               `yaml:"tcc_paths,omitempty"` // macOS-only: paths stat()-ed on cold start to surface TCC popups synchronously
	Plugins         map[string]PluginConf  `yaml:"plugins"`
	Accounts        map[string]AccountConf `yaml:"accounts,omitempty"` // privsep: account name -> uid/gid/state_dir (tracer #92; validated/generalized in #84)
	RelayInstances  []RelayInstanceConfig  `yaml:"instances,omitempty"`
	RemoteIngress   *RemoteIngressConfig   `yaml:"remote_ingress,omitempty"`
	Routes          []RouteConfig          `yaml:"routes,omitempty"`   // Not in MVP
	Webhooks        *WebhooksConfig        `yaml:"webhooks,omitempty"` // Not in MVP
	SourceFiles     map[string]*yaml.Node  `yaml:"-"`                  // Physical files tracked for updates
	ResolvedSecrets map[string]string      `yaml:"-"`                  // name->value, projected from the vault owner at load (epic #48)
	Pipelines       []PipelineEntry        `yaml:"-"`                  // Directory mode: pipeline entries
	ConfigDir       string                 `yaml:"-"`                  // Directory mode: root config directory
}

// SecretsConfig defines encryption-at-rest settings. AgeKeyFile names the age
// identity (private key) file used to decrypt encrypted config/token includes
// at load. It is overridden by the DUCTILE_AGE_KEY_FILE environment variable.
// When unset and no default key file exists, encryption at rest is simply off.
type SecretsConfig struct {
	AgeKeyFile string `yaml:"age_key_file,omitempty"`
	// VaultFile names the age-encrypted vault blob (the owned secret store).
	// Relative paths resolve against the config dir; empty uses the default
	// (<configDir>/vault.age). Absent file = no vault yet (coexistence window).
	VaultFile string `yaml:"vault_file,omitempty"`
}

// EnvironmentVarsConfig defines env file includes for interpolation.
type EnvironmentVarsConfig struct {
	Include []string `yaml:"include,omitempty"`
}

// ServiceConfig defines core service settings.
type ServiceConfig struct {
	Name                        string        `yaml:"name"`
	TickInterval                time.Duration `yaml:"tick_interval"`
	LogLevel                    string        `yaml:"log_level"`
	LogFormat                   string        `yaml:"log_format"`
	DedupeTTL                   time.Duration `yaml:"dedupe_ttl"`
	JobLogRetention             time.Duration `yaml:"job_log_retention"`
	JobQueueRetention           time.Duration `yaml:"job_queue_retention"`
	JobTransitionsRetention     time.Duration `yaml:"job_transitions_retention"`
	JobAttemptsRetention        time.Duration `yaml:"job_attempts_retention"`
	BreakerTransitionsRetention time.Duration `yaml:"breaker_transitions_retention"`
	MaxWorkers                  int           `yaml:"max_workers,omitempty"`
	// PluginEnvPassthrough lists environment variable names granted to plugin
	// children on top of the built-in spawn-hygiene allowlist. Use sparingly:
	// every name here is one the child sees from the gateway's environment.
	PluginEnvPassthrough []string `yaml:"plugin_env_passthrough,omitempty"`
	// Admission decomplects the four independent admission-control policies that
	// strict_mode used to bundle. When present it is authoritative; when absent
	// the deprecated StrictMode alias is consulted (see AdmissionPolicy).
	Admission *AdmissionConfig `yaml:"admission,omitempty"`
	// StrictMode is the DEPRECATED bundled switch. strict_mode: true is an alias
	// that enables all four admission policies; prefer the explicit admission
	// block. Retained for back-compat (a coexistence window, like tokens.yaml).
	StrictMode    bool `yaml:"strict_mode"`
	AllowSymlinks bool `yaml:"allow_symlinks"`
	// HookMaxDepth caps the on-hook lifecycle chain depth. A root job that fires
	// a hook produces a depth-1 hook job; if that hook job itself fires a hook
	// (because its plugin has notify_on_complete: true), the next would-be hook
	// would be depth 2, and so on. Enqueue refuses any hook beyond this cap.
	// Set to 0 to use the default (DefaultHookMaxDepth). Negative is rejected
	// by config validation. P2-11.
	HookMaxDepth int `yaml:"hook_max_depth,omitempty"`
	// Unconfined is the privsep boot-gate escape hatch (PrivSec ADR §5). When true,
	// the gateway runs plugins at its own uid (no drop) even on a host that holds the
	// drop capability and configures accounts — the one explicit, audited way to opt
	// out of enforcement. The boot gate logs it loudly. Default false: capability and
	// accounts-configured must agree or the gateway refuses to start.
	Unconfined bool `yaml:"unconfined,omitempty"`
}

// AdmissionConfig is the decomplected set of admission-control policies that the
// daemon applies when admitting a config — at boot and at reload. Each field is
// an independent gate; the old strict_mode boolean welded all four together.
type AdmissionConfig struct {
	// VerifyIntegrityOnBoot runs the .checksums + plugin-fingerprint preflight at
	// startup. (Reload always verifies integrity regardless of this flag.)
	VerifyIntegrityOnBoot bool `yaml:"verify_integrity_on_boot"`
	// FailOnDrift promotes operational config/routes drift (otherwise warnings)
	// to admission failures — at both boot and reload.
	FailOnDrift bool `yaml:"fail_on_drift"`
	// ValidateConfigOnBoot requires doctor.Validate() to pass at startup.
	ValidateConfigOnBoot bool `yaml:"validate_config_on_boot"`
	// RequireAPIAuth rejects an enabled API that has no auth tokens configured.
	RequireAPIAuth bool `yaml:"require_api_auth"`
}

// AdmissionPolicy resolves the effective admission policy. An explicit admission
// block is authoritative. Otherwise the deprecated strict_mode alias maps to
// all-policies-on; with neither set, every policy is off (today's zero-value
// default — a permissive deployment).
func (s ServiceConfig) AdmissionPolicy() AdmissionConfig {
	if s.Admission != nil {
		return *s.Admission
	}
	if s.StrictMode {
		return AdmissionConfig{
			VerifyIntegrityOnBoot: true,
			FailOnDrift:           true,
			ValidateConfigOnBoot:  true,
			RequireAPIAuth:        true,
		}
	}
	return AdmissionConfig{}
}

// StrictModeDeprecationWarning returns a non-empty operator warning when the
// deprecated strict_mode field is in use, or "" when it is not. A co-present
// admission block means strict_mode is being silently superseded — say so.
func (s ServiceConfig) StrictModeDeprecationWarning() string {
	if !s.StrictMode {
		return ""
	}
	if s.Admission != nil {
		return "service.strict_mode is deprecated and ignored because service.admission is set; remove strict_mode"
	}
	return "service.strict_mode is deprecated; replace it with an explicit service.admission block"
}

// StateConfig defines state storage settings.
type StateConfig struct {
	Path string `yaml:"path"`
}

// APIConfig defines HTTP API server settings.
type APIConfig struct {
	Enabled           bool          `yaml:"enabled"`
	Listen            string        `yaml:"listen"`
	Auth              APIAuthConfig `yaml:"auth"`
	MaxConcurrentSync int           `yaml:"max_concurrent_sync,omitempty"`
	MaxSyncTimeout    time.Duration `yaml:"max_sync_timeout,omitempty"`
	// AllowedOrigins lists origins that may receive credentialed CORS headers.
	// Empty (the default) disables cross-origin credential sharing entirely.
	AllowedOrigins []string `yaml:"allowed_origins,omitempty"`
}

// APIAuthConfig defines API authentication settings.
type APIAuthConfig struct {
	Tokens []APIToken `yaml:"tokens,omitempty"`
}

// APIToken defines a bearer token and its scopes.
type APIToken struct {
	Token  string   `yaml:"token"`
	Scopes []string `yaml:"scopes"`
}

// AccountConf is the unprivileged OS identity a plugin is dropped to at spawn
// (PrivSec ADR §5). The map key is the account name a plugin's `run_as:` grant
// references. Tracer (#92) parses the minimal shape; validation (positive
// uid/gid, absolute state_dir, no duplicate uid) and the two-tier defaults land
// in #84. UID/GID are the OS numeric identities; the daemon never creates the
// account — the deploy layer provisions it (sysusers.d / image, ADR §5 Q4).
type AccountConf struct {
	UID      int    `yaml:"uid"`
	GID      int    `yaml:"gid"`
	StateDir string `yaml:"state_dir,omitempty"`
}

// PluginConf defines configuration for a single plugin.
type PluginConf struct {
	Enabled bool   `yaml:"enabled"`
	Uses    string `yaml:"uses,omitempty"`
	// RunAs names the privsep account this plugin is granted (PrivSec ADR §4: the
	// operator's core config grants privilege; a manifest hint is never trusted).
	// Empty = no grant. Resolution to an account identity is #85; the tracer (#92)
	// honours it for the single granted plugin.
	RunAs string `yaml:"run_as,omitempty"`
	// RequiresVault makes vault secret delivery mandatory for this plugin. When
	// true, an unknown/unregistered vault principal (the plugin name) fails the
	// spawn CLOSED rather than opting out — closing the fail-open seam where a
	// misnamed/unregistered principal would silently run the plugin with no
	// secrets. Default false preserves the coexistence opt-out for keyless plugins.
	RequiresVault       bool                  `yaml:"requires_vault,omitempty"`
	Schedule            *ScheduleConfig       `yaml:"schedule,omitempty"` // Deprecated: use schedules.
	Schedules           []ScheduleConfig      `yaml:"schedules,omitempty"`
	Config              map[string]any        `yaml:"config,omitempty"`
	Retry               *RetryConfig          `yaml:"retry,omitempty"`
	Timeouts            *TimeoutsConfig       `yaml:"timeouts,omitempty"`
	CircuitBreaker      *CircuitBreakerConfig `yaml:"circuit_breaker,omitempty"`
	MaxOutstandingPolls int                   `yaml:"max_outstanding_polls,omitempty"`
	Parallelism         int                   `yaml:"parallelism,omitempty"`
	NotifyOnComplete    *bool                 `yaml:"notify_on_complete,omitempty"` // opt-in to on-hook lifecycle signals; nil = false
}

// ScheduleConfig defines when a plugin command should be scheduled.
type ScheduleConfig struct {
	ID              string           `yaml:"id,omitempty"`
	Every           string           `yaml:"every,omitempty"` // e.g., "5m", "hourly", "daily"
	Cron            string           `yaml:"cron,omitempty"`  // standard 5-field cron expression
	At              string           `yaml:"at,omitempty"`    // one-shot RFC3339 timestamp
	After           time.Duration    `yaml:"after,omitempty"` // one-shot delay from service start
	Jitter          time.Duration    `yaml:"jitter,omitempty"`
	CatchUp         string           `yaml:"catch_up,omitempty"`         // skip|run_once|run_all (every schedules)
	IfRunning       string           `yaml:"if_running,omitempty"`       // skip|queue|cancel
	OnlyBetween     string           `yaml:"only_between,omitempty"`     // "HH:MM-HH:MM"
	Timezone        string           `yaml:"timezone,omitempty"`         // IANA timezone name
	NotOn           []any            `yaml:"not_on,omitempty"`           // weekday names (mon) or ints (0-6, 7=sun)
	Command         string           `yaml:"command,omitempty"`          // default: "poll"
	Payload         map[string]any   `yaml:"payload,omitempty"`          // default: {}
	PreferredWindow *PreferredWindow `yaml:"preferred_window,omitempty"` // Not in MVP
}

// NormalizedSchedules returns the schedule list with defaults applied.
func (p PluginConf) NormalizedSchedules() []ScheduleConfig {
	if len(p.Schedules) == 0 {
		return nil
	}

	out := make([]ScheduleConfig, 0, len(p.Schedules))
	for _, s := range p.Schedules {
		entry := s.copy()
		if strings.TrimSpace(entry.ID) == "" {
			entry.ID = "default"
		}
		entry.applyDefaults()
		out = append(out, entry)
	}
	return out
}

// applyDefaults applies per-entry defaults in-place.
func (s *ScheduleConfig) applyDefaults() {
	if strings.TrimSpace(s.Command) == "" {
		s.Command = "poll"
	}
	if strings.TrimSpace(s.CatchUp) == "" {
		s.CatchUp = "skip"
	}
	if strings.TrimSpace(s.IfRunning) == "" {
		s.IfRunning = "skip"
	}
	if s.Payload == nil {
		s.Payload = map[string]any{}
	}
}

func (s ScheduleConfig) copy() ScheduleConfig {
	copied := s
	if s.Payload != nil {
		copied.Payload = make(map[string]any, len(s.Payload))
		for k, v := range s.Payload {
			copied.Payload[k] = v
		}
	}
	if s.NotOn != nil {
		copied.NotOn = append([]any(nil), s.NotOn...)
	}
	return copied
}

// PreferredWindow defines time-of-day constraints for scheduling.
type PreferredWindow struct {
	Start string `yaml:"start"` // e.g., "06:00"
	End   string `yaml:"end"`   // e.g., "22:00"
}

// RetryConfig defines retry behavior for failed jobs.
type RetryConfig struct {
	MaxAttempts int           `yaml:"max_attempts"`
	BackoffBase time.Duration `yaml:"backoff_base"`
}

// TimeoutsConfig defines command-specific timeouts.
//
// Poll/Handle/Health/Init are the four core lifecycle commands every plugin
// implements; their fixed fields keep YAML keys discoverable and validation
// strict. Overrides captures operator-defined per-command timeouts (e.g.
// plugins.stress.timeouts.cpu: 15s) so plugin-specific command names declared
// in the manifest are also enforced rather than silently demoted to a default.
// The yaml:",inline" tag means keys not matched by the four core fields fall
// into Overrides verbatim. P2-05.
type TimeoutsConfig struct {
	Poll      time.Duration            `yaml:"poll"`
	Handle    time.Duration            `yaml:"handle"`
	Health    time.Duration            `yaml:"health,omitempty"`
	Init      time.Duration            `yaml:"init,omitempty"`
	Overrides map[string]time.Duration `yaml:",inline,omitempty"`
}

// ResolvedPluginConf is the single resolver for a plugin's effective
// retry / timeout / circuit-breaker / parallelism values. It applies the one
// "value if set (> 0) else DefaultPluginConf()" rule in exactly one place, so the
// runtime hot paths (dispatcher getTimeout/computeRetryDelay, scheduler
// breakerThreshold/breakerResetAfter), the enqueue path (MaxAttemptsForPlugin),
// and the `config show --effective` view (EffectivePluginConf) all read the same
// resolution. Adding a field or changing a default can no longer make one site
// silently disagree with another (#75).
//
// Build one with ResolvePluginConf(raw, maxWorkers). Defaults are single-sourced
// from DefaultPluginConf; maxWorkers (service.max_workers) is the default for
// parallelism, matching mergePluginDefaults.
type ResolvedPluginConf struct {
	raw        PluginConf
	def        PluginConf
	maxWorkers int
}

// ResolvePluginConf builds a resolver over a RAW (un-merged) plugin config.
// maxWorkers is only consulted by Parallelism; pass 0 when resolving fields that
// do not need it.
func ResolvePluginConf(raw PluginConf, maxWorkers int) ResolvedPluginConf {
	return ResolvedPluginConf{raw: raw, def: DefaultPluginConf(), maxWorkers: maxWorkers}
}

// MaxAttempts resolves retry.max_attempts: the plugin value when set (> 0),
// otherwise the global default.
func (r ResolvedPluginConf) MaxAttempts() int {
	if r.raw.Retry != nil && r.raw.Retry.MaxAttempts > 0 {
		return r.raw.Retry.MaxAttempts
	}
	return r.def.Retry.MaxAttempts
}

// BackoffBase resolves retry.backoff_base: the plugin value when set (> 0),
// otherwise the global default.
func (r ResolvedPluginConf) BackoffBase() time.Duration {
	if r.raw.Retry != nil && r.raw.Retry.BackoffBase > 0 {
		return r.raw.Retry.BackoffBase
	}
	return r.def.Retry.BackoffBase
}

// Timeout resolves the effective timeout for a single command. Resolution order
// mirrors the runtime dispatcher: an operator Overrides entry (> 0) wins; then the
// matching core field (poll/handle/health/init) when set (> 0); otherwise the
// per-command default. Unknown commands fall back to the default poll timeout.
// A nil Timeouts block is treated as the default block.
func (r ResolvedPluginConf) Timeout(command string) time.Duration {
	timeouts := r.raw.Timeouts
	if timeouts == nil {
		timeouts = r.def.Timeouts
	}
	if override, ok := timeouts.Overrides[command]; ok && override > 0 {
		return override
	}
	def := r.def.Timeouts
	switch command {
	case "poll":
		if timeouts.Poll > 0 {
			return timeouts.Poll
		}
		return def.Poll
	case "handle":
		if timeouts.Handle > 0 {
			return timeouts.Handle
		}
		return def.Handle
	case "health":
		if timeouts.Health > 0 {
			return timeouts.Health
		}
		return def.Health
	case "init":
		if timeouts.Init > 0 {
			return timeouts.Init
		}
		return def.Init
	default:
		return def.Poll
	}
}

// BreakerThreshold resolves circuit_breaker.threshold: the plugin value when set
// (> 0), otherwise the global default.
func (r ResolvedPluginConf) BreakerThreshold() int {
	if r.raw.CircuitBreaker == nil || r.raw.CircuitBreaker.Threshold <= 0 {
		return r.def.CircuitBreaker.Threshold
	}
	return r.raw.CircuitBreaker.Threshold
}

// BreakerResetAfter resolves circuit_breaker.reset_after: the plugin value when
// set (> 0), otherwise the global default.
func (r ResolvedPluginConf) BreakerResetAfter() time.Duration {
	if r.raw.CircuitBreaker == nil || r.raw.CircuitBreaker.ResetAfter <= 0 {
		return r.def.CircuitBreaker.ResetAfter
	}
	return r.raw.CircuitBreaker.ResetAfter
}

// Parallelism resolves the plugin's worker fan-out: the plugin value when set
// (> 0), otherwise service.max_workers (the mergePluginDefaults default). This is
// the base value before the dispatcher applies the manifest concurrency_safe clamp.
func (r ResolvedPluginConf) Parallelism() int {
	if r.raw.Parallelism > 0 {
		return r.raw.Parallelism
	}
	return r.maxWorkers
}

// MaxAttemptsForPlugin returns the retry max-attempts value to stamp into an
// EnqueueRequest for jobs targeting the given plugin: the plugin-level
// Retry.MaxAttempts when configured (> 0), otherwise DefaultPluginConf()'s value.
//
// All non-scheduler enqueue paths (API direct/pipeline, webhook ingress, relay
// ingress, internal routed/hook dispatch) must call this so the operator's
// configured retry policy is honored uniformly. P2-02.
func MaxAttemptsForPlugin(pluginConf PluginConf) int {
	return ResolvePluginConf(pluginConf, 0).MaxAttempts()
}

// Provenance tags for an effective config value (see EffectivePluginConf).
const (
	SourceExplicit = "explicit" // value set in the operator's config file
	SourceDefault  = "default"  // value inherited from DefaultPluginConf (code)
)

// EffectivePluginConf resolves a RAW (un-merged) plugin config into the values
// actually in force at runtime, plus a per-field provenance map (field path →
// SourceExplicit|SourceDefault). It mirrors the scattered runtime resolvers
// field-by-field — MaxAttemptsForPlugin, dispatcher getTimeout/computeRetryDelay/
// pluginParallelism, scheduler breakerThreshold/breakerResetAfter — so the view
// matches what runs: a value is explicit when set (> 0) in the file, otherwise it
// is the DefaultPluginConf value. Defaults are single-sourced from
// DefaultPluginConf so this never re-hardcodes them (#71). maxWorkers is
// service.max_workers, the default for parallelism (mergePluginDefaults semantics).
//
// Pass the raw conf (e.g. from LoadRaw); passing an already-merged conf would
// report inherited block values as explicit.
func EffectivePluginConf(raw PluginConf, maxWorkers int) (PluginConf, map[string]string) {
	def := DefaultPluginConf()
	res := ResolvePluginConf(raw, maxWorkers)
	src := make(map[string]string)
	out := raw

	// Resolved VALUES come from the shared ResolvedPluginConf so the view can never
	// drift from the runtime resolution. The view only adds PROVENANCE: a field is
	// explicit when set (> 0) in the raw config, otherwise default (#75).
	mark := func(key string, explicit bool) {
		if explicit {
			src[key] = SourceExplicit
		} else {
			src[key] = SourceDefault
		}
	}

	// enabled is operator-declared by listing the plugin stanza.
	src["enabled"] = SourceExplicit

	var rawRetry RetryConfig
	if raw.Retry != nil {
		rawRetry = *raw.Retry
	}
	out.Retry = &RetryConfig{
		MaxAttempts: res.MaxAttempts(),
		BackoffBase: res.BackoffBase(),
	}
	mark("retry.max_attempts", rawRetry.MaxAttempts > 0)
	mark("retry.backoff_base", rawRetry.BackoffBase > 0)

	var rawTimeouts TimeoutsConfig
	if raw.Timeouts != nil {
		rawTimeouts = *raw.Timeouts
	}
	effTimeouts := &TimeoutsConfig{
		Poll:   res.Timeout("poll"),
		Handle: res.Timeout("handle"),
		Health: res.Timeout("health"),
		Init:   res.Timeout("init"),
	}
	mark("timeouts.poll", rawTimeouts.Poll > 0)
	mark("timeouts.handle", rawTimeouts.Handle > 0)
	mark("timeouts.health", rawTimeouts.Health > 0)
	mark("timeouts.init", rawTimeouts.Init > 0)
	if len(rawTimeouts.Overrides) > 0 {
		effTimeouts.Overrides = make(map[string]time.Duration, len(rawTimeouts.Overrides))
		for cmd, d := range rawTimeouts.Overrides {
			effTimeouts.Overrides[cmd] = d
			src["timeouts."+cmd] = SourceExplicit
		}
	}
	out.Timeouts = effTimeouts

	var rawCB CircuitBreakerConfig
	if raw.CircuitBreaker != nil {
		rawCB = *raw.CircuitBreaker
	}
	out.CircuitBreaker = &CircuitBreakerConfig{
		Threshold:  res.BreakerThreshold(),
		ResetAfter: res.BreakerResetAfter(),
	}
	mark("circuit_breaker.threshold", rawCB.Threshold > 0)
	mark("circuit_breaker.reset_after", rawCB.ResetAfter > 0)

	if raw.MaxOutstandingPolls > 0 {
		out.MaxOutstandingPolls = raw.MaxOutstandingPolls
	} else {
		out.MaxOutstandingPolls = def.MaxOutstandingPolls
	}
	mark("max_outstanding_polls", raw.MaxOutstandingPolls > 0)

	// Parallelism's default is service.max_workers, not DefaultPluginConf().Parallelism
	// — this matches mergePluginDefaults, the value the dispatcher actually sees.
	out.Parallelism = res.Parallelism()
	mark("parallelism", raw.Parallelism > 0)

	return out, src
}

// CircuitBreakerConfig defines circuit breaker settings.
type CircuitBreakerConfig struct {
	Threshold  int           `yaml:"threshold"`
	ResetAfter time.Duration `yaml:"reset_after"`
}

// RouteConfig defines event routing between plugins.
type RouteConfig struct {
	From      string `yaml:"from"`
	EventType string `yaml:"event_type"`
	To        string `yaml:"to"`
}

// RelayInstanceConfig defines one named outbound relay target.
type RelayInstanceConfig struct {
	Name        string        `yaml:"name"`
	Enabled     bool          `yaml:"enabled"`
	BaseURL     string        `yaml:"base_url"`
	IngressPath string        `yaml:"ingress_path"`
	SecretRef   string        `yaml:"secret_ref"`
	KeyID       string        `yaml:"key_id,omitempty"`
	Timeout     time.Duration `yaml:"timeout,omitempty"`
	Allow       []string      `yaml:"allow,omitempty"`
}

// RemoteIngressConfig defines trusted inbound relay peer policy.
type RemoteIngressConfig struct {
	ListenPath       string            `yaml:"listen_path"`
	MaxBodySize      string            `yaml:"max_body_size,omitempty"`
	AllowedClockSkew time.Duration     `yaml:"allowed_clock_skew,omitempty"`
	RequireKeyID     bool              `yaml:"require_key_id,omitempty"`
	TrustedPeers     []RelayPeerConfig `yaml:"peers"`
}

// RelayPeerConfig defines one trusted inbound relay peer.
type RelayPeerConfig struct {
	Name      string            `yaml:"name"`
	Enabled   bool              `yaml:"enabled"`
	SecretRef string            `yaml:"secret_ref"`
	KeyID     string            `yaml:"key_id,omitempty"`
	Accept    []string          `yaml:"accept,omitempty"`
	Baggage   RelayBaggageRules `yaml:"baggage,omitempty"`
}

// RelayBaggageRules defines which remote baggage keys may seed local root context.
type RelayBaggageRules struct {
	Allow []string `yaml:"allow,omitempty"`
}

// WebhooksConfig defines webhook listener settings.
type WebhooksConfig struct {
	Listen    string            `yaml:"listen"`
	Endpoints []WebhookEndpoint `yaml:"endpoints"`
}

// WebhookEndpoint defines a single webhook endpoint.
type WebhookEndpoint struct {
	Name            string `yaml:"name,omitempty"` // Directory mode: endpoint name
	Path            string `yaml:"path"`
	Plugin          string `yaml:"plugin"`
	SecretRef       string `yaml:"secret_ref,omitempty"`
	SignatureHeader string `yaml:"signature_header"`
	MaxBodySize     string `yaml:"max_body_size"`
}

// ChecksumManifest stores BLAKE3 hashes for scope files (webhooks.yaml)
// and plugin identity fingerprints (manifest.yaml + entrypoint bytes) for each
// configured plugin when the operator runs `ductile config lock`.
type ChecksumManifest struct {
	Version            int                 `yaml:"version"`
	GeneratedAt        string              `yaml:"generated_at"`
	Hashes             map[string]string   `yaml:"hashes"` // filename -> BLAKE3 hash
	PluginFingerprints []PluginFingerprint `yaml:"plugin_fingerprints,omitempty"`
}

// PluginFingerprint records the authorized identity of a configured plugin at
// lock time. Manifest and entrypoint bytes are hashed with BLAKE3. Paths are
// stored post-symlink resolution (matching the plugin loader's trust policy).
// Aliases (config `uses:` key) record the alias Name together with the base
// plugin's paths, and Uses carries the base plugin name; non-aliases have an
// empty Uses field.
type PluginFingerprint struct {
	Name                   string `yaml:"name"`
	Enabled                bool   `yaml:"enabled"`
	Uses                   string `yaml:"uses,omitempty"`
	ManifestPath           string `yaml:"manifest_path"`
	ManifestResolvedPath   string `yaml:"manifest_resolved_path,omitempty"`
	ManifestHash           string `yaml:"manifest_hash"`
	EntrypointPath         string `yaml:"entrypoint_path"`
	EntrypointResolvedPath string `yaml:"entrypoint_resolved_path,omitempty"`
	EntrypointHash         string `yaml:"entrypoint_hash"`
}

// PluginsFileConfig is the structure of plugins.yaml.
type PluginsFileConfig struct {
	Plugins map[string]PluginConf `yaml:"plugins"`
}

// RoutesFileConfig is the structure of routes.yaml.
type RoutesFileConfig struct {
	Routes []RouteConfig `yaml:"routes"`
}

// RelayInstancesFileConfig wraps outbound relay instances for standalone relay-instances.yaml.
type RelayInstancesFileConfig struct {
	Instances []RelayInstanceConfig `yaml:"instances"`
}

// RelayIngressFileConfig wraps inbound relay policy for standalone relay-ingress.yaml.
type RelayIngressFileConfig struct {
	RemoteIngress RemoteIngressConfig `yaml:"remote_ingress"`
}

// WebhooksFileConfig wraps webhook endpoints for standalone webhooks.yaml.
// It accepts the documented nested form:
//
//	webhooks:
//	  endpoints: [...]
//
// and also preserves compatibility with the older flat form:
//
//	webhooks: [...]
type WebhooksFileConfig struct {
	Webhooks WebhookEndpoints `yaml:"webhooks"`
}

// WebhookEndpoints supports both nested and legacy flat standalone webhooks.yaml shapes.
type WebhookEndpoints []WebhookEndpoint

// UnmarshalYAML accepts either:
//   - webhooks: [{...}]
//   - webhooks: { endpoints: [{...}] }
func (w *WebhookEndpoints) UnmarshalYAML(value *yaml.Node) error {
	var flat []WebhookEndpoint
	if err := value.Decode(&flat); err == nil {
		*w = flat
		return nil
	}

	var nested struct {
		Endpoints []WebhookEndpoint `yaml:"endpoints"`
	}
	if err := value.Decode(&nested); err != nil {
		return err
	}
	*w = nested.Endpoints
	return nil
}

// MarshalYAML writes the documented nested standalone form.
func (w WebhookEndpoints) MarshalYAML() (any, error) {
	return struct {
		Endpoints []WebhookEndpoint `yaml:"endpoints"`
	}{Endpoints: []WebhookEndpoint(w)}, nil
}

// ExecutionMode defines how a pipeline should be triggered and its results returned.
type ExecutionMode string

const (
	// ExecutionModeAsync returns 202 immediately.
	ExecutionModeAsync ExecutionMode = "async"
	// ExecutionModeSync blocks until the pipeline completes or times out.
	ExecutionModeSync ExecutionMode = "synchronous"
)

// PipelineEntry defines a named pipeline triggered by an event type.
type PipelineEntry struct {
	Name          string        `yaml:"name"`
	On            string        `yaml:"on"`
	Steps         []StepEntry   `yaml:"steps,omitempty"`
	ExecutionMode ExecutionMode `yaml:"execution_mode,omitempty"`
	Timeout       time.Duration `yaml:"timeout,omitempty"`
}

// StepEntry is a single step in a pipeline.
type StepEntry struct {
	ID      string            `yaml:"id,omitempty"`
	Uses    string            `yaml:"uses,omitempty"`
	Call    string            `yaml:"call,omitempty"`
	Relay   *RelayStepEntry   `yaml:"relay,omitempty"`
	Steps   []StepEntry       `yaml:"steps,omitempty"`
	Split   []StepEntry       `yaml:"split,omitempty"`
	With    map[string]string `yaml:"with,omitempty"`
	Baggage map[string]string `yaml:"baggage,omitempty"`
}

// RelayStepEntry is a first-class remote relay step in pipeline config views.
type RelayStepEntry struct {
	To        string            `yaml:"to" json:"to"`
	Event     string            `yaml:"event" json:"event"`
	DedupeKey string            `yaml:"dedupe_key,omitempty" json:"dedupe_key,omitempty"`
	With      map[string]string `yaml:"with,omitempty" json:"with,omitempty"`
	Baggage   map[string]string `yaml:"baggage,omitempty" json:"baggage,omitempty"`
}

// PipelinesFileConfig wraps pipeline entries for standalone pipelines/*.yaml.
type PipelinesFileConfig struct {
	Pipelines []PipelineEntry `yaml:"pipelines"`
}

// IntegrityTier classifies files by security sensitivity.
type IntegrityTier int

const (
	// TierOperational files warn on mismatch but allow loading.
	TierOperational IntegrityTier = iota
	// TierHighSecurity files hard-fail on mismatch.
	TierHighSecurity
)

// IntegrityResult captures the outcome of integrity verification.
type IntegrityResult struct {
	Passed   bool
	Warnings []string
	Errors   []string
}

// ConfigFiles represents the discovered file manifest for directory mode.
type ConfigFiles struct {
	Root           string   // Config directory root (absolute)
	Config         string   // config.yaml path (absolute)
	Plugins        []string // plugins/*.yaml paths (absolute, sorted)
	Pipelines      []string // pipelines/*.yaml paths (absolute, sorted)
	Webhooks       string   // webhooks.yaml path (absolute, empty if missing)
	Routes         string   // routes.yaml path (absolute, empty if missing)
	RelayInstances string   // relay-instances.yaml path (absolute, empty if missing)
	RelayIngress   string   // relay-ingress.yaml path (absolute, empty if missing)
	Scopes         []string // scopes/*.json paths (absolute, sorted)
}

// FileTier returns the integrity tier for a given file path.
func (cf *ConfigFiles) FileTier(path string) IntegrityTier {
	if path == cf.Webhooks {
		return TierHighSecurity
	}
	if slices.Contains(cf.Scopes, path) {
		return TierHighSecurity
	}
	return TierOperational
}

// AllFiles returns all discovered file paths.
func (cf *ConfigFiles) AllFiles() []string {
	var files []string
	files = append(files, cf.Config)
	files = append(files, cf.Plugins...)
	files = append(files, cf.Pipelines...)
	if cf.Webhooks != "" {
		files = append(files, cf.Webhooks)
	}
	if cf.Routes != "" {
		files = append(files, cf.Routes)
	}
	if cf.RelayInstances != "" {
		files = append(files, cf.RelayInstances)
	}
	if cf.RelayIngress != "" {
		files = append(files, cf.RelayIngress)
	}
	files = append(files, cf.Scopes...)
	return files
}

// HighSecurityFiles returns only high-security tier file paths.
func (cf *ConfigFiles) HighSecurityFiles() []string {
	var files []string
	if cf.Webhooks != "" {
		files = append(files, cf.Webhooks)
	}
	files = append(files, cf.Scopes...)
	return files
}

// DefaultHookMaxDepth bounds the on-hook lifecycle chain. Four hops is enough
// for legitimate chained notifications while bounding loop blast radius. P2-11.
const DefaultHookMaxDepth = 4

// Defaults returns a Config with sensible defaults for MVP.
func Defaults() *Config {
	return &Config{
		Service: ServiceConfig{
			Name:                        "ductile",
			TickInterval:                60 * time.Second,
			LogLevel:                    "info",
			LogFormat:                   "json",
			DedupeTTL:                   24 * time.Hour,
			JobLogRetention:             30 * 24 * time.Hour,
			JobQueueRetention:           24 * time.Hour,
			JobTransitionsRetention:     30 * 24 * time.Hour,
			JobAttemptsRetention:        30 * 24 * time.Hour,
			BreakerTransitionsRetention: 90 * 24 * time.Hour,
			MaxWorkers:                  max(1, runtime.NumCPU()-1),
			AllowSymlinks:               false,
			HookMaxDepth:                DefaultHookMaxDepth,
		},
		State: StateConfig{
			Path: "./data/state.db",
		},
		API: APIConfig{
			Enabled: false,
			Listen:  "127.0.0.1:8080",
		},
		Plugins: make(map[string]PluginConf),
	}
}

// EffectivePluginRoots returns deduplicated plugin roots in priority order.
func (c *Config) EffectivePluginRoots() []string {
	roots := make([]string, 0, len(c.PluginRoots))
	roots = append(roots, c.PluginRoots...)

	seen := make(map[string]struct{}, len(roots))
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

// DefaultPluginConf returns default plugin configuration.
func DefaultPluginConf() PluginConf {
	return PluginConf{
		Enabled: true,
		Retry: &RetryConfig{
			MaxAttempts: 4,
			BackoffBase: 30 * time.Second,
		},
		Timeouts: &TimeoutsConfig{
			Poll:   60 * time.Second,
			Handle: 120 * time.Second,
			Health: 10 * time.Second,
			Init:   30 * time.Second,
		},
		CircuitBreaker: &CircuitBreakerConfig{
			Threshold:  3,
			ResetAfter: 30 * time.Minute,
		},
		MaxOutstandingPolls: 1,
		Parallelism:         1,
	}
}
