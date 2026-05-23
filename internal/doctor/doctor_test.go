package doctor

import (
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
)

func validConfig() *config.Config {
	return &config.Config{
		Service: config.ServiceConfig{
			Name:         "test",
			TickInterval: 60 * time.Second,
			LogLevel:     "info",
		},
		State:       config.StateConfig{Path: "/tmp/test.db"},
		PluginRoots: []string{"./plugins"},
		Plugins: map[string]config.PluginConf{
			"echo": {
				Enabled: true,
				Schedules: []config.ScheduleConfig{
					{Every: "5m"},
				},
			},
		},
	}
}

func registryWith(plugins ...*plugin.Plugin) *plugin.Registry {
	r := plugin.NewRegistry()
	for _, p := range plugins {
		_ = r.Add(p)
	}
	return r
}

func echoPlugin() *plugin.Plugin {
	return &plugin.Plugin{
		Name:     "echo",
		Protocol: 2,
		Commands: plugin.Commands{
			{Name: "poll", Type: plugin.CommandTypeRead},
			{Name: "handle", Type: plugin.CommandTypeWrite},
		},
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	t.Parallel()
	d := New(validConfig(), registryWith(echoPlugin()))
	r := d.Validate()
	if !r.Valid {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
}

func TestValidate_MissingPluginRoots(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.PluginRoots = nil
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "service", "plugin_roots")
}

func TestValidate_MissingStatePath(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.State.Path = ""
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "service", "state.path")
}

func TestValidate_PluginNotDiscovered(t *testing.T) {
	t.Parallel()
	d := New(validConfig(), registryWith()) // empty registry
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "plugin_refs", "echo")
}

func TestValidate_RequiredConfigKey(t *testing.T) {
	t.Parallel()
	p := echoPlugin()
	p.ConfigKeys = &plugin.ConfigKeys{Required: []string{"api_token"}}
	d := New(validConfig(), registryWith(p))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "plugin_refs", "api_token")
}

func TestValidate_UsesAliasResolves(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Plugins = map[string]config.PluginConf{
		"check_youtube": {
			Enabled: true,
			Uses:    "echo",
		},
	}
	p := echoPlugin()
	d := New(cfg, registryWith(p))
	r := d.Validate()
	if !r.Valid {
		t.Fatalf("expected valid, got errors: %v", r.Errors)
	}
}

func TestValidate_UsesMissingBase(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Plugins = map[string]config.PluginConf{
		"check_youtube": {
			Enabled: true,
			Uses:    "switch",
		},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "plugin_refs", "switch")
}

// TestValidate_UsesIndirectCycle reproduces C-FRO-17: only a direct
// self-reference (uses == name) was rejected. An indirect uses cycle
// (a uses b, b uses a) was never detected, even though a 3-color DFS cycle
// detector already exists in this file for routes. The uses graph must run
// through the same cycle detection.
func TestValidate_UsesIndirectCycle(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Plugins = map[string]config.PluginConf{
		"a": {Enabled: true, Uses: "b"},
		"b": {Enabled: true, Uses: "a"},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid: indirect uses cycle a->b->a")
	}
	assertHasError(t, r, "plugin_refs", "circular")
}

