// Package doctor validates ductile configuration and plugin setup.
package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
)

// StopwatchRetentionThresholds: when both row count and oldest-row age
// exceed these, doctor warns the operator that the stopwatch ledger
// looks like it has no retention configured. Two conditions in
// conjunction so a small instance with a few months of rows doesn't
// get nagged.
const (
	StopwatchWarnRowCount = 100_000
	StopwatchWarnMaxAge   = 90 * 24 * time.Hour
)

// StopwatchSnapshot is a cheap probe of job_stopwatch fed to Doctor by
// the caller (typically `ductile config check`, which has DB access).
// When nil, the stopwatch retention check is silently skipped — useful
// for callers that don't have or want a DB connection (e.g. startup
// strict-mode validation).
type StopwatchSnapshot struct {
	RowCount         int
	OldestRecordedAt time.Time // zero value if RowCount == 0
}

// Result holds the outcome of a validation run.
type Result struct {
	Valid    bool    `json:"valid"`
	Errors   []Issue `json:"errors,omitempty"`
	Warnings []Issue `json:"warnings,omitempty"`
}

// Issue describes a single validation error or warning.
type Issue struct {
	Category string `json:"category"`
	Message  string `json:"message"`
	Field    string `json:"field,omitempty"`
}

// HookPipeline is a minimal projection of a compiled hook pipeline, used by
// validateHookCycles. Callers extract these from their loaded router pipelines
// (only on-hook entries with at least one target need be included). P2-11.
type HookPipeline struct {
	Name       string   // pipeline name, used in cycle warning messages
	OnHook     string   // hook trigger signal (e.g. "job.completed")
	FromPlugin string   // optional source plugin filter; "" means any source
	Targets    []string // target plugin names invoked by this pipeline's steps
}

// Doctor validates configuration against discovered plugins.
type Doctor struct {
	cfg               *config.Config
	registry          *plugin.Registry
	hookPipelines     []HookPipeline
	stopwatchSnapshot *StopwatchSnapshot
}

// New creates a Doctor from a loaded config and plugin registry.
func New(cfg *config.Config, registry *plugin.Registry) *Doctor {
	return &Doctor{cfg: cfg, registry: registry}
}

// AddHookPipelines registers compiled hook pipelines for cycle analysis.
// Without this, validateHookCycles has no graph to walk and stays silent.
// P2-11.
func (d *Doctor) AddHookPipelines(hooks []HookPipeline) *Doctor {
	d.hookPipelines = hooks
	return d
}

// AddStopwatchSnapshot supplies a probe of job_stopwatch (row count +
// oldest-row timestamp). When set, warnStopwatchRetention runs. When
// nil, the check is silently skipped — callers without DB access
// (e.g. startup strict-mode validation) can simply not call this.
func (d *Doctor) AddStopwatchSnapshot(snap *StopwatchSnapshot) *Doctor {
	d.stopwatchSnapshot = snap
	return d
}

// Validate runs all checks and returns a result.
func (d *Doctor) Validate() *Result {
	r := &Result{Valid: true}

	d.validateServiceConfig(r)
	d.validatePluginRefs(r)
	d.validateUsesCycles(r)
	d.validateAPIConfig(r)
	d.validateTokenScopes(r)
	d.validateWebhooks(r)
	d.validateRoutes(r)
	d.validateHookCycles(r)
	d.warnUnusedPlugins(r)
	d.warnMissingEnvVars(r)
	d.warnSuspiciousSchedule(r)
	d.warnStopwatchRetention(r)

	r.Valid = len(r.Errors) == 0
	return r
}

