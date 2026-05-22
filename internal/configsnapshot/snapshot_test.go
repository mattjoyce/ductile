package configsnapshot

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/storage"
	"gopkg.in/yaml.v3"
)

func TestBuildRedactsSecretsAndHashesSecretChanges(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("service:\n  name: ductile\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := config.Defaults()
	cfg.SourceFiles = map[string]*yaml.Node{configPath: {}}
	cfg.API.Enabled = true
	cfg.API.Auth.Tokens = []config.APIToken{{Token: "api-secret-one", Scopes: []string{"job:rw"}}}
	cfg.Tokens = []config.TokenEntry{{Name: "github_webhook_secret", Key: "webhook-secret-one"}}
	cfg.Webhooks = &config.WebhooksConfig{
		Endpoints: []config.WebhookEndpoint{{Name: "github", Path: "/github", Plugin: "echo", SecretRef: "github_webhook_secret"}},
	}
	cfg.Plugins["echo"] = config.PluginConf{
		Enabled: true,
		Config:  map[string]any{"api_key": "plugin-secret-one", "public": "visible"},
	}

	first, err := Build(BuildInput{
		Config:         cfg,
		ConfigPath:     configPath,
		ConfigSource:   "explicit",
		Reason:         ReasonStartup,
		DuctileVersion: "test-version",
		BinaryPath:     "/tmp/ductile",
		LoadedAt:       time.Date(2026, 4, 18, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Build(first): %v", err)
	}
	if strings.Contains(string(first.SanitizedConfig), "api-secret-one") ||
		strings.Contains(string(first.SanitizedConfig), "webhook-secret-one") ||
		strings.Contains(string(first.SanitizedConfig), "plugin-secret-one") {
		t.Fatalf("sanitized config leaked secret material: %s", first.SanitizedConfig)
	}
	if !strings.Contains(string(first.SanitizedConfig), "[redacted:api.auth.tokens[0].token]") {
		t.Fatalf("sanitized config did not redact API token: %s", first.SanitizedConfig)
	}
	if !strings.HasPrefix(first.ConfigHash, "blake3:") {
		t.Fatalf("ConfigHash = %q", first.ConfigHash)
	}
	if first.SourceHash == nil || !strings.HasPrefix(*first.SourceHash, "blake3:") {
		t.Fatalf("SourceHash = %v", first.SourceHash)
	}

	var uses []SecretUse
	if err := json.Unmarshal(first.SecretFingerprints, &uses); err != nil {
		t.Fatalf("unmarshal secret uses: %v", err)
	}
	if len(uses) < 3 {
		t.Fatalf("expected at least 3 secret uses, got %+v", uses)
	}

	cfg.API.Auth.Tokens[0].Token = "api-secret-two"
	second, err := Build(BuildInput{
		Config:       cfg,
		ConfigPath:   configPath,
		ConfigSource: "explicit",
		Reason:       ReasonStartup,
	})
	if err != nil {
		t.Fatalf("Build(second): %v", err)
	}
	if first.ConfigHash == second.ConfigHash {
		t.Fatal("config hash did not change after secret-only change")
	}
}

// TestBuildRedactsPipelineAndScheduleSecrets reproduces C-FRO-15: secret
// redaction descended only plugins.<name>.config. Sibling subtrees —
// pipeline step `with`/`baggage` and schedule `payload` — were copied
// verbatim into the snapshot, leaking secrets in plaintext. Redaction must
// apply to the whole snapshot structure (F-006 discipline, wider surface).
func TestBuildRedactsPipelineAndScheduleSecrets(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte("service:\n  name: ductile\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := config.Defaults()
	cfg.SourceFiles = map[string]*yaml.Node{configPath: {}}
	cfg.Plugins["echo"] = config.PluginConf{
		Enabled: true,
		Schedules: []config.ScheduleConfig{
			{Every: "5m", Payload: map[string]any{"api_key": "schedule-secret-leak"}},
		},
	}
	cfg.Pipelines = []config.PipelineEntry{
		{
			Name: "p1",
			On:   "echo.done",
			Steps: []config.StepEntry{
				{
					ID:      "s1",
					Uses:    "echo",
					With:    map[string]string{"token": "step-with-secret-leak"},
					Baggage: map[string]string{"auth_secret": "baggage-secret-leak"},
				},
			},
		},
	}

	snap, err := Build(BuildInput{
		Config:         cfg,
		ConfigPath:     configPath,
		ConfigSource:   "explicit",
		Reason:         ReasonStartup,
		DuctileVersion: "test-version",
		BinaryPath:     "/tmp/ductile",
		LoadedAt:       time.Date(2026, 5, 17, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, leak := range []string{"step-with-secret-leak", "baggage-secret-leak", "schedule-secret-leak"} {
		if strings.Contains(string(snap.SanitizedConfig), leak) {
			t.Fatalf("sanitized config leaked %q: %s", leak, snap.SanitizedConfig)
		}
	}

	// Redacted secrets in these subtrees must still be fingerprinted so
	// secret_fingerprints / ConfigHash track them (Codex finding 2): the
	// audit value of redaction depends on drift tracking, not just absence
	// of plaintext.
	var uses []SecretUse
	if err := json.Unmarshal(snap.SecretFingerprints, &uses); err != nil {
		t.Fatalf("unmarshal secret uses: %v", err)
	}
	wantPurposes := map[string]bool{
		"pipelines.p1.steps[0].with.token":          false,
		"pipelines.p1.steps[0].baggage.auth_secret": false,
		"plugins.echo.schedules[0].payload.api_key": false,
	}
	for _, u := range uses {
		if _, ok := wantPurposes[u.Purpose]; ok {
			wantPurposes[u.Purpose] = true
		}
	}
	for purpose, seen := range wantPurposes {
		if !seen {
			t.Fatalf("secret %q not recorded in secret_fingerprints: %s", purpose, snap.SecretFingerprints)
		}
	}

	// A secret-only change inside a redacted subtree must flip ConfigHash
	// (secret drift is tracked, not silently lost).
	cfg.Pipelines[0].Steps[0].With["token"] = "step-with-secret-rotated"
	snap2, err := Build(BuildInput{
		Config:       cfg,
		ConfigPath:   configPath,
		ConfigSource: "explicit",
		Reason:       ReasonStartup,
	})
	if err != nil {
		t.Fatalf("Build(second): %v", err)
	}
	if snap.ConfigHash == snap2.ConfigHash {
		t.Fatal("ConfigHash unchanged after rotating a secret inside a pipeline step `with` (secret drift not tracked)")
	}
}

func TestBuildRecordsExplicitBaggageSemantics(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.Pipelines = []config.PipelineEntry{
		{
			Name: "wisdom",
			On:   "event.start",
			Steps: []config.StepEntry{
				{
					ID:   "summarize",
					Uses: "fabric",
					Baggage: map[string]string{
						"summary.text": "payload.result",
						"from":         "payload.metadata",
						"namespace":    "whisper",
					},
				},
			},
		},
	}

	first, err := Build(BuildInput{
		Config: cfg,
		Reason: ReasonStartup,
	})
	if err != nil {
		t.Fatalf("Build(first): %v", err)
	}

	var semantics map[string]string
	if err := json.Unmarshal(first.Semantics, &semantics); err != nil {
		t.Fatalf("unmarshal semantics: %v", err)
	}
	if semantics["baggage_durability"] != "author_explicit_claims" {
		t.Fatalf("baggage_durability = %q", semantics["baggage_durability"])
	}
	if semantics["baggage_immutability"] != "deep_accretion_immutable_paths" {
		t.Fatalf("baggage_immutability = %q", semantics["baggage_immutability"])
	}
	if semantics["baggage_transition"] != "explicit_claims_only" {
		t.Fatalf("baggage_transition = %q", semantics["baggage_transition"])
	}
	if semantics["retry_policy_owner"] != "core" {
		t.Fatalf("retry_policy_owner = %q", semantics["retry_policy_owner"])
	}
	if semantics["plugin_retry_field"] != "v2_boundary_compatibility_signal" {
		t.Fatalf("plugin_retry_field = %q", semantics["plugin_retry_field"])
	}

	var sanitized map[string]any
	if err := json.Unmarshal(first.SanitizedConfig, &sanitized); err != nil {
		t.Fatalf("unmarshal sanitized config: %v", err)
	}
	pipelines := sanitized["pipelines"].([]any)
	steps := pipelines[0].(map[string]any)["steps"].([]any)
	baggage := steps[0].(map[string]any)["baggage"].(map[string]any)
	if baggage["summary.text"] != "payload.result" {
		t.Fatalf("summary.text baggage = %#v", baggage["summary.text"])
	}
	if baggage["namespace"] != "whisper" {
		t.Fatalf("namespace baggage = %#v", baggage["namespace"])
	}

	cfg.Pipelines[0].Steps[0].Baggage["summary.text"] = "payload.summary"
	second, err := Build(BuildInput{
		Config: cfg,
		Reason: ReasonStartup,
	})
	if err != nil {
		t.Fatalf("Build(second): %v", err)
	}
	if first.ConfigHash == second.ConfigHash {
		t.Fatal("config hash did not change after baggage-only change")
	}
}

func TestInsertAndGetRoundTrip(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Defaults()
	snapshot, err := Build(BuildInput{
		Config:         cfg,
		Reason:         ReasonStartup,
		DuctileVersion: "test-version",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := Insert(context.Background(), db, snapshot); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := Get(context.Background(), db, snapshot.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != snapshot.ID || got.ConfigHash != snapshot.ConfigHash || got.Reason != ReasonStartup {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, snapshot)
	}
}

// TestRenderTimeoutsIncludesOverrides asserts the snapshot renders
// operator-defined per-command timeouts (TimeoutsConfig.Overrides) alongside
// the four core lifecycle fields, so the audit view matches what
// Dispatcher.getTimeout actually enforces (P2-05 follow-up).
func TestRenderTimeoutsIncludesOverrides(t *testing.T) {
	t.Parallel()

	got := renderTimeouts(&config.TimeoutsConfig{
		Poll:   10 * time.Second,
		Handle: 30 * time.Second,
		Overrides: map[string]time.Duration{
			"cpu": 15 * time.Second,
			"io":  5 * time.Second,
		},
	})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("renderTimeouts returned %T, want map[string]any", got)
	}

	if m["poll"] != "10s" {
		t.Errorf("poll = %v, want 10s", m["poll"])
	}
	if m["handle"] != "30s" {
		t.Errorf("handle = %v, want 30s", m["handle"])
	}
	if m["cpu"] != "15s" {
		t.Errorf("cpu override missing from snapshot: %v", m["cpu"])
	}
	if m["io"] != "5s" {
		t.Errorf("io override missing from snapshot: %v", m["io"])
	}
}

// TestRenderTimeoutsOmitsZeroOverrides asserts a zero-valued override is not
// surfaced in the snapshot, matching dispatcher behavior (the > 0 guard at
// Dispatcher.getTimeout means a zero override falls through to the named
// field or the 60s default — it is effectively unused).
func TestRenderTimeoutsOmitsZeroOverrides(t *testing.T) {
	t.Parallel()

	got := renderTimeouts(&config.TimeoutsConfig{
		Poll:      10 * time.Second,
		Overrides: map[string]time.Duration{"cpu": 0},
	})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("renderTimeouts returned %T, want map[string]any", got)
	}
	if _, present := m["cpu"]; present {
		t.Errorf("zero override surfaced in snapshot: %v", m["cpu"])
	}
}

// TestRenderTimeoutsOverrideOfNamedFieldWinsMatchesDispatcher asserts that
// when an operator (or a programmatic caller) sets Overrides[poll]=5s while
// Poll=60s, the snapshot shows 5s for poll — matching Dispatcher.getTimeout
// which checks Overrides[command] before the named field.
func TestRenderTimeoutsOverrideOfNamedFieldWinsMatchesDispatcher(t *testing.T) {
	t.Parallel()

	got := renderTimeouts(&config.TimeoutsConfig{
		Poll:      60 * time.Second,
		Overrides: map[string]time.Duration{"poll": 5 * time.Second},
	})
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("renderTimeouts returned %T, want map[string]any", got)
	}
	if m["poll"] != "5s" {
		t.Errorf("poll = %v, want 5s (Overrides should win in snapshot, matching dispatcher)", m["poll"])
	}
}
