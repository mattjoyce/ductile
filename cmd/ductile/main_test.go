package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/relay"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/scheduler"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func captureOutputWithExitCode(t *testing.T, run func() int) (int, string, string) {
	t.Helper()

	oldStdout := os.Stdout
	oldStderr := os.Stderr

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdout failed: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stderr failed: %v", err)
	}

	os.Stdout = stdoutW
	os.Stderr = stderrW

	code := run()

	_ = stdoutW.Close()
	_ = stderrW.Close()
	os.Stdout = oldStdout
	os.Stderr = oldStderr

	stdoutBytes, _ := io.ReadAll(stdoutR)
	stderrBytes, _ := io.ReadAll(stderrR)

	_ = stdoutR.Close()
	_ = stderrR.Close()

	return code, string(stdoutBytes), string(stderrBytes)
}

func captureRunConfigHashUpdate(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	return captureOutputWithExitCode(t, func() int {
		return runConfigHashUpdate(args)
	})
}

func captureRunPluginLock(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	return captureOutputWithExitCode(t, func() int {
		return runPluginLock(args)
	})
}

// lockConfigAndPlugins performs the §3.1 two-step that replaced the old all-in-one
// lock: `config lock` authorizes the config files (and preserves/prunes recorded
// attestations), then `plugin lock <name>` explicitly attests each named plugin's
// bytes. Most fingerprint wiring tests need a plugin attested before they can
// exercise verify, tamper, or stale-record behavior.
func lockConfigAndPlugins(t *testing.T, dir string, names ...string) {
	t.Helper()
	if code, _, stderr := captureRunConfigHashUpdate(t, []string{"--config-dir", dir}); code != 0 {
		t.Fatalf("config lock failed: %s", stderr)
	}
	for _, n := range names {
		if code, _, stderr := captureRunPluginLock(t, []string{"--config-dir", dir, n}); code != 0 {
			t.Fatalf("plugin lock %s failed: %s", n, stderr)
		}
	}
}

func setVersionMetadataForTest(t *testing.T, v, commit, built string) {
	t.Helper()

	origVersion := version
	origCommit := gitCommit
	origBuildDate := buildDate

	version = v
	gitCommit = commit
	buildDate = built

	t.Cleanup(func() {
		version = origVersion
		gitCommit = origCommit
		buildDate = origBuildDate
	})
}

func TestRuntimeStateStopIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	sched := scheduler.New(
		&config.Config{Service: config.ServiceConfig{TickInterval: time.Hour}},
		nil,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	rt := &runtimeState{
		ctx:       ctx,
		cancel:    cancel,
		scheduler: sched,
		stopDone:  make(chan struct{}),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rt.Stop()
	}()
	go func() {
		defer wg.Done()
		rt.Stop()
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime stop did not complete")
	}

	if err := ctx.Err(); err == nil {
		t.Fatal("runtime context was not cancelled")
	}
}

func TestRunConfigHashUpdateVerboseDryRunShortFlag(t *testing.T) {
	tmpDir := t.TempDir()

	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
include:
  - tokens.yaml
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens:\n  - name: test\n    key: test\n    scopes_file: scopes/test.json\n    scopes_hash: blake3:deadbeef\n"), 0600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := captureRunConfigHashUpdate(t, []string{"--config", configPath, "-v", "--dry-run"})
	if code != 0 {
		t.Fatalf("runConfigHashUpdate() code = %d, stderr: %s", code, stderr)
	}

	if !strings.Contains(stdout, "Processing directory (v2 manifest)") {
		t.Fatalf("stdout missing verbose directory progress: %s", stdout)
	}
	if !strings.Contains(stdout, "DISCOVER [high-security]") {
		t.Fatalf("stdout missing discovery lines: %s", stdout)
	}
	if !strings.Contains(stdout, "DRY-RUN .checksums:") {
		t.Fatalf("stdout missing dry-run line: %s", stdout)
	}
	if !strings.Contains(stdout, "Dry run completed") {
		t.Fatalf("stdout missing dry-run summary: %s", stdout)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".checksums")); !os.IsNotExist(err) {
		t.Fatal(".checksums should not be written in dry-run mode")
	}
}

func TestRunConfigHashUpdateVerboseLongFlagWritesChecksums(t *testing.T) {
	tmpDir := t.TempDir()

	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
include:
  - tokens.yaml
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "tokens.yaml"), []byte("tokens:\n  - name: test\n    key: test\n    scopes_file: scopes/test.json\n    scopes_hash: blake3:deadbeef\n"), 0600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := captureRunConfigHashUpdate(t, []string{"--config", configPath, "--verbose"})
	if code != 0 {
		t.Fatalf("runConfigHashUpdate() code = %d, stderr: %s", code, stderr)
	}

	if !strings.Contains(stdout, "WROTE .checksums:") {
		t.Fatalf("stdout missing wrote checksums line: %s", stdout)
	}
	if !strings.Contains(stdout, "Successfully locked configuration") {
		t.Fatalf("stdout missing success summary: %s", stdout)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".checksums")); err != nil {
		t.Fatalf("expected .checksums to be written: %v", err)
	}
}