// warnStopwatchRetention surfaces unbounded growth of job_stopwatch
// when both the row count AND oldest-row age cross their thresholds.
// Silent when no snapshot is provided OR when either threshold is not
// crossed. The conjunction matters: a small instance with a long
// history shouldn't be nagged, and a busy instance with recent
// pruning shouldn't be either.
//
// Suggestion text names the CLI directly so the operator can copy-paste.
func (d *Doctor) warnStopwatchRetention(r *Result) {
	snap := d.stopwatchSnapshot
	if snap == nil {
		return
	}
	if snap.RowCount < StopwatchWarnRowCount {
		return
	}
	if snap.OldestRecordedAt.IsZero() {
		return
	}
	age := time.Since(snap.OldestRecordedAt)
	if age < StopwatchWarnMaxAge {
		return
	}
	d.addWarning(r, "telemetry", "job_stopwatch", fmt.Sprintf(
		"job_stopwatch has %d rows and the oldest is %s old (threshold %s). "+
			"Consider running 'ductile stopwatch prune --older-than 14d' or wiring "+
			"it as a scheduled sys_exec invocation.",
		snap.RowCount,
		age.Round(time.Hour),
		StopwatchWarnMaxAge,
	))
}

func (d *Doctor) addError(r *Result, category, field, msg string) {
	r.Errors = append(r.Errors, Issue{Category: category, Field: field, Message: msg})
}

func (d *Doctor) addWarning(r *Result, category, field, msg string) {
	r.Warnings = append(r.Warnings, Issue{Category: category, Field: field, Message: msg})
}

// validateServiceConfig checks required service fields.
func (d *Doctor) validateServiceConfig(r *Result) {
	if len(d.cfg.EffectivePluginRoots()) == 0 {
		d.addError(r, "service", "plugin_roots", "plugin_roots is required")
	}
	if d.cfg.State.Path == "" {
		d.addError(r, "service", "state.path", "state.path is required")
	}
	if d.cfg.Service.TickInterval <= 0 {
		d.addError(r, "service", "service.tick_interval", "tick_interval must be positive")
	}
	// P2-10: warn when tick_interval is below the recommended threshold even if it
	// passes the hard floor. Loader rejects sub-MinTickInterval values; doctor warns
	// for everything between MinTickInterval and RecommendedTickInterval.
	if d.cfg.Service.TickInterval >= config.MinTickInterval && d.cfg.Service.TickInterval < config.RecommendedTickInterval {
		d.addWarning(r, "service", "service.tick_interval",
			fmt.Sprintf("tick_interval %s is below recommended (%s); chatty service polls can flood dispatch", d.cfg.Service.TickInterval, config.RecommendedTickInterval))
	}
}

// validatePluginRefs checks that plugins in config are discoverable.
func (d *Doctor) validatePluginRefs(r *Result) {
	for name, pc := range d.cfg.Plugins {
		if !pc.Enabled {
			continue
		}

		lookupName := name
		uses := strings.TrimSpace(pc.Uses)
		if uses != "" {
			if uses == name {
				d.addError(r, "plugin_refs", fmt.Sprintf("plugins.%s.uses", name),
					fmt.Sprintf("plugin %q: uses must reference a different base plugin", name))
				continue
			}
			lookupName = uses
		}

		p, ok := d.registry.Get(lookupName)
		if !ok {
			field := fmt.Sprintf("plugins.%s", name)
			if uses != "" {
				field = fmt.Sprintf("plugins.%s.uses", name)
			}
			d.addError(r, "plugin_refs", field,
				fmt.Sprintf("plugin %q in config but not found in configured plugin roots", lookupName))
			continue
		}

		// Check required config keys
		if p.ConfigKeys != nil {
			for _, key := range p.ConfigKeys.Required {
				if pc.Config == nil {
					d.addError(r, "plugin_refs", fmt.Sprintf("plugins.%s.config", name),
						fmt.Sprintf("plugin %q requires config key %q", lookupName, key))
					continue
				}
				if _, exists := pc.Config[key]; !exists {
					d.addError(r, "plugin_refs", fmt.Sprintf("plugins.%s.config.%s", name, key),
						fmt.Sprintf("plugin %q requires config key %q", lookupName, key))
				}
			}
		}
	}
}

// validateAPIConfig checks API server settings.
func (d *Doctor) validateAPIConfig(r *Result) {
	if !d.cfg.API.Enabled {
		return
	}
	if d.cfg.API.Listen == "" {
		d.addError(r, "api", "api.listen", "api.listen is required when API is enabled")
	}
	if len(d.cfg.API.Auth.Tokens) == 0 {
		d.addError(r, "api", "api.auth.tokens", "api.auth.tokens must be configured when API is enabled")
	}
}

