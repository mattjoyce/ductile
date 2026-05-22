package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/router/conditions"
)

// redactionSentinel replaces a sensitive value in the sanitized config
// view. Local to this package — configsnapshot uses a different,
// path-tagged sentinel deliberately; do not unify them.
const redactionSentinel = "[REDACTED]"

// sensitiveKeyPatterns are substrings that flag a plugin config key as sensitive.
var sensitiveKeyPatterns = []string{"secret", "key", "token", "password", "api_key", "credential", "auth", "passwd"}

func isSensitiveKey(k string) bool {
	lower := strings.ToLower(k)
	for _, p := range sensitiveKeyPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// sanitizePluginConfig redacts sensitive values from a plugin's config
// map at every nesting depth. A sensitive key anywhere in the tree
// (including under a non-sensitive parent, inside a nested map, or in a
// list element) is replaced with the redaction sentinel. F-006.
func sanitizePluginConfig(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	out, _ := redactValue(cfg).(map[string]any)
	return out
}

// redactValue returns a copy of v with sensitive map entries replaced by
// the redaction sentinel, recursing through nested maps and slices. It
// never mutates v: fresh containers are always allocated.
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isSensitiveKey(k) {
				out[k] = redactionSentinel
			} else {
				out[k] = redactValue(val)
			}
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if isSensitiveKey(k) {
				out[k] = redactionSentinel
			} else {
				out[k] = val
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, elem := range t {
			out[i] = redactValue(elem)
		}
		return out
	default:
		return v
	}
}

// redactStringMap is the typed-shape sibling of redactValue for map[string]string
// fields (pipeline step / relay step With and Baggage). The plain map[string]string
// shape is preserved so the JSON output for these fields stays a string-to-string
// object. Returns nil for nil input. Never mutates m.
func redactStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if isSensitiveKey(k) {
			out[k] = redactionSentinel
		} else {
			out[k] = v
		}
	}
	return out
}

// sanitizeScheduleConfig returns a value-copy of s with secret-shaped Payload
// entries redacted at every depth. The runtime config is never mutated because
// sanitizePluginConfig allocates a fresh container.
func sanitizeScheduleConfig(s config.ScheduleConfig) config.ScheduleConfig {
	s.Payload = sanitizePluginConfig(s.Payload)
	return s
}

// sanitizeScheduleConfigs returns a redacted slice copy; returns nil for nil input.
func sanitizeScheduleConfigs(ss []config.ScheduleConfig) []config.ScheduleConfig {
	if ss == nil {
		return nil
	}
	out := make([]config.ScheduleConfig, len(ss))
	for i, s := range ss {
		out[i] = sanitizeScheduleConfig(s)
	}
	return out
}

// sanitizeRelayStep returns a value-copy pointer with secret-shaped With/Baggage
// entries redacted. Returns nil for nil input.
func sanitizeRelayStep(r *config.RelayStepEntry) *config.RelayStepEntry {
	if r == nil {
		return nil
	}
	cp := *r
	cp.With = redactStringMap(cp.With)
	cp.Baggage = redactStringMap(cp.Baggage)
	return &cp
}

// sanitizeStepEntry returns a value-copy of s with secret-shaped With/Baggage
// redacted and nested Steps/Split lists likewise sanitised.
func sanitizeStepEntry(s config.StepEntry) config.StepEntry {
	s.With = redactStringMap(s.With)
	s.Baggage = redactStringMap(s.Baggage)
	s.Relay = sanitizeRelayStep(s.Relay)
	s.Steps = sanitizeStepEntries(s.Steps)
	s.Split = sanitizeStepEntries(s.Split)
	return s
}

// sanitizeStepEntries returns a redacted slice copy; returns nil for nil input.
func sanitizeStepEntries(ss []config.StepEntry) []config.StepEntry {
	if ss == nil {
		return nil
	}
	out := make([]config.StepEntry, len(ss))
	for i, s := range ss {
		out[i] = sanitizeStepEntry(s)
	}
	return out
}