func TestLoadWebhooksFileAcceptsNestedDocumentedShape(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "webhooks.yaml")
	raw := `
webhooks:
  endpoints:
    - name: github
      path: /webhook/github
      plugin: echo
      secret_ref: github_webhook_secret
      signature_header: X-Hub-Signature-256
      max_body_size: 1MB
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadWebhooksFile(path)
	if err != nil {
		t.Fatalf("loadWebhooksFile() failed: %v", err)
	}
	if len(cfg.Webhooks) != 1 {
		t.Fatalf("len(cfg.Webhooks) = %d, want 1", len(cfg.Webhooks))
	}
	if cfg.Webhooks[0].Path != "/webhook/github" {
		t.Fatalf("cfg.Webhooks[0].Path = %q, want /webhook/github", cfg.Webhooks[0].Path)
	}
}

func TestWriteWebhooksFileUsesNestedDocumentedShape(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "webhooks.yaml")
	cfg := &config.WebhooksFileConfig{
		Webhooks: config.WebhookEndpoints{
			{
				Name:            "github",
				Path:            "/webhook/github",
				Plugin:          "echo",
				SecretRef:       "github_webhook_secret",
				SignatureHeader: "X-Hub-Signature-256",
				MaxBodySize:     "1MB",
			},
		},
	}

	if err := writeWebhooksFile(path, cfg); err != nil {
		t.Fatalf("writeWebhooksFile() failed: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() failed: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "webhooks:") {
		t.Fatalf("written file missing webhooks root: %s", text)
	}
	if !strings.Contains(text, "endpoints:") {
		t.Fatalf("written file missing endpoints nesting: %s", text)
	}
}

func TestRunConfigNounActionHelp(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigNoun([]string{"check", "--help"})
	})
	if code != 0 {
		t.Fatalf("runConfigNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile config check") {
		t.Fatalf("stdout missing action help usage: %s", stdout)
	}
}

func TestRunConfigNounHelpTerminology(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigNoun([]string{"--help"})
	})
	if code != 0 {
		t.Fatalf("runConfigNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile config <action>") {
		t.Fatalf("stdout missing action terminology: %s", stdout)
	}
	if strings.Contains(stdout, "<verb>") {
		t.Fatalf("stdout should not reference <verb>: %s", stdout)
	}
}

func TestRunJobNounActionHelp(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runJobNoun([]string{"inspect", "--help"})
	})
	if code != 0 {
		t.Fatalf("runJobNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile job inspect") {
		t.Fatalf("stdout missing inspect action help usage: %s", stdout)
	}
}

func TestRunRelayNounActionHelp(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runRelayNoun([]string{"send", "--help"})
	})
	if code != 0 {
		t.Fatalf("runRelayNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile relay send") {
		t.Fatalf("stdout missing relay send action help usage: %s", stdout)
	}
}

func TestRunSystemNounActionHelp(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemNoun([]string{"start", "--help"})
	})
	if code != 0 {
		t.Fatalf("runSystemNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile system start") {
		t.Fatalf("stdout missing start action help usage: %s", stdout)
	}
}

func TestRunSystemNounResetActionHelp(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemNoun([]string{"reset", "--help"})
	})
	if code != 0 {
		t.Fatalf("runSystemNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile system reset") {
		t.Fatalf("stdout missing reset action help usage: %s", stdout)
	}
}

func TestRunSystemNounSelfcheckActionHelp(t *testing.T) {
	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemNoun([]string{"selfcheck", "--help"})
	})
	if code != 0 {
		t.Fatalf("runSystemNoun() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Usage: ductile system selfcheck") {
		t.Fatalf("stdout missing selfcheck action help usage: %s", stdout)
	}
	if !strings.Contains(stdout, "PRAGMA integrity_check") {
		t.Fatalf("selfcheck help should mention integrity_check: %s", stdout)
	}
}

func TestRunSystemNounHelpListsSelfcheck(t *testing.T) {
	code, _, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemNoun([]string{"help"})
	})
	if code != 0 {
		t.Fatalf("runSystemNoun(help) code = %d, stderr: %s", code, stderr)
	}
	// help prints to stdout; capture both via the helper above.
	_, stdout, _ := captureOutputWithExitCode(t, func() int {
		printSystemNounHelp(os.Stdout)
		return 0
	})
	if !strings.Contains(stdout, "selfcheck") {
		t.Fatalf("system noun help should list selfcheck: %s", stdout)
	}
}

func TestRunSystemSelfcheckOnFreshDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
state:
  path: ` + dbPath + `
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: false
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	_ = db.Close()

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemSelfcheck([]string{"--config", configPath, "--json"})
	})
	if code != 0 {
		t.Fatalf("selfcheck on fresh DB code = %d, stderr=%s, stdout=%s", code, stderr, stdout)
	}

	var report systemStatusReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("unmarshal report: %v\nstdout: %s", err, stdout)
	}
	if !report.Healthy {
		t.Fatalf("expected healthy=true on fresh DB, got report: %+v", report)
	}
	want := []string{"config_discovery", "config_load", "pid_lock", "db_integrity", "db_schema", "queue_terminal_freshness"}
	have := make(map[string]bool, len(report.Checks))
	for _, c := range report.Checks {
		have[c.Name] = c.OK
	}
	for _, name := range want {
		ok, present := have[name]
		if !present {
			t.Fatalf("expected check %q in report, got %+v", name, report.Checks)
		}
		if !ok {
			t.Fatalf("expected check %q OK on fresh DB, got fail: %+v", name, report.Checks)
		}
	}
}

func TestCheckQueueTerminalFreshnessThresholds(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()

	// Empty queue → OK + clean.
	got := checkQueueTerminalFreshness(ctx, db, dbPath)
	if !got.OK {
		t.Fatalf("empty queue should be OK, got %+v", got)
	}
	if !strings.Contains(got.Detail, "no stale") {
		t.Fatalf("empty queue detail mismatch: %q", got.Detail)
	}

	// Insert 100 stale terminal rows → still OK (within tolerance).
	insertStale := func(n int, status string) {
		t.Helper()
		tx, err := db.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		stmt, err := tx.Prepare(`INSERT INTO job_queue (id, plugin, command, status, attempt, max_attempts, submitted_by, created_at, completed_at) VALUES (?, 'echo', 'run', ?, 1, 4, 'test', datetime('now','-3 days'), datetime('now','-2 days','-1 hour'))`)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer func() { _ = stmt.Close() }()
		for i := range n {
			id := status + "-" + strconv.Itoa(i)
			if _, err := stmt.Exec(id, status); err != nil {
				t.Fatalf("exec: %v", err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	insertStale(100, "succeeded")
	got = checkQueueTerminalFreshness(ctx, db, dbPath)
	if !got.OK {
		t.Fatalf("100 stale rows should be within tolerance, got %+v", got)
	}
	if !strings.Contains(got.Detail, "within Wave-1 tolerance") {
		t.Fatalf("100 stale detail should note tolerance: %q", got.Detail)
	}

	// Push past threshold (already 100; insert 5001 more) → FAIL.
	insertStale(5001, "failed")
	got = checkQueueTerminalFreshness(ctx, db, dbPath)
	if got.OK {
		t.Fatalf("over-threshold stale rows should fail, got OK: %+v", got)
	}
	if !strings.Contains(got.Detail, "exceeds threshold") {
		t.Fatalf("over-threshold detail mismatch: %q", got.Detail)
	}
}

func TestPrintUsageUsesActionTerminology(t *testing.T) {
	_, stdout, _ := captureOutputWithExitCode(t, func() int {
		printUsage()
		return 0
	})
	if !strings.Contains(stdout, "ductile <noun> <action> [flags]") {
		t.Fatalf("usage missing action terminology: %s", stdout)
	}
	if strings.Contains(stdout, "<noun> <verb>") {
		t.Fatalf("usage should not reference verb terminology: %s", stdout)
	}
}

func TestRunCLIRootVersionFlag(t *testing.T) {
	setVersionMetadataForTest(t, "1.2.3", "abc1234567890", "2026-02-12T11:30:00Z")

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runCLI([]string{"--version"})
	})
	if code != 0 {
		t.Fatalf("runCLI() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ductile 1.2.3") {
		t.Fatalf("stdout missing semantic version: %s", stdout)
	}
	if !strings.Contains(stdout, "commit: abc123456789") {
		t.Fatalf("stdout missing short commit: %s", stdout)
	}
	if !strings.Contains(stdout, "built_at: 2026-02-12T11:30:00Z") {
		t.Fatalf("stdout missing build time: %s", stdout)
	}
}

func TestRunVersionJSONOutputIncludesMetadata(t *testing.T) {
	setVersionMetadataForTest(t, "2.0.0-rc.1", "aabbccddeeff001122334455", "2026-02-12T11:30:00-05:00")

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runVersion([]string{"--json"})
	})
	if code != 0 {
		t.Fatalf("runVersion() code = %d, stderr: %s", code, stderr)
	}

	var out struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildTime string `json:"build_time"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("failed to parse version JSON: %v\noutput=%s", err, stdout)
	}

	if out.Version != "2.0.0-rc.1" {
		t.Fatalf("version = %q, want %q", out.Version, "2.0.0-rc.1")
	}
	if out.Commit != "aabbccddeeff" {
		t.Fatalf("commit = %q, want %q", out.Commit, "aabbccddeeff")
	}
	if out.BuildTime != "2026-02-12T16:30:00Z" {
		t.Fatalf("build_time = %q, want %q", out.BuildTime, "2026-02-12T16:30:00Z")
	}
}