// validateTokenScopes checks that scope references resolve to real plugins/commands.
func (d *Doctor) validateTokenScopes(r *Result) {
	for i, token := range d.cfg.API.Auth.Tokens {
		for j, scope := range token.Scopes {
			field := fmt.Sprintf("api.auth.tokens[%d].scopes[%d]", i, j)
			d.validateSingleScope(r, scope, field)
		}
	}
}

func (d *Doctor) validateSingleScope(r *Result, scope, field string) {
	// Admin wildcard
	if scope == "*" {
		return
	}

	parts := strings.SplitN(scope, ":", 2)
	if len(parts) < 2 {
		d.addError(r, "token_scopes", field,
			fmt.Sprintf("invalid scope %q (expected format: resource:access or action:resource:command)", scope))
		return
	}

	first, second := parts[0], parts[1]

	// Low-level: action:resource or action:resource:command
	if first == "read" || first == "trigger" || first == "admin" {
		// Valid low-level scope syntax
		return
	}

	// Manifest-driven: plugin:ro, plugin:rw, plugin:allow:cmd, plugin:deny:cmd
	pluginName := first
	p, ok := d.registry.Get(pluginName)
	if !ok {
		// Check if it's a known non-plugin resource
		if pluginName == "jobs" || pluginName == "events" || pluginName == "healthz" || pluginName == "queue" ||
			pluginName == "system" || pluginName == "plugin" {
			return
		}
		d.addError(r, "token_scopes", field,
			fmt.Sprintf("scope %q references unknown plugin %q", scope, pluginName))
		return
	}

	switch {
	case second == "ro" || second == "rw":
		// Valid
	case strings.HasPrefix(second, "allow:"):
		cmd := strings.TrimPrefix(second, "allow:")
		if cmd != "*" && !p.SupportsCommand(cmd) {
			d.addError(r, "token_scopes", field,
				fmt.Sprintf("scope %q: plugin %q has no command %q", scope, pluginName, cmd))
		}
	case strings.HasPrefix(second, "deny:"):
		cmd := strings.TrimPrefix(second, "deny:")
		if cmd != "*" && !p.SupportsCommand(cmd) {
			d.addWarning(r, "token_scopes", field,
				fmt.Sprintf("scope %q: plugin %q has no command %q (deny is a no-op)", scope, pluginName, cmd))
		}
	default:
		d.addError(r, "token_scopes", field,
			fmt.Sprintf("scope %q: invalid access type %q (expected ro, rw, allow:cmd, or deny:cmd)", scope, second))
	}
}

// validateWebhooks checks for path conflicts and plugin references.
func (d *Doctor) validateWebhooks(r *Result) {
	if d.cfg.Webhooks == nil {
		return
	}

	seen := make(map[string]int)
	for i, ep := range d.cfg.Webhooks.Endpoints {
		field := fmt.Sprintf("webhooks.endpoints[%d]", i)

		// Check plugin exists
		if _, ok := d.registry.Get(ep.Plugin); !ok {
			d.addError(r, "webhooks", field+".plugin",
				fmt.Sprintf("webhook %q targets plugin %q which was not discovered", ep.Path, ep.Plugin))
		}

		// Check for path conflicts
		normalized := strings.TrimSuffix(ep.Path, "/")
		if prevIdx, exists := seen[normalized]; exists {
			d.addError(r, "webhooks", field+".path",
				fmt.Sprintf("webhook path %q conflicts with webhooks.endpoints[%d]", ep.Path, prevIdx))
		}
		seen[normalized] = i

		// Check secret configured
		if ep.SecretRef == "" {
			d.addError(r, "webhooks", field,
				fmt.Sprintf("webhook %q: secret_ref is required", ep.Path))
		}
	}
}

