package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		env     map[string]string
		wantErr bool
		checkFn func(t *testing.T, cfg *Config)
	}{
		{
			name: "minimal valid config",
			yaml: `
service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    schedules:
      - id: default
        every: 5m
        jitter: 30s
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Service.TickInterval != 60*time.Second {
					t.Error("tick_interval not parsed")
				}
				if !filepath.IsAbs(cfg.State.Path) {
					t.Errorf("state.path should be absolute, got %s", cfg.State.Path)
				}
				if filepath.Base(cfg.State.Path) != "test.db" {
					t.Errorf("state.path not resolved to test.db: %s", cfg.State.Path)
				}
				echo, ok := cfg.Plugins["echo"]
				if !ok {
					t.Fatal("echo plugin not found")
				}
				if !echo.Enabled {
					t.Error("echo not enabled")
				}
				if len(echo.Schedules) != 1 {
					t.Fatalf("expected 1 schedule, got %d", len(echo.Schedules))
				}
				if echo.Schedules[0].Every != "5m" {
					t.Error("schedules[0].every not parsed")
				}
				// Check defaults applied
				expectedWorkers := max(1, runtime.NumCPU()-1)
				if cfg.Service.MaxWorkers != expectedWorkers {
					t.Errorf("default service.max_workers not applied: got %d, want %d", cfg.Service.MaxWorkers, expectedWorkers)
				}
				if echo.Retry == nil || echo.Retry.MaxAttempts != 4 {
					t.Error("default retry config not applied")
				}
				if echo.Parallelism != expectedWorkers {
					t.Errorf("default plugin parallelism not applied: got %d, want %d", echo.Parallelism, expectedWorkers)
				}
			},
		},
		{
			name: "api token via secret_ref loads (vault resolution deferred to runtime)",
			yaml: `
service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: "localhost:8081"
  auth:
    tokens:
      - secret_ref: ductile-api-admin
        scopes: ["*"]
plugins:
  echo:
    enabled: true
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				if len(cfg.API.Auth.Tokens) != 1 {
					t.Fatalf("expected 1 api token, got %d", len(cfg.API.Auth.Tokens))
				}
				if cfg.API.Auth.Tokens[0].SecretRef != "ductile-api-admin" {
					t.Errorf("secret_ref not preserved: %q", cfg.API.Auth.Tokens[0].SecretRef)
				}
				if cfg.API.Auth.Tokens[0].Token != "" {
					t.Errorf("a secret_ref token must not carry a literal Token, got %q", cfg.API.Auth.Tokens[0].Token)
				}
			},
		},
		{
			name: "api token with neither token nor secret_ref is rejected at load",
			yaml: `
service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: "localhost:8081"
  auth:
    tokens:
      - scopes: ["*"]
plugins:
  echo:
    enabled: true
`,
			wantErr: true,
		},
		{
			name: "api token with a literal value is rejected at load (vault-only, #94)",
			yaml: `
service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: "localhost:8081"
  auth:
    tokens:
      - token: deadbeefdeadbeefdeadbeefdeadbeef
        scopes: ["*"]
plugins:
  echo:
    enabled: true
`,
			wantErr: true,
		},
		{
			name: "plugin_roots parsed",
			yaml: `
service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./external-plugins
  - /opt/ductile-plugins
plugins:
  echo:
    enabled: true
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				roots := cfg.EffectivePluginRoots()
				if len(roots) != 2 {
					t.Fatalf("expected 2 effective roots, got %d", len(roots))
				}
				if roots[0] != "./external-plugins" || roots[1] != "/opt/ductile-plugins" {
					t.Fatalf("unexpected effective roots: %v", roots)
				}
			},
		},
		{
			name: "service max_workers and plugin parallelism parsed",
			yaml: `
service:
  tick_interval: 60s
  max_workers: 4
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    parallelism: 3
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.Service.MaxWorkers != 4 {
					t.Fatalf("service.max_workers = %d, want 4", cfg.Service.MaxWorkers)
				}
				echo := cfg.Plugins["echo"]
				if echo.Parallelism != 3 {
					t.Fatalf("echo.parallelism = %d, want 3", echo.Parallelism)
				}
			},
		},
		{
			name: "plugin parallelism over service max_workers fails validation",
			yaml: `
service:
  tick_interval: 60s
  max_workers: 2
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    parallelism: 3
`,
			wantErr: true,
		},
		{
			name: "plugin parallelism below 1 fails validation",
			yaml: `
service:
  tick_interval: 60s
  max_workers: 2
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    parallelism: -1
`,
			wantErr: true,
		},
		{
			name: "invalid plugin_roots entries fail validation",
			yaml: `
service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ""
  - "   "
plugins:
  echo:
    enabled: true
`,
			wantErr: true,
		},
		{
			name: "env var interpolation",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ${DB_PATH}
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: hourly
    config:
      api_key: ${API_KEY}
      endpoint: ${API_ENDPOINT}
`,
			env: map[string]string{
				"DB_PATH":      "/tmp/test.db",
				"API_KEY":      "secret123",
				"API_ENDPOINT": "https://api.example.com",
			},
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				if cfg.State.Path != "/tmp/test.db" {
					t.Errorf("env var not interpolated in state.path: %s", cfg.State.Path)
				}
				test := cfg.Plugins["test"]
				if test.Config["api_key"] != "secret123" {
					t.Error("env var not interpolated in plugin config")
				}
				if test.Config["endpoint"] != "https://api.example.com" {
					t.Error("env var not interpolated in plugin config")
				}
			},
		},
		{
			name: "missing env var fails validation",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: daily
    config:
      secret_ref: ${MISSING_VAR}
`,
			env:     map[string]string{}, // MISSING_VAR not set
			wantErr: true,
		},
		{
			name: "invalid log level",
			yaml: `
service:
  tick_interval: 30s
  log_level: invalid
state:
  path: ./test.db
plugin_roots:
  - ./plugins
`,
			wantErr: true,
		},
		{
			name: "enabled plugin without schedule is valid (API-triggered only)",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				test := cfg.Plugins["test"]
				if !test.Enabled {
					t.Error("plugin should be enabled")
				}
				if len(test.Schedules) != 0 {
					t.Error("plugin schedules should be empty when omitted")
				}
			},
		},
		{
			name: "schedule entry requires every or cron",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - {}
`,
			wantErr: true,
		},
		{
			name: "cron schedule is valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - cron: "*/15 * * * *"
`,
			wantErr: false,
		},
		{
			name: "invalid cron expression",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - cron: "61 * * * *"
`,
			wantErr: true,
		},
		{
			name: "schedule cannot set both every and cron",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        cron: "*/15 * * * *"
`,
			wantErr: true,
		},
		{
			name: "schedule constraints are valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        only_between: "08:00-22:00"
        timezone: "Australia/Sydney"
        not_on: [saturday, 0]
`,
			wantErr: false,
		},
		{
			name: "schedule invalid timezone",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        timezone: "Mars/Phobos"
`,
			wantErr: true,
		},
		{
			name: "schedule invalid only_between format",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        only_between: "8am-10pm"
`,
			wantErr: true,
		},
		{
			name: "schedule invalid not_on token",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        not_on: [funday]
`,
			wantErr: true,
		},
		{
			name: "schedule catch_up run_once is valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        catch_up: run_once
`,
			wantErr: false,
		},
		{
			name: "schedule catch_up invalid value",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        catch_up: replay
`,
			wantErr: true,
		},
		{
			name: "schedule catch_up run_all not allowed with cron",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - cron: "*/15 * * * *"
        catch_up: run_all
`,
			wantErr: true,
		},
		{
			name: "schedule if_running cancel is valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        if_running: cancel
`,
			wantErr: false,
		},
		{
			name: "schedule if_running invalid value",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        if_running: replace
`,
			wantErr: true,
		},
		{
			name: "one-shot at schedule is valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - at: "2026-03-15T14:00:00Z"
`,
			wantErr: false,
		},
		{
			name: "one-shot after schedule is valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - after: 2h
`,
			wantErr: false,
		},
		{
			name: "one-shot at invalid timestamp",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - at: "tomorrow"
`,
			wantErr: true,
		},
		{
			name: "invalid schedule interval",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: invalid
`,
			wantErr: true,
		},
		{
			name: "legacy schedule is unsupported",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedule:
      every: 5m
`,
			wantErr: true,
		},
		{
			name: "schedules entries default id",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 1h
        command: token_refresh
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				schedules := cfg.Plugins["test"].NormalizedSchedules()
				if len(schedules) != 1 {
					t.Fatalf("expected 1 schedule, got %d", len(schedules))
				}
				if schedules[0].ID != "default" {
					t.Fatalf("expected default schedule id, got %q", schedules[0].ID)
				}
			},
		},
		{
			name: "scheduled handle is invalid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
        command: handle
`,
			wantErr: true,
		},
		{
			name: "multiple schedules are valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - id: refresh
        every: 1h
        command: token_refresh
      - id: poll
        every: 15m
        command: poll
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				test := cfg.Plugins["test"]
				if len(test.Schedules) != 2 {
					t.Fatalf("expected 2 schedules, got %d", len(test.Schedules))
				}
			},
		},
		{
			name: "custom schedule interval",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: true
    schedules:
      - every: 7m
`,
			wantErr: false,
		},
		{
			name: "disabled plugin skips validation",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  test:
    enabled: false
`,
			wantErr: false,
			checkFn: func(t *testing.T, cfg *Config) {
				test := cfg.Plugins["test"]
				if test.Enabled {
					t.Error("plugin should be disabled")
				}
			},
		},
		{
			name: "api token empty scopes is valid (discovery-only token)",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: 127.0.0.1:8080
  auth:
    tokens:
      - secret_ref: ductile-api-readonly
        scopes: []
plugins:
  test:
    enabled: false
`,
			wantErr: false,
		},
		{
			name: "api token with scopes is valid",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: 127.0.0.1:8080
  auth:
    tokens:
      - secret_ref: ductile-api-readonly
        scopes: [plugin:ro]
plugins:
  test:
    enabled: false
`,
			wantErr: false,
		},
		{
			name: "api token with a literal ${ENV} value is rejected (vault-only, #94)",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: 127.0.0.1:8080
  auth:
    tokens:
      - token: ${MISSING_TOKEN}
        scopes: [plugin:ro]
plugins:
  test:
    enabled: false
`,
			wantErr: true,
		},
		{
			name: "api token via ${ENV} is rejected even when the var is set (literal, vault-only #94)",
			yaml: `
service:
  tick_interval: 30s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
api:
  enabled: true
  listen: 127.0.0.1:8080
  auth:
    tokens:
      - token: ${TOKEN}
        scopes: [plugin:ro]
plugins:
  test:
    enabled: false
`,
			env: map[string]string{
				"TOKEN": "ro-token",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			for k, v := range tt.env {
				if err := os.Setenv(k, v); err != nil {
					t.Fatalf("set env %s: %v", k, err)
				}
				k := k
				t.Cleanup(func() { _ = os.Unsetenv(k) })
			}

			// Create temp config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tt.yaml), 0644); err != nil {
				t.Fatalf("failed to write test config: %v", err)
			}

			// Load config
			cfg, err := Load(configPath)

			if (err != nil) != tt.wantErr {
				t.Errorf("Load() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && tt.checkFn != nil {
				tt.checkFn(t, cfg)
			}
		})
	}
}