func TestRunConfigSetApplyRejectsInvalidConfigAndRollsBack(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
service:
  name: test-gw
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: true
    schedules:
      - every: 5m
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigSet([]string{"--config", configPath, "--apply", "service.log_level=invalid"})
	})
	if code == 0 {
		t.Fatalf("runConfigSet() should fail for invalid apply, stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "Apply failed: validation failed:") {
		t.Fatalf("stderr missing validation failure details: %s", stderr)
	}

	reloaded, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("config should still be valid after failed apply: %v", err)
	}
	if got := reloaded.Service.LogLevel; got != "info" {
		t.Fatalf("service.log_level should remain %q after failed apply, got %q", "info", got)
	}
}

func TestRunSystemResetResetsCircuitBreaker(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
state:
  path: ` + dbPath + `
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: false
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := queue.New(db)
	openedAt := time.Now().UTC().Add(-5 * time.Minute)
	if err := q.UpsertCircuitBreaker(context.Background(), queue.CircuitBreaker{
		Plugin:       "echo",
		Command:      "poll",
		State:        queue.CircuitOpen,
		FailureCount: 3,
		OpenedAt:     &openedAt,
	}); err != nil {
		t.Fatalf("UpsertCircuitBreaker: %v", err)
	}

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemReset([]string{"--config", configPath, "echo"})
	})
	if code != 0 {
		t.Fatalf("runSystemReset() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Reset circuit breaker for echo (poll)") {
		t.Fatalf("stdout missing reset confirmation: %s", stdout)
	}

	got, err := q.GetCircuitBreaker(context.Background(), "echo", "poll")
	if err != nil {
		t.Fatalf("GetCircuitBreaker: %v", err)
	}
	if got == nil {
		t.Fatal("expected circuit breaker row after reset")
	} else {
		if got.State != queue.CircuitClosed {
			t.Fatalf("state=%q want %q", got.State, queue.CircuitClosed)
		}
		if got.FailureCount != 0 {
			t.Fatalf("failure_count=%d want 0", got.FailureCount)
		}
	}

	transitions, err := q.ListCircuitBreakerTransitions(context.Background(), "echo", "poll", 10)
	if err != nil {
		t.Fatalf("ListCircuitBreakerTransitions: %v", err)
	}
	if len(transitions) != 1 {
		t.Fatalf("transitions len=%d want 1", len(transitions))
	}
	if transitions[0].Reason != queue.CircuitTransitionManualReset {
		t.Fatalf("transition reason=%q want %q", transitions[0].Reason, queue.CircuitTransitionManualReset)
	}
}

func TestRunSystemBreakerShowsCircuitBreakerHistory(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")
	configYAML := `
state:
  path: ` + dbPath + `
plugin_roots:
  - ./plugins
plugins:
  echo:
    enabled: false
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := queue.New(db)
	openedAt := time.Now().UTC().Add(-5 * time.Minute)
	lastJobID := "job-open"
	if err := q.UpsertCircuitBreaker(context.Background(), queue.CircuitBreaker{
		Plugin:       "echo",
		Command:      "poll",
		State:        queue.CircuitOpen,
		FailureCount: 3,
		OpenedAt:     &openedAt,
		LastJobID:    &lastJobID,
	}); err != nil {
		t.Fatalf("UpsertCircuitBreaker: %v", err)
	}
	from := queue.CircuitClosed
	if err := q.RecordCircuitBreakerTransition(context.Background(), queue.CircuitBreakerTransition{
		Plugin:       "echo",
		Command:      "poll",
		FromState:    &from,
		ToState:      queue.CircuitOpen,
		FailureCount: 3,
		Reason:       queue.CircuitTransitionFailureThreshold,
		JobID:        &lastJobID,
	}); err != nil {
		t.Fatalf("RecordCircuitBreakerTransition: %v", err)
	}

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemBreaker([]string{"--config", configPath, "--json", "echo"})
	})
	if code != 0 {
		t.Fatalf("runSystemBreaker() code = %d, stderr: %s", code, stderr)
	}

	var out struct {
		Plugin      string `json:"plugin"`
		Command     string `json:"command"`
		State       string `json:"state"`
		Transitions []struct {
			Reason string `json:"reason"`
			JobID  string `json:"job_id"`
		} `json:"transitions"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("failed to parse breaker json: %v\noutput=%s", err, stdout)
	}
	if out.Plugin != "echo" || out.Command != "poll" || out.State != string(queue.CircuitOpen) {
		t.Fatalf("unexpected breaker report: %#v", out)
	}
	if len(out.Transitions) != 1 {
		t.Fatalf("transitions len=%d want 1", len(out.Transitions))
	}
	if out.Transitions[0].Reason != string(queue.CircuitTransitionFailureThreshold) {
		t.Fatalf("reason=%q want %q", out.Transitions[0].Reason, queue.CircuitTransitionFailureThreshold)
	}
	if out.Transitions[0].JobID != lastJobID {
		t.Fatalf("job_id=%q want %q", out.Transitions[0].JobID, lastJobID)
	}
}