// validateRoutes checks plugin refs and circular dependencies.
func (d *Doctor) validateRoutes(r *Result) {
	if len(d.cfg.Routes) == 0 {
		return
	}

	graph := make(map[string][]string)
	for i, route := range d.cfg.Routes {
		field := fmt.Sprintf("routes[%d]", i)

		if _, ok := d.registry.Get(route.From); !ok {
			if _, inConfig := d.cfg.Plugins[route.From]; !inConfig {
				d.addError(r, "routes", field+".from",
					fmt.Sprintf("route source plugin %q not found", route.From))
			}
		}
		if _, ok := d.registry.Get(route.To); !ok {
			if _, inConfig := d.cfg.Plugins[route.To]; !inConfig {
				d.addError(r, "routes", field+".to",
					fmt.Sprintf("route target plugin %q not found", route.To))
			}
		}

		graph[route.From] = append(graph[route.From], route.To)
	}

	if node, cyclic := detectGraphCycle(graph); cyclic {
		d.addError(r, "routes", "routes",
			fmt.Sprintf("circular dependency detected involving plugin %q", node))
	}
}

// detectGraphCycle runs a 3-color DFS over a directed adjacency graph and
// returns a node on a cycle if one exists. Shared by route and uses
// validation so both forms of circular dependency use the same machinery.
func detectGraphCycle(graph map[string][]string) (string, bool) {
	const (
		unvisited = 0
		inStack   = 1
		done      = 2
	)
	visited := make(map[string]int)
	var walk func(node string) bool
	walk = func(node string) bool {
		visited[node] = inStack
		for _, next := range graph[node] {
			if visited[next] == inStack {
				return true
			}
			if visited[next] == unvisited && walk(next) {
				return true
			}
		}
		visited[node] = done
		return false
	}
	for node := range graph {
		if visited[node] == unvisited && walk(node) {
			return node, true
		}
	}
	return "", false
}

// validateHookCycles walks the on-hook lifecycle graph and warns when a cycle
// exists. P2-11: a reciprocal notifying-hook configuration (A's completion
// fires a hook targeting B, B's completion fires a hook targeting A — and both
// plugins have notify_on_complete: true) creates an unbounded work loop at
// runtime. This check surfaces the cycle at config time.
//
// Graph edges: for each plugin P with notify_on_complete: true AND each hook
// pipeline H where H.FromPlugin is "" or P, add an edge P → T for every target
// T in H.Targets that itself has notify_on_complete: true. Targets without
// notify_on_complete cannot re-fire so they are terminal and excluded from the
// cycle graph (avoids false positives).
func (d *Doctor) validateHookCycles(r *Result) {
	if len(d.hookPipelines) == 0 {
		return
	}

	// Identify plugins that re-fire on completion. Only these can extend a hook
	// chain — a target plugin without notify_on_complete is a terminal node.
	notifying := make(map[string]bool, len(d.cfg.Plugins))
	for name, pc := range d.cfg.Plugins {
		if !pc.Enabled {
			continue
		}
		if pc.NotifyOnComplete != nil && *pc.NotifyOnComplete {
			notifying[name] = true
		}
	}
	if len(notifying) == 0 {
		return
	}

	graph := make(map[string][]string)
	for source := range notifying {
		for _, hook := range d.hookPipelines {
			if hook.OnHook == "" {
				continue
			}
			if hook.FromPlugin != "" && hook.FromPlugin != source {
				continue
			}
			for _, target := range hook.Targets {
				if !notifying[target] {
					continue // target cannot re-fire — no extending the chain
				}
				graph[source] = append(graph[source], target)
			}
		}
	}

	if len(graph) == 0 {
		return
	}

	if node, cyclic := detectGraphCycle(graph); cyclic {
		d.addWarning(r, "hook_cycles", "",
			fmt.Sprintf("reciprocal notify_on_complete hook cycle detected involving plugin %q; review pipelines with on-hook triggers", node))
	}
}

// validateUsesCycles detects indirect cycles in the plugin `uses` graph
// (a uses b, b uses a). Direct self-reference (uses == name) is reported
// with a more specific message by validatePluginRefs and is excluded from
// the graph here to avoid double reporting.
func (d *Doctor) validateUsesCycles(r *Result) {
	graph := make(map[string][]string)
	for name, pc := range d.cfg.Plugins {
		if !pc.Enabled {
			continue
		}
		uses := strings.TrimSpace(pc.Uses)
		if uses == "" || uses == name {
			continue
		}
		graph[name] = append(graph[name], uses)
	}
	if node, cyclic := detectGraphCycle(graph); cyclic {
		d.addError(r, "plugin_refs", "plugins",
			fmt.Sprintf("circular uses dependency detected involving plugin %q", node))
	}
}