func TestInterpolateEnv(t *testing.T) {
	tests := []struct {
		name  string
		input string
		env   map[string]string
		want  string
	}{
		{
			name:  "simple replacement",
			input: "path: ${HOME}/data",
			env:   map[string]string{"HOME": "/users/test"},
			want:  "path: /users/test/data",
		},
		{
			name:  "multiple vars",
			input: "${USER}:${PASS}@${HOST}",
			env: map[string]string{
				"USER": "admin",
				"PASS": "secret",
				"HOST": "localhost",
			},
			want: "admin:secret@localhost",
		},
		{
			name:  "undefined var unchanged",
			input: "key: ${UNDEFINED}",
			env:   map[string]string{},
			want:  "key: ${UNDEFINED}",
		},
		{
			name:  "no vars",
			input: "plain text",
			env:   map[string]string{},
			want:  "plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up environment
			for k, v := range tt.env {
				if err := os.Setenv(k, v); err != nil {
					t.Fatalf("set env %s: %v", k, err)
				}
				k := k
				t.Cleanup(func() { _ = os.Unsetenv(k) })
			}

			got := interpolateEnv(tt.input)
			if got != tt.want {
				t.Errorf("interpolateEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseInterval(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"5 minutes", "5m", 5 * time.Minute, false},
		{"15 minutes", "15m", 15 * time.Minute, false},
		{"30 minutes", "30m", 30 * time.Minute, false},
		{"7 minutes", "7m", 7 * time.Minute, false},
		{"hourly", "hourly", 1 * time.Hour, false},
		{"13 hours", "13h", 13 * time.Hour, false},
		{"2 hours", "2h", 2 * time.Hour, false},
		{"6 hours", "6h", 6 * time.Hour, false},
		{"3 days", "3d", 3 * 24 * time.Hour, false},
		{"2 weeks", "2w", 14 * 24 * time.Hour, false},
		{"daily", "daily", 24 * time.Hour, false},
		{"weekly", "weekly", 7 * 24 * time.Hour, false},
		{"monthly", "monthly", 30 * 24 * time.Hour, false},
		{"invalid", "invalid", 0, true},
		{"negative", "-5m", 0, true},
		{"zero", "0s", 0, true},
		{"invalid days", "xd", 0, true},
		{"invalid weeks", "1.2.3w", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInterval(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseInterval() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseInterval() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *Config
		wantErr bool
	}{
		{
			name: "valid config",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 60 * time.Second,
					LogLevel:     "info",
					MaxWorkers:   1,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
				Plugins: map[string]PluginConf{
					"test": {
						Enabled: true,
						Schedules: []ScheduleConfig{
							{Every: "hourly"},
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "negative tick interval",
			cfg: &Config{
				Service:     ServiceConfig{TickInterval: -1},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			// P2-10: tick_interval below the floor (sub-100ms) is a sanity guard against
			// hostile or accidental tight ticks that flood the work loop.
			name: "tick interval below 100ms floor",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 50 * time.Millisecond,
					LogLevel:     "info",
					MaxWorkers:   1,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			// P2-10: the floor itself is accepted.
			name: "tick interval at 100ms floor",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 100 * time.Millisecond,
					LogLevel:     "info",
					MaxWorkers:   1,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: false,
		},
		{
			// P2-11: negative hook_max_depth is rejected (0 is reserved for "use default").
			name: "negative hook_max_depth",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 60 * time.Second,
					LogLevel:     "info",
					MaxWorkers:   1,
					HookMaxDepth: -1,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			name: "invalid max workers",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 60 * time.Second,
					LogLevel:     "info",
					MaxWorkers:   0,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			name: "plugin parallelism exceeds max workers",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 60 * time.Second,
					LogLevel:     "info",
					MaxWorkers:   2,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
				Plugins: map[string]PluginConf{
					"test": {
						Enabled:     true,
						Parallelism: 3,
					},
				},
			},
			wantErr: true,
		},
		{
			name: "job queue retention equals dedupe ttl",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval:      60 * time.Second,
					LogLevel:          "info",
					MaxWorkers:        1,
					DedupeTTL:         24 * time.Hour,
					JobQueueRetention: 24 * time.Hour,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: false,
		},
		{
			name: "job queue retention longer than dedupe ttl",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval:      60 * time.Second,
					LogLevel:          "info",
					MaxWorkers:        1,
					DedupeTTL:         12 * time.Hour,
					JobQueueRetention: 24 * time.Hour,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: false,
		},
		{
			name: "job queue retention shorter than dedupe ttl",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval:      60 * time.Second,
					LogLevel:          "info",
					MaxWorkers:        1,
					DedupeTTL:         24 * time.Hour,
					JobQueueRetention: time.Hour,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			name: "invalid log level",
			cfg: &Config{
				Service: ServiceConfig{
					TickInterval: 60 * time.Second,
					LogLevel:     "trace",
					MaxWorkers:   1,
				},
				State:       StateConfig{Path: "./test.db"},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			name: "missing state path",
			cfg: &Config{
				Service:     ServiceConfig{TickInterval: 60 * time.Second, LogLevel: "info"},
				State:       StateConfig{},
				PluginRoots: []string{"./plugins"},
			},
			wantErr: true,
		},
		{
			name: "missing plugin_roots",
			cfg: &Config{
				Service: ServiceConfig{TickInterval: 60 * time.Second, LogLevel: "info"},
				State:   StateConfig{Path: "./test.db"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// hasVault=false: these cases exercise non-bootstrap validation, so the
			// api-enabled-needs-token rule applies as before (the vault-present
			// bootstrap exception is covered in boot_posture_test.go).
			err := validate(tt.cfg, false)
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestLoadMultiFileWithInclude tests loading configuration using include array.
func TestLoadMultiFileWithInclude(t *testing.T) {
	tmpDir := t.TempDir()

	// Create main config.yaml with include array
	configYAML := `
include:
  - plugins.yaml

service:
  name: ductile
  tick_interval: 60s
  log_level: info
state:
  path: /var/lib/ductile/state.db
plugin_roots:
  - /usr/local/lib/ductile/plugins
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create plugins.yaml
	pluginsYAML := `
plugins:
  echo:
    enabled: true
    schedules:
      - every: 5m
        jitter: 30s
`
	if err := os.WriteFile(filepath.Join(tmpDir, "plugins.yaml"), []byte(pluginsYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load config
	configPath := filepath.Join(tmpDir, "config.yaml")
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify config loaded correctly
	if cfg.Service.Name != "ductile" {
		t.Errorf("Service.Name = %q, want %q", cfg.Service.Name, "ductile")
	}
	if cfg.State.Path != "/var/lib/ductile/state.db" {
		t.Errorf("State.Path = %q", cfg.State.Path)
	}
	if _, ok := cfg.Plugins["echo"]; !ok {
		t.Error("echo plugin not loaded from included file")
	}
}

// TestDeepMergeConfigAPIFields guards the deepMergeConfig API merge: an api: block
// from an INCLUDED file must carry every field through, not just the few the merge
// once hand-listed. management_socket (#129) and allowed_origins were silently
// dropped from multi-file configs — single-file loads never exercised the merge.
func TestDeepMergeConfigAPIFields(t *testing.T) {
	dst := &Config{}
	src := &Config{API: APIConfig{
		Enabled:           true,
		Listen:            "127.0.0.1:18181",
		Auth:              APIAuthConfig{Tokens: []APIToken{{SecretRef: "api-admin"}}},
		MaxConcurrentSync: 7,
		MaxSyncTimeout:    42 * time.Second,
		AllowedOrigins:    []string{"https://dash.example"},
		ManagementSocket:  "/tmp/ductile-mgmt.sock",
	}}
	if err := deepMergeConfig(dst, src); err != nil {
		t.Fatalf("deepMergeConfig: %v", err)
	}
	if !dst.API.Enabled || dst.API.Listen != "127.0.0.1:18181" {
		t.Errorf("api enabled/listen not merged: %+v", dst.API)
	}
	if len(dst.API.Auth.Tokens) != 1 || dst.API.Auth.Tokens[0].SecretRef != "api-admin" {
		t.Errorf("API.Auth.Tokens = %+v, want the secret_ref carried through the merge", dst.API.Auth.Tokens)
	}
	if dst.API.MaxConcurrentSync != 7 {
		t.Errorf("API.MaxConcurrentSync = %d, want it carried through the merge", dst.API.MaxConcurrentSync)
	}
	if dst.API.MaxSyncTimeout != 42*time.Second {
		t.Errorf("API.MaxSyncTimeout = %v, want it carried through the merge", dst.API.MaxSyncTimeout)
	}
	if len(dst.API.AllowedOrigins) != 1 || dst.API.AllowedOrigins[0] != "https://dash.example" {
		t.Errorf("API.AllowedOrigins = %v, want it carried through the merge", dst.API.AllowedOrigins)
	}
	if dst.API.ManagementSocket != "/tmp/ductile-mgmt.sock" {
		t.Errorf("API.ManagementSocket = %q, want it carried through the merge", dst.API.ManagementSocket)
	}
}

func TestLoadIncludeDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	includeDir := filepath.Join(tmpDir, "includes")
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		t.Fatal(err)
	}

	configYAML := `
include:
  - includes

service:
  tick_interval: 60s
  log_level: info
state:
  path: ./test.db
plugin_roots:
  - ./plugins
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	pluginsA := `
plugins:
  alpha:
    enabled: true
`
	pluginsB := `
plugins:
  beta:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(includeDir, "a.yaml"), []byte(pluginsA), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(includeDir, "b.yaml"), []byte(pluginsB), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(filepath.Join(tmpDir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if _, ok := cfg.Plugins["alpha"]; !ok {
		t.Error("alpha plugin not loaded from include directory")
	}
	if _, ok := cfg.Plugins["beta"]; !ok {
		t.Error("beta plugin not loaded from include directory")
	}
}

func TestLoadEnvironmentVarsInclude(t *testing.T) {
	tmpDir := t.TempDir()
	envPath := filepath.Join(tmpDir, ".env")
	if err := os.WriteFile(envPath, []byte("DUCTILE_TEST_NAME=envtest\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("DUCTILE_TEST_NAME"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("DUCTILE_TEST_NAME") })

	configYAML := `
environment_vars:
  include:
    - .env

service:
  name: ${DUCTILE_TEST_NAME}
  tick_interval: 60s
  log_level: info
state:
  path: ./test.db
plugin_roots:
  - ./plugins
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(filepath.Join(tmpDir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Service.Name != "envtest" {
		t.Fatalf("Service.Name = %q, want %q", cfg.Service.Name, "envtest")
	}
}

// TestLoadMissingIncludeFile tests hard fail when included file is missing.
func TestLoadMissingIncludeFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create config.yaml with include to non-existent file
	configYAML := `
include:
  - missing.yaml

service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load should fail with good error message
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() should have failed with missing include file")
	}

	// Check error message is helpful
	errMsg := err.Error()
	if !contains(errMsg, "file not found") {
		t.Errorf("Error message should mention 'file not found', got: %v", errMsg)
	}
	if !contains(errMsg, "Hint") {
		t.Errorf("Error message should contain hint, got: %v", errMsg)
	}
}

// TestLoadAbsoluteIncludePath tests include with absolute path.
func TestLoadAbsoluteIncludePath(t *testing.T) {
	tmpDir := t.TempDir()

	// Create plugins.yaml in temp dir
	pluginsYAML := `
plugins:
  test:
    enabled: true
    schedules:
      - every: 5m
`
	pluginsPath := filepath.Join(tmpDir, "plugins.yaml")
	if err := os.WriteFile(pluginsPath, []byte(pluginsYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create config.yaml with absolute path include
	configYAML := fmt.Sprintf(`
include:
  - %s

service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
`, pluginsPath)

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load should succeed
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if _, ok := cfg.Plugins["test"]; !ok {
		t.Error("test plugin not loaded from absolute path include")
	}
}

// TestLoadCircularInclude tests cycle detection in includes.
func TestLoadCircularInclude(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a.yaml that includes b.yaml
	aYAML := `
include:
  - b.yaml
service:
  tick_interval: 60s
`
	if err := os.WriteFile(filepath.Join(tmpDir, "a.yaml"), []byte(aYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create b.yaml that includes a.yaml (circular)
	bYAML := `
include:
  - a.yaml
state:
  path: ./test.db
`
	if err := os.WriteFile(filepath.Join(tmpDir, "b.yaml"), []byte(bYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create main config
	configYAML := `
include:
  - a.yaml
plugin_roots:
  - ./plugins
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load should fail with cycle detection
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() should have failed with circular dependency")
	}

	if !contains(err.Error(), "circular dependency") {
		t.Errorf("Error should mention circular dependency, got: %v", err)
	}
}

// TestLoadCircularIncludeThroughRoot guards C-FRO-13. Verification note: the
// claimed *silent token doubling* impact was NOT reproduced — the existing
// child-file visited tracking already trips cycle detection and Load fails
// safely (cfg=nil). The real defect is that the root config's own path was
// not seeded into the visited set, so the cycle was caught one level too
// late (after a wasted re-merge, with the error naming the child instead of
// the root). This test pins the safe behaviour: an include cycle that runs
// back through the root config is rejected with a circular-dependency error.
func TestLoadCircularIncludeThroughRoot(t *testing.T) {
	tmpDir := t.TempDir()

	// child.yaml includes the root config.yaml back -> cycle through root.
	childYAML := `
include:
  - config.yaml
state:
  path: ./test.db
`
	if err := os.WriteFile(filepath.Join(tmpDir, "child.yaml"), []byte(childYAML), 0644); err != nil {
		t.Fatal(err)
	}

	configYAML := `
include:
  - child.yaml
plugin_roots:
  - ./plugins
api:
  auth:
    tokens:
      - token: root-secret
        scopes: ["admin"]
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Pre-fix: Load succeeds and the root is merged twice (tokens doubled).
	// Post-fix: the cycle back through root is detected like any other.
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() should have failed: include cycle through the root config")
	}
	if !contains(err.Error(), "circular dependency") {
		t.Errorf("error should mention circular dependency, got: %v", err)
	}
}

// contains checks if a string contains a substring (helper for tests).
func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) &&
		(func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		})()
}

// TestHashVerification tests BLAKE3 hash verification for scope files.
func TestHashVerification(t *testing.T) {
	tmpDir := t.TempDir()

	// webhooks.yaml is the recognized scope file (hash-verified on load).
	webhooksYAML := "webhooks:\n  endpoints: []\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "webhooks.yaml"), []byte(webhooksYAML), 0600); err != nil {
		t.Fatal(err)
	}

	// Create config with include for webhooks.yaml
	configYAML := `
include:
  - webhooks.yaml

service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    schedules:
      - every: 5m
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Generate checksums for the scope file
	if _, err := GenerateChecksumsWithReport(tmpDir, []string{"webhooks.yaml"}, false); err != nil {
		t.Fatal(err)
	}

	// Load should succeed with correct hash
	if _, err := Load(configPath); err != nil {
		t.Fatalf("Load() with correct hash failed: %v", err)
	}

	// Tamper with the scope file
	if err := os.WriteFile(filepath.Join(tmpDir, "webhooks.yaml"), []byte(webhooksYAML+"# tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Load should fail with hash mismatch
	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() should have failed with hash mismatch")
	}

	// Check error message is helpful
	if !contains(err.Error(), "verification failed") {
		t.Errorf("Error should mention verification failed, got: %v", err)
	}
}

func TestDiscoverScopeDirsFromIncludes(t *testing.T) {
	tmpDir := t.TempDir()

	configDir := filepath.Join(tmpDir, "config")
	hooksDir := filepath.Join(tmpDir, "hooks")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}

	mainYAML := `
include:
  - base.yaml
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(mainYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// webhooks.yaml is the recognized scope file; it lives in a sibling dir,
	// reached via a nested include, to prove scope-dir discovery walks includes.
	baseYAML := `
include:
  - ../hooks/webhooks.yaml
`
	if err := os.WriteFile(filepath.Join(configDir, "base.yaml"), []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(hooksDir, "webhooks.yaml"), []byte("webhooks:\n  endpoints: []\n"), 0600); err != nil {
		t.Fatal(err)
	}

	dirs, err := DiscoverScopeDirs(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatalf("DiscoverScopeDirs() failed: %v", err)
	}

	if len(dirs) != 1 {
		t.Fatalf("DiscoverScopeDirs() returned %d dirs, want 1: %v", len(dirs), dirs)
	}
	if dirs[0] != hooksDir {
		t.Errorf("DiscoverScopeDirs() = %s, want hooks dir %s", dirs[0], hooksDir)
	}
}

func TestDiscoverScopeDirsFallbackToRootDir(t *testing.T) {
	tmpDir := t.TempDir()

	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
service:
  tick_interval: 60s
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	dirs, err := DiscoverScopeDirs(configPath)
	if err != nil {
		t.Fatalf("DiscoverScopeDirs() failed: %v", err)
	}

	if len(dirs) != 1 {
		t.Fatalf("DiscoverScopeDirs() returned %d dirs, want 1: %v", len(dirs), dirs)
	}
	if dirs[0] != tmpDir {
		t.Errorf("DiscoverScopeDirs() = %q, want %q", dirs[0], tmpDir)
	}
}

func TestLoadFailsWhenWebhookSecretRefTokenIsMissing(t *testing.T) {
	tmpDir := t.TempDir()

	configYAML := `
include:
  - tokens.yaml
  - webhooks.yaml

service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
webhooks:
  listen: "127.0.0.1:8091"
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	webhooksYAML := `
webhooks:
  endpoints:
    - path: /webhook/github
      plugin: echo
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
`
	if err := os.WriteFile(filepath.Join(tmpDir, "webhooks.yaml"), []byte(webhooksYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := GenerateChecksumsWithReport(tmpDir, []string{"tokens.yaml", "webhooks.yaml"}, false); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tmpDir)
	if err == nil {
		t.Fatal("Load() succeeded, want missing webhook token error")
	}
	if !contains(err.Error(), `secret_ref "github_webhook_secret" not found in the vault`) {
		t.Fatalf("Load() error = %v, want missing webhook token message", err)
	}
}

func TestLoadAcceptsIncludedStandaloneWebhooksFileNestedShape(t *testing.T) {
	tmpDir := t.TempDir()
	configYAML := `
include:
  - webhooks.yaml

service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
secrets:
  age_key_file: age.key
  vault_file: vault.age
plugins:
  echo:
    enabled: true
webhooks:
  listen: "127.0.0.1:8091"
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens:\n  - name: github_webhook_secret\n    key: test-secret\n    scopes_file: scopes/webhook.json\n    scopes_hash: blake3:dummy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	webhooksYAML := `
webhooks:
  endpoints:
    - path: /webhook/github
      plugin: echo
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
`
	if err := os.WriteFile(filepath.Join(tmpDir, "webhooks.yaml"), []byte(webhooksYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "scopes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "scopes", "webhook.json"), []byte("[\"*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateChecksumsWithReport(tmpDir, []string{"webhooks.yaml"}, false); err != nil {
		t.Fatal(err)
	}
	// The webhook secret now resolves from the vault (epic #48), not tokens.yaml.
	seedVault(t, tmpDir, map[string]string{"github_webhook_secret": "test-secret"})

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Webhooks == nil || len(cfg.Webhooks.Endpoints) != 1 {
		t.Fatalf("cfg.Webhooks.Endpoints = %v, want 1 endpoint", cfg.Webhooks)
	}
	if cfg.ResolvedSecrets["github_webhook_secret"] != "test-secret" {
		t.Fatalf("github_webhook_secret not resolved from vault: %v", cfg.ResolvedSecrets)
	}
}

func TestLoadFailsWhenWebhookSecretRefMissingFromIncludedTokensFile(t *testing.T) {
	tmpDir := t.TempDir()
	configYAML := `
include:
  - tokens.yaml
  - webhooks.yaml

service:
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
webhooks:
  listen: "127.0.0.1:8091"
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	webhooksYAML := `
webhooks:
  endpoints:
    - path: /webhook/github
      plugin: echo
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
`
	if err := os.WriteFile(filepath.Join(tmpDir, "webhooks.yaml"), []byte(webhooksYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateChecksumsWithReport(tmpDir, []string{"tokens.yaml", "webhooks.yaml"}, false); err != nil {
		t.Fatal(err)
	}

	_, err := Load(tmpDir)
	if err == nil {
		t.Fatal("Load() succeeded, want missing webhook token error")
	}
	if !contains(err.Error(), `secret_ref "github_webhook_secret" not found in the vault`) {
		t.Fatalf("Load() error = %v, want missing webhook token message", err)
	}
}

func TestLoadAcceptsRelayConfigIncludes(t *testing.T) {
	tmpDir := t.TempDir()
	configYAML := `
include:
  - relay-instances.yaml
  - relay-ingress.yaml

service:
  name: home-primary
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
secrets:
  age_key_file: age.key
  vault_file: vault.age
plugins:
  echo:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "scopes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "scopes", "relay.json"), []byte("[\"*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tokensYAML := `
tokens:
  - name: relay-lab-v1
    key: test-secret
    scopes_file: scopes/relay.json
    scopes_hash: blake3:dummy
`
	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte(tokensYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	instancesYAML := `
instances:
  - name: lab
    enabled: true
    base_url: https://lab.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
    key_id: v1
    timeout: 10s
    allow:
      - backup.ready
`
	if err := os.WriteFile(filepath.Join(tmpDir, "relay-instances.yaml"), []byte(instancesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	ingressYAML := `
remote_ingress:
  listen_path: /ingest/peer
  max_body_size: 1MB
  allowed_clock_skew: 5m
  require_key_id: true
  peers:
    - name: home-primary
      enabled: true
      secret_ref: relay-lab-v1
      key_id: v1
      accept:
        - backup.ready
      baggage:
        allow:
          - trace_id
`
	if err := os.WriteFile(filepath.Join(tmpDir, "relay-ingress.yaml"), []byte(ingressYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := GenerateChecksumsWithReport(tmpDir, []string{"tokens.yaml"}, false); err != nil {
		t.Fatal(err)
	}

	seedVault(t, tmpDir, map[string]string{"relay-lab-v1": "test-secret"})

	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if len(cfg.RelayInstances) != 1 || cfg.RelayInstances[0].Name != "lab" {
		t.Fatalf("cfg.RelayInstances = %+v, want lab", cfg.RelayInstances)
	}
	if cfg.RemoteIngress == nil || cfg.RemoteIngress.ListenPath != "/ingest/peer" {
		t.Fatalf("cfg.RemoteIngress = %+v, want listen_path /ingest/peer", cfg.RemoteIngress)
	}
	if len(cfg.RemoteIngress.TrustedPeers) != 1 || cfg.RemoteIngress.TrustedPeers[0].Name != "home-primary" {
		t.Fatalf("cfg.RemoteIngress.TrustedPeers = %+v, want home-primary", cfg.RemoteIngress.TrustedPeers)
	}
}

func TestLoadRejectsDuplicateRelayInstanceNames(t *testing.T) {
	tmpDir := t.TempDir()
	configYAML := `
include:
  - relay-instances.yaml

service:
  name: home-primary
  tick_interval: 60s
state:
  path: ./test.db
plugin_roots:
  - ./plugins
secrets:
  age_key_file: age.key
  vault_file: vault.age
plugins:
  echo:
    enabled: true
`
	if err := os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "scopes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "scopes", "relay.json"), []byte("[\"*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokensYAML := `
tokens:
  - name: relay-lab-v1
    key: test-secret
    scopes_file: scopes/relay.json
    scopes_hash: blake3:dummy
`
	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte(tokensYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	instancesYAML := `
instances:
  - name: lab
    enabled: true
    base_url: https://lab-a.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
  - name: lab
    enabled: true
    base_url: https://lab-b.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
`
	if err := os.WriteFile(filepath.Join(tmpDir, "relay-instances.yaml"), []byte(instancesYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateChecksumsWithReport(tmpDir, []string{"tokens.yaml"}, false); err != nil {
		t.Fatal(err)
	}

	seedVault(t, tmpDir, map[string]string{"relay-lab-v1": "test-secret"})

	_, err := Load(tmpDir)
	if err == nil {
		t.Fatal("Load() succeeded, want duplicate relay instance error")
	}
	if !contains(err.Error(), `duplicate relay instance name "lab"`) {
		t.Fatalf("Load() error = %v, want duplicate relay instance message", err)
	}
}

// TestDeepMerge tests deep merging of included configs.
func TestDeepMerge(t *testing.T) {
	tmpDir := t.TempDir()

	// Create base.yaml with some defaults
	baseYAML := `
service:
  name: ductile
  tick_interval: 60s
  log_level: info
`
	if err := os.WriteFile(filepath.Join(tmpDir, "base.yaml"), []byte(baseYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create override.yaml that overrides log_level
	overrideYAML := `
service:
  log_level: debug
`
	if err := os.WriteFile(filepath.Join(tmpDir, "override.yaml"), []byte(overrideYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create main config that includes both
	configYAML := `
include:
  - base.yaml
  - override.yaml

state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    schedules:
      - every: 5m
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load config
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	// Verify merge: should have name from base, log_level from override
	if cfg.Service.Name != "ductile" {
		t.Errorf("Service.Name = %q, want %q", cfg.Service.Name, "ductile")
	}
	if cfg.Service.LogLevel != "debug" {
		t.Errorf("Service.LogLevel = %q, want %q (should be overridden)", cfg.Service.LogLevel, "debug")
	}
	if cfg.Service.TickInterval != 60*time.Second {
		t.Error("Service.TickInterval should be preserved from base")
	}
}

// TestLegacySingleFile ensures backward compatibility with single config.yaml.
func TestLegacySingleFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a single config.yaml with all settings
	legacyYAML := `
service:
  tick_interval: 60s
  log_level: info
state:
  path: ./test.db
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    schedules:
      - every: 5m
`
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(legacyYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Load should work with single file (legacy mode)
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() legacy mode failed: %v", err)
	}

	// Verify config loaded correctly
	if cfg.Service.TickInterval != 60*time.Second {
		t.Error("tick_interval not parsed in legacy mode")
	}
	if _, ok := cfg.Plugins["echo"]; !ok {
		t.Error("echo plugin not loaded in legacy mode")
	}
}

// Tilde expansion (#143 doc-audit follow-up): a leading "~/" or bare "~" in a
// path field expands to the user's home directory at load. "~user" forms are
// left untouched. Behavior is observable through Load alone.
func TestLoadExpandsTildeInStatePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	cfg := `
service:
  name: test
state:
  path: ~/ductile-tilde-test/state.db
plugin_roots:
  - ./plugins
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, "ductile-tilde-test", "state.db")
	if loaded.State.Path != want {
		t.Errorf("state.path: want %q, got %q", want, loaded.State.Path)
	}
}

func TestLoadExpandsTildeInPluginRootsAndPathFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()

	// The configured age key and env include must exist at their EXPANDED
	// locations — being found there is itself the assertion that expansion
	// runs before the loader's existence checks.
	tildeDir := filepath.Join(home, "ductile-tilde-test")
	if err := os.MkdirAll(tildeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tildeDir, "age.key"), []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".env.ductile"), []byte("DUCTILE_TILDE_TEST_FOO=bar\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := `
service:
  name: test
state:
  path: ./test.db
plugin_roots:
  - ~/ductile-tilde-test/plugins
  - ./plugins
  - ~otheruser/plugins
secrets:
  age_key_file: ~/ductile-tilde-test/age.key
  vault_file: ~/ductile-tilde-test/vault.age
api:
  enabled: false
  listen: "127.0.0.1:8081"
  management_socket: ~/ductile-tilde-test/admin.sock
tcc_paths:
  - ~/Documents
environment_vars:
  include:
    - ~/.env.ductile
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tilde := filepath.Join(home, "ductile-tilde-test")
	if got := loaded.PluginRoots[0]; got != filepath.Join(tilde, "plugins") {
		t.Errorf("plugin_roots[0]: got %q", got)
	}
	if got := loaded.PluginRoots[2]; got != "~otheruser/plugins" {
		t.Errorf("~user form must stay untouched, got %q", got)
	}
	if got := loaded.Secrets.AgeKeyFile; got != filepath.Join(tilde, "age.key") {
		t.Errorf("age_key_file: got %q", got)
	}
	if got := loaded.Secrets.VaultFile; got != filepath.Join(tilde, "vault.age") {
		t.Errorf("vault_file: got %q", got)
	}
	if got := loaded.API.ManagementSocket; got != filepath.Join(tilde, "admin.sock") {
		t.Errorf("management_socket: got %q", got)
	}
	if got := loaded.TCCPaths[0]; got != filepath.Join(home, "Documents") {
		t.Errorf("tcc_paths[0]: got %q", got)
	}
	if got := loaded.EnvironmentVars.Include[0]; got != filepath.Join(home, ".env.ductile") {
		t.Errorf("environment_vars.include[0]: got %q", got)
	}
}

func TestExpandTildeForms(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := map[string]string{
		"~":          home,
		"~/x":        filepath.Join(home, "x"),
		"~user/x":    "~user/x",
		"/abs/~/x":   "/abs/~/x",
		"relative/~": "relative/~",
		"":           "",
	}
	for in, want := range cases {
		if got := expandTilde(in); got != want {
			t.Errorf("expandTilde(%q): want %q, got %q", in, want, got)
		}
	}
}