func TestRunSystemPluginFactsJSON(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")

	if err := os.MkdirAll(filepath.Join(tmpDir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	configYAML := `
service:
  tick_interval: 30s
  log_level: info
state:
  path: ` + dbPath + `
plugin_roots:
  - ` + filepath.Join(tmpDir, "plugins") + `
plugins:
  file_watch:
    enabled: true
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	st := state.NewStore(db)
	if _, _, err := st.RecordFact(context.Background(), state.PluginFact{
		ID:         "fact-1",
		PluginName: "file_watch",
		FactType:   state.FactTypeFileWatchSnapshot,
		JobID:      "job-1",
		Command:    "poll",
		FactJSON:   json.RawMessage(`{"last_poll_at":"2026-04-22T01:02:03Z","watches":{"single-file":{"exists":true}}}`),
		CreatedAt:  time.Date(2026, 4, 22, 1, 2, 3, 0, time.UTC),
	}, "mirror_object"); err != nil {
		t.Fatalf("RecordFact: %v", err)
	}

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemPluginFacts([]string{"--config", configPath, "--json", "--fact-type", state.FactTypeFileWatchSnapshot, "file_watch"})
	})
	if code != 0 {
		t.Fatalf("runSystemPluginFacts() code = %d, stderr: %s", code, stderr)
	}

	var out struct {
		Plugin   string `json:"plugin"`
		FactType string `json:"fact_type"`
		Facts    []struct {
			ID       string         `json:"id"`
			FactType string         `json:"fact_type"`
			JobID    string         `json:"job_id"`
			Command  string         `json:"command"`
			Fact     map[string]any `json:"fact"`
		} `json:"facts"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("failed to parse plugin facts json: %v\noutput=%s", err, stdout)
	}
	if out.Plugin != "file_watch" || out.FactType != state.FactTypeFileWatchSnapshot {
		t.Fatalf("unexpected report header: %#v", out)
	}
	if len(out.Facts) != 1 {
		t.Fatalf("len(out.Facts) = %d, want 1", len(out.Facts))
	}
	if out.Facts[0].ID != "fact-1" || out.Facts[0].JobID != "job-1" || out.Facts[0].Command != "poll" {
		t.Fatalf("unexpected fact row: %#v", out.Facts[0])
	}
	if _, ok := out.Facts[0].Fact["watches"]; !ok {
		t.Fatalf("expected watches payload in fact: %#v", out.Facts[0].Fact)
	}
}

func TestRunSystemSkillsFallsBackWithoutUsableAutoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DUCTILE_CONFIG_DIR", tmpDir)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemSkills(nil)
	})
	if code != 0 {
		t.Fatalf("runSystemSkills() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "AI Operator Guide (Core Mode)") {
		t.Fatalf("stdout missing core fallback header: %s", stdout)
	}
	if !strings.Contains(stdout, "token-frugal baseline") {
		t.Fatalf("stdout missing fallback guidance: %s", stdout)
	}
	if !strings.Contains(stdout, "`ductile config check --json`") {
		t.Fatalf("stdout missing quick-loop command: %s", stdout)
	}
}

func TestRunSystemSkillsExplicitConfigLoadFailureReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	missingConfigPath := filepath.Join(tmpDir, "missing-config.yaml")

	code, _, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemSkills([]string{"--config", missingConfigPath})
	})
	if code == 0 {
		t.Fatalf("runSystemSkills() should fail for explicit invalid config, stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "Failed to load config:") {
		t.Fatalf("stderr missing explicit load failure: %s", stderr)
	}
}

func TestRunSystemSkillsWithConfigEmitsLiveManifest(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemSkills([]string{"--config", tmpDir})
	})
	if code != 0 {
		t.Fatalf("runSystemSkills() code = %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "LLM Operator Skill Manifest") {
		t.Fatalf("stdout missing live manifest header: %s", stdout)
	}
	if strings.Contains(stdout, "AI Operator Guide (Core Mode)") {
		t.Fatalf("stdout should not fall back in configured mode: %s", stdout)
	}
}