// warnUnusedPlugins warns about discovered plugins not referenced in config.
func (d *Doctor) warnUnusedPlugins(r *Result) {
	for name := range d.registry.All() {
		if _, inConfig := d.cfg.Plugins[name]; !inConfig {
			d.addWarning(r, "unused", "",
				fmt.Sprintf("plugin %q discovered but not referenced in config", name))
		}
	}
}

// warnMissingEnvVars warns about ${VAR} references where VAR is not set.
func (d *Doctor) warnMissingEnvVars(r *Result) {
	envVarRe := regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)

	// Check token values and webhook secrets for unresolved env vars
	for i, token := range d.cfg.API.Auth.Tokens {
		if token.Token == "" {
			d.addWarning(r, "env_vars", fmt.Sprintf("api.auth.tokens[%d].token", i),
				"token value is empty (possibly unresolved environment variable)")
		}
	}

	// Check webhook secrets
	if d.cfg.Webhooks != nil {
		for i, ep := range d.cfg.Webhooks.Endpoints {
			if ep.SecretRef != "" && envVarRe.MatchString(ep.SecretRef) {
				for _, m := range envVarRe.FindAllStringSubmatch(ep.SecretRef, -1) {
					if os.Getenv(m[1]) == "" {
						d.addWarning(r, "env_vars", fmt.Sprintf("webhooks.endpoints[%d].secret_ref", i),
							fmt.Sprintf("environment variable ${%s} not set", m[1]))
					}
				}
			}
		}
	}
}

// warnSuspiciousSchedule warns about intervals that seem too short or too long.
func (d *Doctor) warnSuspiciousSchedule(r *Result) {
	for name, pc := range d.cfg.Plugins {
		if !pc.Enabled {
			continue
		}
		schedules := pc.NormalizedSchedules()
		if len(schedules) == 0 {
			continue
		}
		for i, schedule := range schedules {
			if strings.TrimSpace(schedule.Cron) != "" {
				continue
			}
			interval, err := config.ParseInterval(schedule.Every)
			if err != nil {
				d.addError(r, "schedule", fmt.Sprintf("plugins.%s.schedules[%d].every", name, i),
					fmt.Sprintf("invalid schedule interval %q: %v", schedule.Every, err))
				continue
			}
			if interval.Minutes() < 1 {
				d.addWarning(r, "schedule", fmt.Sprintf("plugins.%s.schedules[%d].every", name, i),
					fmt.Sprintf("schedule interval %q is very short (< 1m)", schedule.Every))
			}
			_ = interval // weekly/monthly are fine; only sub-minute intervals are warned on here.
		}
	}
}

// FormatHuman returns a human-readable validation report.
func FormatHuman(r *Result) string {
	var b strings.Builder

	if r.Valid && len(r.Warnings) == 0 {
		b.WriteString("Configuration valid.\n")
		return b.String()
	}

	if r.Valid && len(r.Warnings) > 0 {
		b.WriteString("Configuration valid")
		fmt.Fprintf(&b, " (%d warning(s))\n", len(r.Warnings))
	}

	if !r.Valid {
		fmt.Fprintf(&b, "Configuration invalid (%d error(s), %d warning(s))\n", len(r.Errors), len(r.Warnings))
	}

	for _, e := range r.Errors {
		if e.Field != "" {
			fmt.Fprintf(&b, "  ERROR [%s] %s: %s\n", e.Category, e.Field, e.Message)
		} else {
			fmt.Fprintf(&b, "  ERROR [%s] %s\n", e.Category, e.Message)
		}
	}
	for _, w := range r.Warnings {
		if w.Field != "" {
			fmt.Fprintf(&b, "  WARN  [%s] %s: %s\n", w.Category, w.Field, w.Message)
		} else {
			fmt.Fprintf(&b, "  WARN  [%s] %s\n", w.Category, w.Message)
		}
	}

	return b.String()
}

// FormatJSON returns the result as indented JSON.
func FormatJSON(r *Result) (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