func TestValidate_TokenScopeValidPlugin(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.API.Enabled = true
	cfg.API.Listen = "localhost:8080"
	cfg.API.Auth.Tokens = []config.APIToken{
		{Token: "test-key", Scopes: []string{"echo:ro"}},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if !r.Valid {
		t.Fatalf("expected valid, got: %v", r.Errors)
	}
}

func TestValidate_TokenScopeUnknownPlugin(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.API.Enabled = true
	cfg.API.Listen = "localhost:8080"
	cfg.API.Auth.Tokens = []config.APIToken{
		{Token: "test-key", Scopes: []string{"nonexistent:ro"}},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "token_scopes", "nonexistent")
}

func TestValidate_TokenScopeInvalidCommand(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.API.Enabled = true
	cfg.API.Listen = "localhost:8080"
	cfg.API.Auth.Tokens = []config.APIToken{
		{Token: "test-key", Scopes: []string{"echo:allow:bogus"}},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "token_scopes", "bogus")
}

func TestValidate_TokenScopeLowLevel(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.API.Enabled = true
	cfg.API.Listen = "localhost:8080"
	cfg.API.Auth.Tokens = []config.APIToken{
		{Token: "test-key", Scopes: []string{"read:jobs", "trigger:echo:poll", "admin:*"}},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if !r.Valid {
		t.Fatalf("expected valid, got: %v", r.Errors)
	}
}

func TestValidate_WebhookPathConflict(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Webhooks = &config.WebhooksConfig{
		Listen: ":9090",
		Endpoints: []config.WebhookEndpoint{
			{Path: "/webhook/github", Plugin: "echo", SecretRef: "s1", SignatureHeader: "X-Hub-Signature-256"},
			{Path: "/webhook/github/", Plugin: "echo", SecretRef: "s2", SignatureHeader: "X-Hub-Signature-256"},
		},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "webhooks", "conflict")
}

func TestValidate_WebhookMissingSecret(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Webhooks = &config.WebhooksConfig{
		Listen: ":9090",
		Endpoints: []config.WebhookEndpoint{
			{Path: "/webhook/test", Plugin: "echo", SignatureHeader: "X-Sig"},
		},
	}
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "webhooks", "secret")
}

func TestValidate_RouteCycle(t *testing.T) {
	t.Parallel()
	pluginA := &plugin.Plugin{Name: "a", Commands: plugin.Commands{{Name: "handle", Type: plugin.CommandTypeWrite}}}
	pluginB := &plugin.Plugin{Name: "b", Commands: plugin.Commands{{Name: "handle", Type: plugin.CommandTypeWrite}}}
	cfg := validConfig()
	cfg.Plugins["a"] = config.PluginConf{Enabled: true}
	cfg.Plugins["b"] = config.PluginConf{Enabled: true}
	cfg.Routes = []config.RouteConfig{
		{From: "a", EventType: "x", To: "b"},
		{From: "b", EventType: "y", To: "a"},
	}
	d := New(cfg, registryWith(echoPlugin(), pluginA, pluginB))
	r := d.Validate()
	if r.Valid {
		t.Fatal("expected invalid")
	}
	assertHasError(t, r, "routes", "circular")
}

// TestValidate_WarnsLowTickInterval — P2-10: doctor flags tick rates below the
// recommended bound (1s) but above the hard floor (100ms) so operators see the
// chatty-poll warning before runtime symptoms appear.
func TestValidate_WarnsLowTickInterval(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Service.TickInterval = 500 * time.Millisecond
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	if !r.Valid {
		t.Fatalf("expected valid (warning only), got errors: %v", r.Errors)
	}
	assertHasWarning(t, r, "service", "tick_interval")
}

// TestValidate_NoWarnForRecommendedTickInterval — boundary: 1s exactly should not warn.
func TestValidate_NoWarnForRecommendedTickInterval(t *testing.T) {
	t.Parallel()
	cfg := validConfig()
	cfg.Service.TickInterval = 1 * time.Second
	d := New(cfg, registryWith(echoPlugin()))
	r := d.Validate()
	for _, w := range r.Warnings {
		if w.Category == "service" && strings.Contains(w.Message, "tick_interval") {
			t.Fatalf("did not expect tick_interval warning at 1s boundary, got: %v", w)
		}
	}
}

func TestValidate_WarnUnusedPlugin(t *testing.T) {
	t.Parallel()
	extra := &plugin.Plugin{Name: "unused-plugin", Commands: plugin.Commands{{Name: "poll", Type: plugin.CommandTypeRead}}}
	d := New(validConfig(), registryWith(echoPlugin(), extra))
	r := d.Validate()
	if !r.Valid {
		t.Fatalf("expected valid, got: %v", r.Errors)
	}
	assertHasWarning(t, r, "unused", "unused-plugin")
}

func TestFormatJSON(t *testing.T) {
	t.Parallel()
	r := &Result{
		Valid:  false,
		Errors: []Issue{{Category: "test", Message: "bad thing"}},
	}
	out, err := FormatJSON(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "bad thing") {
		t.Fatalf("expected JSON to contain error message, got: %s", out)
	}
}

func TestFormatHuman_Valid(t *testing.T) {
	t.Parallel()
	r := &Result{Valid: true}
	out := FormatHuman(r)
	if !strings.Contains(out, "valid") {
		t.Fatalf("expected 'valid' in output, got: %s", out)
	}
}

func TestFormatHuman_Errors(t *testing.T) {
	t.Parallel()
	r := &Result{
		Valid:  false,
		Errors: []Issue{{Category: "test", Field: "x.y", Message: "broken"}},
	}
	out := FormatHuman(r)
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "broken") {
		t.Fatalf("expected error in output, got: %s", out)
	}
}

// --- helpers ---

func assertHasError(t *testing.T, r *Result, category, substring string) {
	t.Helper()
	for _, e := range r.Errors {
		if e.Category == category && strings.Contains(e.Message, substring) {
			return
		}
	}
	t.Fatalf("expected error with category=%q containing %q, got: %v", category, substring, r.Errors)
}

func assertHasWarning(t *testing.T, r *Result, category, substring string) {
	t.Helper()
	for _, w := range r.Warnings {
		if w.Category == category && strings.Contains(w.Message, substring) {
			return
		}
	}
	t.Fatalf("expected warning with category=%q containing %q, got: %v", category, substring, r.Warnings)
}