func writeConfigDirFixtureWithPlugin(t *testing.T, dir string) {
	t.Helper()

	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// allow_symlinks: true accommodates macOS t.TempDir() symlink layout.
	configYAML := `
service:
  tick_interval: 60s
  log_level: info
  allow_symlinks: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
plugin_roots:
  - ` + pluginsDir + `
plugins: {}
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write an echo-style plugin with a write command (has semantic anchors).
	pluginDir := filepath.Join(pluginsDir, "echo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: echo
version: "1.0.0"
protocol: 2
entrypoint: run.sh
description: "Test echo plugin."
commands:
  - name: poll
    type: write
    description: "Emit events."
    idempotent: false
    retry_safe: false
  - name: health
    type: read
    description: "Health check."
    idempotent: true
    retry_safe: true
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunSystemSkillsPluginCommandHasSemanticAnchors(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixtureWithPlugin(t, tmpDir)

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemSkills([]string{"--config", tmpDir})
	})
	if code != 0 {
		t.Fatalf("runSystemSkills() code = %d, stderr: %s", code, stderr)
	}
	for _, want := range []string{
		"tier=WRITE",
		"mut=1",
		"idem=0",
		"retry=0",
		"m=POST",
		"p=/plugin/",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q: %s", want, stdout)
		}
	}
}

func TestRunSystemSkillsOutputIsDeterministic(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixtureWithPlugin(t, tmpDir)

	_, stdout1, _ := captureOutputWithExitCode(t, func() int {
		return runSystemSkills([]string{"--config", tmpDir})
	})
	_, stdout2, _ := captureOutputWithExitCode(t, func() int {
		return runSystemSkills([]string{"--config", tmpDir})
	})
	if stdout1 != stdout2 {
		t.Fatalf("runSystemSkills() output is not deterministic:\nrun1=%s\nrun2=%s", stdout1, stdout2)
	}
}

func TestRunSystemSkillsOperatorGuidancePresent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DUCTILE_CONFIG_DIR", tmpDir)

	// Core mode: guidance must be present.
	_, coreStdout, _ := captureOutputWithExitCode(t, func() int {
		return runSystemSkills(nil)
	})
	if !strings.Contains(coreStdout, "progressive disclosure") {
		t.Fatalf("core-mode stdout missing progressive disclosure guidance: %s", coreStdout)
	}

	// Full manifest: guidance must be present.
	writeConfigDirFixtureWithPlugin(t, tmpDir)
	_, fullStdout, _ := captureOutputWithExitCode(t, func() int {
		return runSystemSkills([]string{"--config", tmpDir})
	})
	if !strings.Contains(fullStdout, "Operator guidance: use `--json`") {
		t.Fatalf("full-manifest stdout missing operator guidance: %s", fullStdout)
	}
}

func writeConfigDirFixture(t *testing.T, dir string) {
	t.Helper()

	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// allow_symlinks: true accommodates macOS t.TempDir() which returns
	// /var/folders/... — a symlink to /private/var/folders/... — and would
	// otherwise be refused by plugin_root symlink detection.
	configYAML := `
service:
  tick_interval: 60s
  log_level: info
  allow_symlinks: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
plugin_roots:
  - ` + pluginsDir + `
plugins: {}
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRelaySenderConfigFixture(t *testing.T, dir, baseURL string) {
	t.Helper()

	pluginsDir := filepath.Join(dir, "plugins")
	scopesDir := filepath.Join(dir, "scopes")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(scopesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	configYAML := `
include:
  - relay-instances.yaml
service:
  name: home-primary
  tick_interval: 60s
  log_level: info
  allow_symlinks: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
plugin_roots:
  - ` + pluginsDir + `
secrets:
  age_key_file: age.key
  vault_file: vault.age
plugins: {}
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scopesDir, "relay.json"), []byte("[\"*\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tokensYAML := `
tokens:
  - name: relay-lab-v1
    key: shared-secret
    scopes_file: scopes/relay.json
    scopes_hash: blake3:dummy
`
	if err := os.WriteFile(filepath.Join(dir, "tokens.yaml"), []byte(tokensYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	relayInstancesYAML := `
instances:
  - name: lab
    enabled: true
    base_url: ` + baseURL + `
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
    key_id: v1
    timeout: 5s
    allow:
      - backup.ready
      - withings.measurement_recorded
`
	if err := os.WriteFile(filepath.Join(dir, "relay-instances.yaml"), []byte(relayInstancesYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	// The relay secret now resolves from the vault (epic #48), not tokens.yaml.
	seedVaultSecrets(t, dir, map[string]string{"relay-lab-v1": "shared-secret"})
}

func TestRunRelaySendDeliversConfiguredEvent(t *testing.T) {
	tmpDir := t.TempDir()

	db, err := storage.OpenSQLite(context.Background(), filepath.Join(tmpDir, "relay.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	q := queue.New(db)
	contexts := state.NewContextStore(db)

	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	pipelineYAML := `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
        baggage:
          trace_id: context.trace_id
`
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	engine, err := router.LoadFromConfigFiles([]string{pipelinePath}, nil, slog.Default())
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	receiverCfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{
					Name:      "home-primary",
					Enabled:   true,
					SecretRef: "relay-lab-v1",
					KeyID:     "v1",
					Accept:    []string{"backup.ready"},
					Baggage:   config.RelayBaggageRules{Allow: []string{"trace_id"}},
				},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	receiver, err := relay.NewReceiver(receiverCfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	mux := chi.NewRouter()
	mux.Post(receiver.RoutePattern(), receiver.HandleHTTP)
	server := httptest.NewServer(mux)
	defer server.Close()

	writeRelaySenderConfigFixture(t, tmpDir, server.URL)
	lockCode, _, lockStderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmpDir})
	if lockCode != 0 {
		t.Fatalf("runConfigHashUpdate() code = %d, stderr: %s", lockCode, lockStderr)
	}

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runRelaySend([]string{
			"--config-dir", tmpDir,
			"--event", "backup.ready",
			"--payload", `{"path":"/srv/backups/latest.tar.zst"}`,
			"--baggage", `{"trace_id":"tr-789"}`,
			"--dedupe-key", "backup.ready:2026-05-03",
			"--origin-plugin", "backup-runner",
			"--origin-job-id", "job-123",
			"--origin-event-id", "evt-456",
			"--json",
			"lab",
		})
	})
	if code != 0 {
		t.Fatalf("runRelaySend() code = %d, stderr: %s", code, stderr)
	}

	var accepted relay.AcceptanceResponse
	if err := json.Unmarshal([]byte(stdout), &accepted); err != nil {
		t.Fatalf("failed to parse relay send json: %v\noutput=%s", err, stdout)
	}
	if accepted.Status != "accepted" {
		t.Fatalf("accepted.Status = %q, want accepted", accepted.Status)
	}
	if accepted.JobID == "" {
		t.Fatal("expected accepted job id")
	}

	job, err := q.GetJobByID(context.Background(), accepted.JobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if job.Plugin != "echo" || job.Command != "handle" {
		t.Fatalf("job = %+v, want echo handle", job)
	}

	var payload []byte
	var eventContextID string
	if err := db.QueryRow(`SELECT payload, event_context_id FROM job_queue WHERE id = ?`, accepted.JobID).Scan(&payload, &eventContextID); err != nil {
		t.Fatalf("query job payload/context: %v", err)
	}

	var event protocol.Event
	if err := json.Unmarshal(payload, &event); err != nil {
		t.Fatalf("Unmarshal payload event: %v", err)
	}
	if event.Type != "backup.ready" {
		t.Fatalf("event.Type = %q, want backup.ready", event.Type)
	}
	if event.Source != "relay:home-primary" {
		t.Fatalf("event.Source = %q, want relay:home-primary", event.Source)
	}

	ctxRow, err := contexts.Get(context.Background(), eventContextID)
	if err != nil {
		t.Fatalf("contexts.Get: %v", err)
	}
	var scope map[string]any
	if err := json.Unmarshal(ctxRow.AccumulatedJSON, &scope); err != nil {
		t.Fatalf("Unmarshal accumulated_json: %v", err)
	}
	if scope["trace_id"] != "tr-789" {
		t.Fatalf("trace_id = %v, want tr-789", scope["trace_id"])
	}
}

func TestRunRelaySendAcceptsInstanceFirstOrdering(t *testing.T) {
	var observedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		observedBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted","peer":"home-primary","event_type":"withings.measurement_recorded","receiver_event_id":"evt-9"}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	writeRelaySenderConfigFixture(t, tmpDir, server.URL)
	lockCode, _, lockStderr := captureRunConfigHashUpdate(t, []string{"--config-dir", tmpDir})
	if lockCode != 0 {
		t.Fatalf("runConfigHashUpdate() code = %d, stderr: %s", lockCode, lockStderr)
	}

	code, _, stderr := captureOutputWithExitCode(t, func() int {
		return runRelaySend([]string{
			"lab",
			"--config-dir", tmpDir,
			"--event", "withings.measurement_recorded",
			"--payload", `{"withings_id":"abc"}`,
			"--json",
		})
	})
	if code != 0 {
		t.Fatalf("runRelaySend() code = %d, stderr: %s", code, stderr)
	}
	if len(observedBody) == 0 {
		t.Fatal("relay receiver did not receive a request")
	}
	if !bytes.Contains(observedBody, []byte(`"withings.measurement_recorded"`)) {
		t.Fatalf("relay body did not carry underscored event type: %s", string(observedBody))
	}
}

func TestRunConfigTokenCreateAndInspectJSON(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	createCode, createStdout, createStderr := captureOutputWithExitCode(t, func() int {
		return runConfigTokenCreate([]string{
			"--config-dir", tmpDir,
			"--name", "github-integration",
			"--scopes", "read:jobs,read:events",
			"--format", "json",
		})
	})
	if createCode != 0 {
		t.Fatalf("runConfigTokenCreate() code = %d, stderr: %s", createCode, createStderr)
	}

	var createOut struct {
		Status string `json:"status"`
		Token  struct {
			Name       string `json:"name"`
			ScopesFile string `json:"scopes_file"`
			ScopesHash string `json:"scopes_hash"`
		} `json:"token"`
		EnvVar string `json:"env_var"`
	}
	if err := json.Unmarshal([]byte(createStdout), &createOut); err != nil {
		t.Fatalf("failed to parse create json: %v\noutput=%s", err, createStdout)
	}
	if createOut.Status != "success" {
		t.Fatalf("status = %q, want success", createOut.Status)
	}
	if createOut.Token.Name != "github-integration" {
		t.Fatalf("token.name = %q", createOut.Token.Name)
	}
	if !strings.HasPrefix(createOut.Token.ScopesHash, "blake3:") {
		t.Fatalf("token.scopes_hash missing blake3 prefix: %q", createOut.Token.ScopesHash)
	}
	if createOut.EnvVar != "GITHUB_INTEGRATION_TOKEN" {
		t.Fatalf("env_var = %q, want %q", createOut.EnvVar, "GITHUB_INTEGRATION_TOKEN")
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "tokens.yaml")); err != nil {
		t.Fatalf("tokens.yaml not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "scopes", "github-integration.json")); err != nil {
		t.Fatalf("scope file not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, ".checksums")); err != nil {
		t.Fatalf(".checksums not written: %v", err)
	}

	inspectCode, inspectStdout, inspectStderr := captureOutputWithExitCode(t, func() int {
		return runConfigTokenInspect([]string{
			"github-integration",
			"--config-dir", tmpDir,
			"--format", "json",
		})
	})
	if inspectCode != 0 {
		t.Fatalf("runConfigTokenInspect() code = %d, stderr: %s", inspectCode, inspectStderr)
	}

	var inspectOut struct {
		Name        string `json:"name"`
		HashMatches bool   `json:"hash_matches"`
	}
	if err := json.Unmarshal([]byte(inspectStdout), &inspectOut); err != nil {
		t.Fatalf("failed to parse inspect json: %v\noutput=%s", err, inspectStdout)
	}
	if inspectOut.Name != "github-integration" {
		t.Fatalf("inspect name = %q", inspectOut.Name)
	}
	if !inspectOut.HashMatches {
		t.Fatalf("inspect hash_matches = false, output=%s", inspectStdout)
	}
}

func TestRunConfigScopeAddWithPositionalBeforeFlags(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	createCode, _, createStderr := captureOutputWithExitCode(t, func() int {
		return runConfigTokenCreate([]string{
			"--config-dir", tmpDir,
			"--name", "github-integration",
			"--scopes", "read:jobs",
		})
	})
	if createCode != 0 {
		t.Fatalf("runConfigTokenCreate() code = %d, stderr: %s", createCode, createStderr)
	}

	scopeCode, _, scopeStderr := captureOutputWithExitCode(t, func() int {
		return runConfigScopeAdd([]string{
			"github-integration",
			"echo:ro",
			"--config-dir", tmpDir,
		})
	})
	if scopeCode != 0 {
		t.Fatalf("runConfigScopeAdd() code = %d, stderr: %s", scopeCode, scopeStderr)
	}

	raw, err := os.ReadFile(filepath.Join(tmpDir, "scopes", "github-integration.json"))
	if err != nil {
		t.Fatalf("read scope file: %v", err)
	}
	var doc struct {
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse scope file: %v", err)
	}

	if !containsString(doc.Scopes, "read:jobs") || !containsString(doc.Scopes, "echo:ro") {
		t.Fatalf("scope file missing expected scopes: %v", doc.Scopes)
	}
}

func containsString(list []string, value string) bool {
	return slices.Contains(list, value)
}

func writeRouteFixture(t *testing.T, dir string) {
	t.Helper()

	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// allow_symlinks: true accommodates macOS t.TempDir() symlink layout.
	configYAML := `
service:
  tick_interval: 60s
  log_level: info
  allow_symlinks: true
state:
  path: ` + filepath.Join(dir, "state.db") + `
plugin_roots:
  - ` + pluginsDir + `
plugins:
  withings:
    enabled: false
    schedules:
      - every: daily
  slack:
    enabled: false
    schedules:
      - every: daily
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}
}

func createTestPlugin(t *testing.T, pluginsDir, name string) {
	t.Helper()

	pluginDir := filepath.Join(pluginsDir, name)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
manifest_spec: ductile.plugin
manifest_version: 1
name: ` + name + `
version: "1.0.0"
protocol: 2
entrypoint: run.sh
commands:
  - name: handle
    type: write
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestRunConfigPluginSetCreatesPluginFile(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	code, _, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigPluginSet([]string{
			"echo",
			"schedules.0.every",
			"2h",
			"--config-dir", tmpDir,
		})
	})
	if code != 0 {
		t.Fatalf("runConfigPluginSet() code = %d, stderr: %s", code, stderr)
	}

	pluginFile := filepath.Join(tmpDir, "plugins", "echo.yaml")
	raw, err := os.ReadFile(pluginFile)
	if err != nil {
		t.Fatalf("read plugin file: %v", err)
	}
	if !strings.Contains(string(raw), "every: 2h") {
		t.Fatalf("plugin file missing updated schedule: %s", string(raw))
	}
}