// sanitizePipelines returns a slice copy of ps with each pipeline's Steps tree
// sanitised. The pipeline-level fields (Name, On, ExecutionMode, Timeout) carry
// no secret-bearing maps so they pass through unchanged.
func sanitizePipelines(ps []config.PipelineEntry) []config.PipelineEntry {
	if ps == nil {
		return nil
	}
	out := make([]config.PipelineEntry, len(ps))
	for i, p := range ps {
		p.Steps = sanitizeStepEntries(p.Steps)
		out[i] = p
	}
	return out
}

// -- Sanitized view types ----------------------------------------------------

type configViewPlugin struct {
	Enabled        bool                         `json:"enabled"`
	Uses           string                       `json:"uses,omitempty"`
	Schedules      []config.ScheduleConfig      `json:"schedules,omitempty"`
	Config         map[string]any               `json:"config,omitempty"`
	Retry          *config.RetryConfig          `json:"retry,omitempty"`
	Timeouts       *config.TimeoutsConfig       `json:"timeouts,omitempty"`
	CircuitBreaker *config.CircuitBreakerConfig `json:"circuit_breaker,omitempty"`
	Parallelism    int                          `json:"parallelism,omitempty"`
}

type configViewToken struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	// Key is intentionally omitted.
}

type configViewAPIToken struct {
	Scopes []string `json:"scopes"`
	// Token value is intentionally omitted.
}

type configViewWebhook struct {
	Name            string `json:"name,omitempty"`
	Path            string `json:"path"`
	Plugin          string `json:"plugin"`
	SecretRef       string `json:"secret_ref,omitempty"`
	SignatureHeader string `json:"signature_header,omitempty"`
	MaxBodySize     string `json:"max_body_size,omitempty"`
}

type configViewResponse struct {
	Service        config.ServiceConfig                 `json:"service"`
	API            configViewAPI                        `json:"api"`
	State          config.StateConfig                   `json:"state"`
	Plugins        map[string]configViewPlugin          `json:"plugins"`
	Pipelines      []config.PipelineEntry               `json:"pipelines"`
	Webhooks       []configViewWebhook                  `json:"webhooks,omitempty"`
	Tokens         []configViewToken                    `json:"tokens,omitempty"`
	Routes         []config.RouteConfig                 `json:"routes,omitempty"`
	CompiledRoutes map[string][]configViewCompiledRoute `json:"compiled_routes,omitempty"`
}

// configViewCompiledRoute exposes one compiled route's match-and-dispatch
// shape for operator inspection. Includes Sprint 17's source-plugin selector
// and the entry-route predicate so the richer match shape is visible rather
// than implicit.
type configViewCompiledRoute struct {
	ID          string                             `json:"id"`
	Source      configViewCompiledRouteSource      `json:"source"`
	Destination configViewCompiledRouteDestination `json:"destination"`
}

type configViewCompiledRouteSource struct {
	Trigger      string                `json:"trigger,omitempty"`
	HookSignal   string                `json:"hook_signal,omitempty"`
	SourcePlugin string                `json:"source_plugin,omitempty"`
	Pipeline     string                `json:"pipeline,omitempty"`
	StepID       string                `json:"step_id,omitempty"`
	EventType    string                `json:"event_type,omitempty"`
	DepthLT      int                   `json:"depth_lt,omitempty"`
	If           *conditions.Condition `json:"if,omitempty"`
}

type configViewCompiledRouteDestination struct {
	Kind         string `json:"kind"`
	StepID       string `json:"step_id,omitempty"`
	Plugin       string `json:"plugin,omitempty"`
	Command      string `json:"command,omitempty"`
	CallPipeline string `json:"call_pipeline,omitempty"`
}