func TestRunConfigRouteAddAndRemove(t *testing.T) {
	tmpDir := t.TempDir()
	writeRouteFixture(t, tmpDir)

	addCode, _, addStderr := captureOutputWithExitCode(t, func() int {
		return runConfigRouteAdd([]string{
			"--config-dir", tmpDir,
			"--from", "withings",
			"--event", "weight_updated",
			"--to", "slack",
		})
	})
	if addCode != 0 {
		t.Fatalf("runConfigRouteAdd() code = %d, stderr: %s", addCode, addStderr)
	}

	raw, err := os.ReadFile(filepath.Join(tmpDir, "routes.yaml"))
	if err != nil {
		t.Fatalf("read routes file: %v", err)
	}
	if !strings.Contains(string(raw), "withings") || !strings.Contains(string(raw), "weight_updated") {
		t.Fatalf("route not written: %s", string(raw))
	}

	removeCode, _, removeStderr := captureOutputWithExitCode(t, func() int {
		return runConfigRouteRemove([]string{
			"--config-dir", tmpDir,
			"--from", "withings",
			"--event", "weight_updated",
			"--to", "slack",
		})
	})
	if removeCode != 0 {
		t.Fatalf("runConfigRouteRemove() code = %d, stderr: %s", removeCode, removeStderr)
	}
}

func TestRunConfigWebhookAddAndList(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)
	pluginsDir := filepath.Join(tmpDir, "plugins")
	createTestPlugin(t, pluginsDir, "github-handler")

	addCode, _, addStderr := captureOutputWithExitCode(t, func() int {
		return runConfigWebhookAdd([]string{
			"--config-dir", tmpDir,
			"--name", "github",
			"--path", "/webhook/github",
			"--plugin", "github-handler",
			"--secret-ref", "github_secret",
		})
	})
	if addCode != 0 && addCode != 2 {
		t.Fatalf("runConfigWebhookAdd() code = %d, stderr: %s", addCode, addStderr)
	}

	listCode, stdout, listStderr := captureOutputWithExitCode(t, func() int {
		return runConfigWebhookList([]string{"--config-dir", tmpDir, "--format", "json"})
	})
	if listCode != 0 {
		t.Fatalf("runConfigWebhookList() code = %d, stderr: %s", listCode, listStderr)
	}

	var hooks []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Plugin string `json:"plugin"`
	}
	if err := json.Unmarshal([]byte(stdout), &hooks); err != nil {
		t.Fatalf("failed to parse webhook list json: %v\noutput=%s", err, stdout)
	}
	if len(hooks) != 1 || hooks[0].Path != "/webhook/github" || hooks[0].Plugin != "github-handler" {
		t.Fatalf("unexpected webhook list: %+v", hooks)
	}
}