// renderCompiledRoutes walks the router's compiled-route manifest for each
// loaded pipeline and returns a JSON-friendly view. Operators can use this
// to answer "what signal does this route match?", "does it require a source
// plugin?", and "what predicate is evaluated?" without reading the compiled
// DAG by hand.
//
// Pipeline names come from PipelineSummary so the inspection surface works
// in both directory mode (cfg.Pipelines populated) and inline-include mode
// (cfg.Pipelines is nil because pipelines come from a single included file
// rather than a pipelines/ directory).
func renderCompiledRoutes(router PipelineRouter) map[string][]configViewCompiledRoute {
	if router == nil {
		return nil
	}
	summary := router.PipelineSummary()
	if len(summary) == 0 {
		return nil
	}
	out := make(map[string][]configViewCompiledRoute)
	for _, info := range summary {
		routes := router.GetCompiledRoutes(info.Name)
		if len(routes) == 0 {
			continue
		}
		view := make([]configViewCompiledRoute, 0, len(routes))
		for _, route := range routes {
			view = append(view, configViewCompiledRoute{
				ID: route.ID,
				Source: configViewCompiledRouteSource{
					Trigger:      route.Source.Trigger,
					HookSignal:   route.Source.HookSignal,
					SourcePlugin: route.Source.SourcePlugin,
					Pipeline:     route.Source.Pipeline,
					StepID:       route.Source.StepID,
					EventType:    route.Source.EventType,
					DepthLT:      route.Source.DepthLT,
					If:           route.Source.If,
				},
				Destination: configViewCompiledRouteDestination{
					Kind:         string(route.Destination.Kind),
					StepID:       route.Destination.StepID,
					Plugin:       route.Destination.Plugin,
					Command:      route.Destination.Command,
					CallPipeline: route.Destination.CallPipeline,
				},
			})
		}
		out[info.Name] = view
	}
	return out
}

type configViewAPI struct {
	Enabled bool                 `json:"enabled"`
	Listen  string               `json:"listen"`
	Tokens  []configViewAPIToken `json:"tokens,omitempty"`
}

// -- Handler -----------------------------------------------------------------

// handleConfigView returns the reconciled config with secrets redacted.
func (s *Server) handleConfigView(w http.ResponseWriter, r *http.Request) {
	cfg := s.config.RuntimeConfig
	if cfg == nil {
		s.writeError(w, http.StatusServiceUnavailable, "config not available")
		return
	}

	// Sanitize API tokens — show scopes only, drop token values.
	apiTokens := make([]configViewAPIToken, 0, len(cfg.API.Auth.Tokens))
	for _, t := range cfg.API.Auth.Tokens {
		apiTokens = append(apiTokens, configViewAPIToken{Scopes: t.Scopes})
	}

	// Sanitize plugins — redact sensitive config map keys.
	plugins := make(map[string]configViewPlugin, len(cfg.Plugins))
	for name, p := range cfg.Plugins {
		schedules := p.Schedules
		if schedules == nil && p.Schedule != nil {
			schedules = []config.ScheduleConfig{*p.Schedule}
		}
		plugins[name] = configViewPlugin{
			Enabled:        p.Enabled,
			Uses:           p.Uses,
			Schedules:      sanitizeScheduleConfigs(schedules),
			Config:         sanitizePluginConfig(p.Config),
			Retry:          p.Retry,
			Timeouts:       p.Timeouts,
			CircuitBreaker: p.CircuitBreaker,
			Parallelism:    p.Parallelism,
		}
	}

	// Sanitize webhooks — SecretRef is a name reference, not the secret value; safe.
	var webhooks []configViewWebhook
	if cfg.Webhooks != nil {
		for _, ep := range cfg.Webhooks.Endpoints {
			webhooks = append(webhooks, configViewWebhook{
				Name:            ep.Name,
				Path:            ep.Path,
				Plugin:          ep.Plugin,
				SecretRef:       ep.SecretRef,
				SignatureHeader: ep.SignatureHeader,
				MaxBodySize:     ep.MaxBodySize,
			})
		}
	}

	// Sanitize tokens — drop Key field.
	tokens := make([]configViewToken, 0, len(cfg.Tokens))
	for _, t := range cfg.Tokens {
		tokens = append(tokens, configViewToken{
			Name:        t.Name,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
		})
	}

	resp := configViewResponse{
		Service: cfg.Service,
		API: configViewAPI{
			Enabled: cfg.API.Enabled,
			Listen:  cfg.API.Listen,
			Tokens:  apiTokens,
		},
		State:          cfg.State,
		Plugins:        plugins,
		Pipelines:      sanitizePipelines(cfg.Pipelines),
		Webhooks:       webhooks,
		Tokens:         tokens,
		Routes:         cfg.Routes,
		CompiledRoutes: renderCompiledRoutes(s.router),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