func TestRunConfigInitBackupRestore(t *testing.T) {
	tmpDir := t.TempDir()
	// Resolve macOS /var → /private/var symlink so the validation walker
	// doesn't refuse the auto-init config (whose template, correctly, leaves
	// service.allow_symlinks at the prod-safe default of false).
	if resolved, err := filepath.EvalSymlinks(tmpDir); err == nil {
		tmpDir = resolved
	}
	configDir := filepath.Join(tmpDir, "cfg")

	initCode, _, initStderr := captureOutputWithExitCode(t, func() int {
		return runConfigInit([]string{"--config-dir", configDir})
	})
	if initCode != 0 {
		t.Fatalf("runConfigInit() code = %d, stderr: %s", initCode, initStderr)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.yaml")); err != nil {
		t.Fatalf("config.yaml missing after init: %v", err)
	}

	backupPath := filepath.Join(tmpDir, "backup.tar.gz")
	backupCode, _, backupStderr := captureOutputWithExitCode(t, func() int {
		return runConfigBackup([]string{"--config-dir", configDir, "--output", backupPath})
	})
	if backupCode != 0 {
		t.Fatalf("runConfigBackup() code = %d, stderr: %s", backupCode, backupStderr)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup archive missing: %v", err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "tokens.yaml"), []byte("tokens:\n  - name: changed\n    key: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	restoreCode, _, restoreStderr := captureOutputWithExitCode(t, func() int {
		return runConfigRestore([]string{backupPath, "--config-dir", configDir})
	})
	if restoreCode != 0 {
		t.Fatalf("runConfigRestore() code = %d, stderr: %s", restoreCode, restoreStderr)
	}

	raw, err := os.ReadFile(filepath.Join(configDir, "tokens.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "changed") {
		t.Fatalf("restore did not replace modified tokens.yaml: %s", string(raw))
	}
}

func TestRunConfigCheckSupportsConfigDirFlag(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	code, _, stderr := captureOutputWithExitCode(t, func() int {
		return runConfigCheck([]string{"--config-dir", tmpDir})
	})
	if code != 0 {
		t.Fatalf("runConfigCheck() code = %d, stderr: %s", code, stderr)
	}
}

func TestRunConfigGetAndSetSupportConfigDirFlag(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	setCode, _, setStderr := captureOutputWithExitCode(t, func() int {
		return runConfigSet([]string{"--config-dir", tmpDir, "--apply", "service.log_level=debug"})
	})
	if setCode != 0 {
		t.Fatalf("runConfigSet() code = %d, stderr: %s", setCode, setStderr)
	}

	getCode, stdout, getStderr := captureOutputWithExitCode(t, func() int {
		return runConfigGet([]string{"--config-dir", tmpDir, "service.log_level"})
	})
	if getCode != 0 {
		t.Fatalf("runConfigGet() code = %d, stderr: %s", getCode, getStderr)
	}
	if !strings.Contains(stdout, "debug") {
		t.Fatalf("runConfigGet() output missing updated value: %s", stdout)
	}
}

func TestRunConfigShow(t *testing.T) {
	tmpDir := t.TempDir()
	writeConfigDirFixture(t, tmpDir)

	t.Run("show full config yaml", func(t *testing.T) {
		code, stdout, stderr := captureOutputWithExitCode(t, func() int {
			return runConfigShow([]string{"--config-dir", tmpDir})
		})
		if code != 0 {
			t.Fatalf("runConfigShow() code = %d, stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "service:") {
			t.Fatalf("stdout missing service section: %s", stdout)
		}
	})

	t.Run("show specific field json", func(t *testing.T) {
		code, stdout, stderr := captureOutputWithExitCode(t, func() int {
			return runConfigShow([]string{"--config-dir", tmpDir, "--json", "service.log_level"})
		})
		if code != 0 {
			t.Fatalf("runConfigShow() code = %d, stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "\"info\"") {
			t.Fatalf("stdout missing log_level value: %s", stdout)
		}
	})
}

func TestRunSystemStatusJSONHealthy(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")

	if err := os.MkdirAll(filepath.Join(tmpDir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	_ = db.Close()

	configYAML := `
service:
  tick_interval: 30s
  log_level: info
state:
  path: ` + dbPath + `
plugin_roots:
  - ` + filepath.Join(tmpDir, "plugins") + `
plugins: {}
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemStatus([]string{"--config", configPath, "--json"})
	})
	if code != 0 {
		t.Fatalf("runSystemStatus() code = %d, stderr: %s", code, stderr)
	}

	var report struct {
		Healthy bool `json:"healthy"`
		Checks  []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("failed to parse JSON status output: %v\noutput=%s", err, stdout)
	}
	if !report.Healthy {
		t.Fatalf("expected healthy=true, got false; output=%s", stdout)
	}
	if len(report.Checks) < 4 {
		t.Fatalf("expected at least 4 checks, got %d", len(report.Checks))
	}
}

func TestRunSystemStatusConfigLoadFailure(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("invalid: [yaml"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, _ := captureOutputWithExitCode(t, func() int {
		return runSystemStatus([]string{"--config", configPath})
	})
	if code == 0 {
		t.Fatalf("runSystemStatus() should fail for invalid config; stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "config_load: FAIL") {
		t.Fatalf("expected config_load failure in output; stdout=%s", stdout)
	}
	if !strings.Contains(stdout, "state_db: FAIL") || !strings.Contains(stdout, "pid_lock: FAIL") {
		t.Fatalf("expected dependent checks to fail when config load fails; stdout=%s", stdout)
	}
}

func TestRunSystemStatusDetectsActivePIDLock(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")

	if err := os.MkdirAll(filepath.Join(tmpDir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	_ = db.Close()

	configYAML := `
service:
  tick_interval: 30s
  log_level: info
state:
  path: ` + dbPath + `
plugin_roots:
  - ` + filepath.Join(tmpDir, "plugins") + `
plugins: {}
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfigForTool(configPath)
	if err != nil {
		t.Fatalf("loadConfigForTool: %v", err)
	}
	lockPath := getPIDLockPath(cfg)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := captureOutputWithExitCode(t, func() int {
		return runSystemStatus([]string{"--config", configPath, "--json"})
	})
	if code == 0 {
		t.Fatalf("runSystemStatus() should fail when active pid lock exists; stderr=%s stdout=%s", stderr, stdout)
	}

	var report struct {
		Healthy bool `json:"healthy"`
		Checks  []struct {
			Name      string `json:"name"`
			OK        bool   `json:"ok"`
			ActivePID int    `json:"active_pid"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("failed to parse JSON status output: %v\noutput=%s", err, stdout)
	}
	if report.Healthy {
		t.Fatalf("expected healthy=false when active lock exists; output=%s", stdout)
	}

	foundPIDCheck := false
	for _, c := range report.Checks {
		if c.Name == "pid_lock" {
			foundPIDCheck = true
			if c.OK {
				t.Fatalf("expected pid_lock check to fail when active pid exists; output=%s", stdout)
			}
			if c.ActivePID != os.Getpid() {
				t.Fatalf("expected active_pid=%d, got %d", os.Getpid(), c.ActivePID)
			}
		}
	}
	if !foundPIDCheck {
		t.Fatalf("expected pid_lock check in output; output=%s", stdout)
	}
}

func TestValidateScheduledCommands(t *testing.T) {
	registry := plugin.NewRegistry()
	if err := registry.Add(&plugin.Plugin{
		Name: "echo",
		Commands: plugin.Commands{
			{Name: "poll", Type: plugin.CommandTypeWrite},
			{Name: "token_refresh", Type: plugin.CommandTypeWrite},
		},
	}); err != nil {
		t.Fatalf("registry add: %v", err)
	}

	t.Run("valid scheduled command", func(t *testing.T) {
		cfg := &config.Config{
			Plugins: map[string]config.PluginConf{
				"echo": {
					Enabled: true,
					Schedules: []config.ScheduleConfig{
						{ID: "refresh", Every: "1h", Command: "token_refresh"},
					},
				},
			},
		}
		if err := validateScheduledCommands(cfg, registry); err != nil {
			t.Fatalf("validateScheduledCommands() error = %v", err)
		}
	})

	t.Run("unsupported scheduled command", func(t *testing.T) {
		cfg := &config.Config{
			Plugins: map[string]config.PluginConf{
				"echo": {
					Enabled: true,
					Schedules: []config.ScheduleConfig{
						{ID: "bad", Every: "1h", Command: "missing"},
					},
				},
			},
		}
		err := validateScheduledCommands(cfg, registry)
		if err == nil {
			t.Fatal("expected error for unsupported scheduled command")
		}
		if !strings.Contains(err.Error(), `unsupported command "missing"`) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestRunJobInspect(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	configPath := filepath.Join(tmpDir, "config.yaml")

	configYAML := `
service:
  name: test-gw
state:
  path: ` + dbPath + `
plugin_roots:
  - ./plugins
plugins: {}
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	q := queue.New(db)
	jobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	t.Run("inspect human output", func(t *testing.T) {
		code, stdout, stderr := captureOutputWithExitCode(t, func() int {
			return runInspect([]string{"--config", configPath, jobID})
		})
		if code != 0 {
			t.Fatalf("runInspect() code = %d, stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "Lineage Report") || !strings.Contains(stdout, jobID) {
			t.Fatalf("stdout missing report headers or job ID: %s", stdout)
		}
	})

	t.Run("inspect json output", func(t *testing.T) {
		code, stdout, stderr := captureOutputWithExitCode(t, func() int {
			return runInspect([]string{"--config", configPath, jobID, "--json"})
		})
		if code != 0 {
			t.Fatalf("runInspect() code = %d, stderr: %s", code, stderr)
		}
		var report struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal([]byte(stdout), &report); err != nil {
			t.Fatalf("failed to parse JSON report: %v\noutput=%s", err, stdout)
		}
		if report.JobID != jobID {
			t.Fatalf("job_id mismatch: got %s, want %s", report.JobID, jobID)
		}
	})
}

func TestResolveConfigDirFromFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("service: {}\nstate: {path: ./state.db}\nplugins: {}\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	resolved := resolveConfigDir(configPath)
	if resolved != tmpDir {
		t.Fatalf("resolveConfigDir(file) = %q, want %q", resolved, tmpDir)
	}
}

func TestResolveConfigDirFromDirectoryPath(t *testing.T) {
	tmpDir := t.TempDir()

	resolved := resolveConfigDir(tmpDir)
	if resolved != tmpDir {
		t.Fatalf("resolveConfigDir(dir) = %q, want %q", resolved, tmpDir)
	}
}

func TestBuildValidatedGatewayAPIURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		base     string
		endpoint string
		want     string
		wantErr  string
	}{
		{
			name:     "joins localhost base and endpoint",
			base:     "http://localhost:8080/api",
			endpoint: "/jobs?limit=10",
			want:     "http://localhost:8080/api/jobs?limit=10",
		},
		{
			name:     "allows private gateway IP",
			base:     "https://192.168.1.20:8443",
			endpoint: "/healthz",
			want:     "https://192.168.1.20:8443/healthz",
		},
		{
			name:     "rejects public gateway host",
			base:     "https://example.com",
			endpoint: "/jobs",
			wantErr:  "host must be localhost or an IP address",
		},
		{
			name:     "rejects endpoint with host",
			base:     "http://localhost:8080",
			endpoint: "//example.com/jobs",
			wantErr:  "endpoint must be an absolute path",
		},
		{
			name:     "rejects unsupported scheme",
			base:     "file:///tmp/ductile.sock",
			endpoint: "/jobs",
			wantErr:  "scheme must be http or https",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildValidatedGatewayAPIURL(tc.base, tc.endpoint)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatal("expected error")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want to contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildValidatedGatewayAPIURL() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("buildValidatedGatewayAPIURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
