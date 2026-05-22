package dispatch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/log"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/relay"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/conditions"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
	"gopkg.in/yaml.v3"
)

func TestMain(m *testing.M) {
	log.Setup("ERROR") // Suppress logs in tests
	os.Exit(m.Run())
}

func setupTestDispatcher(t *testing.T) (*Dispatcher, *sql.DB, string, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	// Create test plugin directory
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("failed to create plugins dir: %v", err)
	}

	registry := plugin.NewRegistry()
	hub := events.NewHub(128)

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}

	disp := New(q, st, contextStore, nil, registry, hub, cfg)

	cleanup := func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
		if err := os.RemoveAll(tmpDir); err != nil {
			t.Fatalf("RemoveAll(%s): %v", tmpDir, err)
		}
	}

	return disp, db, pluginsDir, cleanup
}

func createTestPlugin(t *testing.T, pluginsDir, name, script string) *plugin.Plugin {
	t.Helper()
	return createTestPluginWithManifestExtra(t, pluginsDir, name, script, "")
}

func createTestPluginWithManifestExtra(t *testing.T, pluginsDir, name, script, manifestExtra string) *plugin.Plugin {
	t.Helper()

	pluginDir := filepath.Join(pluginsDir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("failed to create plugin dir: %v", err)
	}

	// Write manifest
	manifest := fmt.Sprintf(`manifest_spec: ductile.plugin
manifest_version: 1
name: %s
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
  - name: handle
    type: write
  - name: health
    type: read
%s`, name, manifestExtra)

	manifestPath := filepath.Join(pluginDir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0644); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	// Write executable script
	scriptPath := filepath.Join(pluginDir, "run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	// Load and validate plugin
	reg, err := plugin.DiscoverManyWithOptions([]string{pluginsDir}, func(level, msg string, args ...any) {}, plugin.DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("failed to discover plugins: %v", err)
	}

	plug, ok := reg.Get(name)
	if !ok {
		t.Fatalf("plugin %q not found after discovery", name)
	}

	return plug
}

func TestDispatcher_ExecuteJob_Success(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	// Create a simple echo plugin that returns success
	script := `#!/bin/bash
read input
echo '{"status": "ok", "result": "ok", "state_updates": {"last_run": "2024-01-01T00:00:00Z"}}'
`
	plug := createTestPlugin(t, pluginsDir, "echo", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(echo): %v", err)
	}

	// Configure plugin
	disp.cfg.Plugins["echo"] = config.PluginConf{
		Enabled: true,
		Config:  map[string]any{"test": "value"},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()

	// Enqueue a job
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue job: %v", err)
	}

	// Dequeue and execute
	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("failed to dequeue job: %v", err)
	}
	if job == nil {
		t.Fatal("expected job, got nil")
	}

	disp.executeJob(ctx, job)

	// Verify job completed
	var status string
	err = db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query job status: %v", err)
	}

	if status != "succeeded" {
		t.Errorf("expected status=succeeded, got %s", status)
	}

	// Verify state was updated
	pluginState, err := disp.state.Get(ctx, "echo")
	if err != nil {
		t.Fatalf("failed to get plugin state: %v", err)
	}

	var stateMap map[string]any
	if err := json.Unmarshal(pluginState, &stateMap); err != nil {
		t.Fatalf("failed to unmarshal state: %v", err)
	}

	if lastRun, ok := stateMap["last_run"]; !ok || lastRun != "2024-01-01T00:00:00Z" {
		t.Errorf("expected last_run in state, got %v", stateMap)
	}
}

// TestDispatcher_ExecuteJob_RequestEnvelopeHasNoWorkspaceDir is the Sprint 18
// regression. The dispatcher must not include a workspace_dir field in the
// protocol-v2 request envelope it writes to the plugin's stdin, and the
// plugin must still spawn and complete end-to-end without one.
func TestDispatcher_ExecuteJob_RequestEnvelopeHasNoWorkspaceDir(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	captureDir := t.TempDir()
	capturePath := filepath.Join(captureDir, "stdin.json")

	// Plugin tees stdin to a known path and returns success. We assert against
	// the raw bytes written to the plugin's stdin, so this captures whatever
	// the dispatcher actually sent over the wire.
	script := fmt.Sprintf(`#!/bin/bash
cat > %q
echo '{"status":"ok","result":"captured"}'
`, capturePath)

	plug := createTestPlugin(t, pluginsDir, "capture", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(capture): %v", err)
	}

	disp.cfg.Plugins["capture"] = config.PluginConf{
		Enabled:  true,
		Timeouts: &config.TimeoutsConfig{Poll: 5 * time.Second},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "capture",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil || job == nil {
		t.Fatalf("Dequeue: job=%v err=%v", job, err)
	}
	disp.executeJob(ctx, job)

	var status string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != string(queue.StatusSucceeded) {
		t.Fatalf("job status = %q, want succeeded", status)
	}

	rawStdin, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	if len(rawStdin) == 0 {
		t.Fatalf("plugin stdin was empty; nothing was written by dispatcher")
	}

	// Belt-and-braces: assert both at the JSON level (no workspace_dir key)
	// and at the byte level (no workspace_dir substring), since either form
	// of regression would re-introduce the field.
	var envelope map[string]any
	if err := json.Unmarshal(rawStdin, &envelope); err != nil {
		t.Fatalf("unmarshal captured stdin: %v (raw=%q)", err, string(rawStdin))
	}
	if _, present := envelope["workspace_dir"]; present {
		t.Fatalf("protocol envelope includes workspace_dir: %s", string(rawStdin))
	}
	if strings.Contains(string(rawStdin), "workspace_dir") {
		t.Fatalf("workspace_dir substring leaked into request envelope: %s", string(rawStdin))
	}
}

func TestDispatcher_ExecuteCoreRelayStep(t *testing.T) {
	disp, db, _, cleanup := setupTestDispatcher(t)
	defer cleanup()

	var got relay.Envelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("relay method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/ingest/peer/home-primary" {
			t.Fatalf("relay path = %s", r.URL.Path)
		}
		if peer := r.Header.Get(relay.HeaderPeer); peer != "home-primary" {
			t.Fatalf("relay peer header = %q, want home-primary", peer)
		}
		if keyID := r.Header.Get(relay.HeaderKeyID); keyID != "v1" {
			t.Fatalf("relay key id = %q, want v1", keyID)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode relay envelope: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted","peer":"home-primary","event_type":"backup.ready","receiver_event_id":"evt-remote","job_id":"job-remote"}`))
	}))
	defer server.Close()

	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name: "ship-backup",
		On:   "backup.archive.created",
		Steps: []dsl.StepSpec{{
			ID: "relay-to-lab",
			Relay: &dsl.RelaySpec{
				To:        "lab",
				Event:     "backup.ready",
				DedupeKey: "payload.archive_id",
				With: map[string]string{
					"archive_id":   "payload.archive_id",
					"archive_path": "payload.archive_path",
				},
				Baggage: &dsl.BaggageSpec{
					Mappings: map[string]string{"trace_id": "context.trace_id"},
				},
			},
		}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	disp.router = router.New(set, nil)
	disp.cfg.Service.Name = "home-primary"
	disp.cfg.Tokens = []config.TokenEntry{{Name: "relay_lab_v1", Key: "shared-secret"}}
	disp.cfg.RelayInstances = []config.RelayInstanceConfig{{
		Name:        "lab",
		Enabled:     true,
		BaseURL:     server.URL,
		IngressPath: "/ingest/peer/home-primary",
		SecretRef:   "relay_lab_v1",
		KeyID:       "v1",
		Allow:       []string{"backup.ready"},
	}}

	ctx := context.Background()
	eventCtx, err := disp.contexts.Create(ctx, nil, "ship-backup", "relay-to-lab", json.RawMessage(`{"trace_id":"tr-1"}`))
	if err != nil {
		t.Fatalf("ContextStore.Create: %v", err)
	}
	payload, err := json.Marshal(protocol.Event{
		Type:    "backup.archive.created",
		EventID: "evt-local",
		Payload: map[string]any{
			"archive_id":   "archive-1",
			"archive_path": "/srv/backups/latest.tar.zst",
			"ignored":      "not relayed",
		},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "core.relay",
		Command:        "handle",
		Payload:        payload,
		SubmittedBy:    "route",
		EventContextID: &eventCtx.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue(core.relay): %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil || job == nil {
		t.Fatalf("Dequeue(core.relay): job=%v err=%v", job, err)
	}

	disp.executeJob(ctx, job)

	result, err := disp.queue.GetJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJobByID(core.relay): %v", err)
	}
	if result.Status != queue.StatusSucceeded {
		t.Fatalf("core.relay status = %q, want %q, error=%v", result.Status, queue.StatusSucceeded, result.LastError)
	}
	if got.Event.Type != "backup.ready" {
		t.Fatalf("relayed event type = %q, want backup.ready", got.Event.Type)
	}
	if got.Event.DedupeKey != "archive-1" {
		t.Fatalf("relayed dedupe key = %q, want archive-1", got.Event.DedupeKey)
	}
	if got.Event.Payload["archive_id"] != "archive-1" || got.Event.Payload["archive_path"] != "/srv/backups/latest.tar.zst" {
		t.Fatalf("relayed payload = %+v", got.Event.Payload)
	}
	if _, exists := got.Event.Payload["ignored"]; exists {
		t.Fatalf("relayed payload included non-projected field: %+v", got.Event.Payload)
	}
	if got.Baggage["trace_id"] != "tr-1" {
		t.Fatalf("relayed baggage = %+v, want trace_id", got.Baggage)
	}
	if got.Origin.Instance != "home-primary" || got.Origin.JobID != job.ID || got.Origin.EventID != "evt-local" {
		t.Fatalf("relayed origin = %+v", got.Origin)
	}

	var status string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status); err != nil {
		t.Fatalf("query relay job status: %v", err)
	}
	if status != string(queue.StatusSucceeded) {
		t.Fatalf("db relay status = %s, want succeeded", status)
	}
}

func TestDispatcher_ExecuteJob_SchedulerPollLifecycleEvents(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status": "ok", "result": "ok"}'
`
	plug := createTestPlugin(t, pluginsDir, "echo", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(echo): %v", err)
	}
	disp.cfg.Plugins["echo"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	dedupeKey := "echo:poll:primary"
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "echo",
		Command:     "poll",
		SubmittedBy: disp.cfg.Service.Name,
		DedupeKey:   &dedupeKey,
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if job == nil {
		t.Fatal("expected job, got nil")
	}

	disp.executeJob(ctx, job)

	var status string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", status)
	}

	started := filterEventsByType(disp.events.SnapshotSince(0), "poll.started")
	if len(started) != 1 {
		t.Fatalf("poll.started events = %d, want 1", len(started))
	}
	startedPayload := eventPayload(t, started[0])
	if startedPayload["job_id"] != jobID || startedPayload["schedule_id"] != "primary" {
		t.Fatalf("poll.started payload = %+v", startedPayload)
	}

	completed := filterEventsByType(disp.events.SnapshotSince(0), "poll.completed")
	if len(completed) != 1 {
		t.Fatalf("poll.completed events = %d, want 1", len(completed))
	}
	completedPayload := eventPayload(t, completed[0])
	if completedPayload["job_id"] != jobID || completedPayload["status"] != "succeeded" || completedPayload["schedule_id"] != "primary" {
		t.Fatalf("poll.completed payload = %+v", completedPayload)
	}
}

func TestDispatcher_ExecuteJob_FileWatchPollRecordsFactAndDerivedState(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status":"ok","result":"file_watch poll complete: watches=1 events=0","state_updates":{"watches":{"single-file":{"exists":true,"fingerprint":"abc123","path":"/tmp/file.txt","updated_at":"2026-04-22T01:02:03Z"}},"last_poll_at":"2026-04-22T01:02:03Z"}}'
`
	plug := createTestPluginWithManifestExtra(t, pluginsDir, "file_watch", script, `
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: file_watch.snapshot
    compatibility_view: mirror_object`)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(file_watch): %v", err)
	}

	disp.cfg.Plugins["file_watch"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "file_watch",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if job == nil {
		t.Fatal("expected job, got nil")
	}

	disp.executeJob(ctx, job)

	var status string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", status)
	}

	facts, err := disp.state.ListFacts(ctx, "file_watch", state.FactTypeFileWatchSnapshot, 10)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("len(facts) = %d, want 1", len(facts))
	}
	if facts[0].JobID != jobID {
		t.Fatalf("fact job_id = %q, want %q", facts[0].JobID, jobID)
	}

	pluginState, err := disp.state.Get(ctx, "file_watch")
	if err != nil {
		t.Fatalf("Get(file_watch): %v", err)
	}
	if string(pluginState) != string(facts[0].FactJSON) {
		t.Fatalf("compatibility state = %s, want %s", string(pluginState), string(facts[0].FactJSON))
	}
}

func TestPluginFactsFromStateUpdatesUsesManifestDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		manifest              string
		job                   *queue.Job
		updates               json.RawMessage
		wantTypes             []string
		wantCompatibilityView string
	}{
		{
			name: "file_watch poll",
			manifest: `manifest_spec: ductile.plugin
manifest_version: 1
name: file_watch
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: file_watch.snapshot
    compatibility_view: mirror_object
`,
			job:                   &queue.Job{ID: "job-1", Plugin: "file_watch", Command: "poll"},
			updates:               json.RawMessage(`{"last_poll_at":"2026-04-22T00:00:00Z","watches":{}}`),
			wantTypes:             []string{state.FactTypeFileWatchSnapshot},
			wantCompatibilityView: "mirror_object",
		},
		{
			name: "multiple declarations for one command",
			manifest: `manifest_spec: ductile.plugin
manifest_version: 1
name: file_watch
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: file_watch.snapshot
    compatibility_view: mirror_object
  - when:
      command: poll
    from: state_updates
    fact_type: file_watch.audit
`,
			job:                   &queue.Job{ID: "job-2", Plugin: "file_watch", Command: "poll"},
			updates:               json.RawMessage(`{"last_poll_at":"2026-04-22T00:00:00Z","watches":{}}`),
			wantTypes:             []string{state.FactTypeFileWatchSnapshot, "file_watch.audit"},
			wantCompatibilityView: "mirror_object",
		},
		{
			name: "command without declaration is ignored",
			manifest: `manifest_spec: ductile.plugin
manifest_version: 1
name: stress
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
  - name: state
    type: write
fact_outputs:
  - when:
      command: state
    from: state_updates
    fact_type: stress.state_snapshot
    compatibility_view: mirror_object
`,
			job:     &queue.Job{ID: "job-3", Plugin: "stress", Command: "poll"},
			updates: json.RawMessage(`{"count":42}`),
		},
		{
			name: "unknown plugin is ignored",
			manifest: `manifest_spec: ductile.plugin
manifest_version: 1
name: other
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: other.snapshot
`,
			job:     &queue.Job{ID: "job-4", Plugin: "echo", Command: "poll"},
			updates: json.RawMessage(`{"last_run":"2026-04-22T00:00:00Z"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var manifest plugin.Manifest
			if err := yaml.Unmarshal([]byte(tt.manifest), &manifest); err != nil {
				t.Fatalf("yaml.Unmarshal: %v", err)
			}
			registry := plugin.NewRegistry()
			if err := registry.Add(&plugin.Plugin{
				Name:        manifest.Name,
				Commands:    manifest.Commands,
				FactOutputs: manifest.FactOutputs,
			}); err != nil {
				t.Fatalf("registry.Add: %v", err)
			}

			facts, err := pluginFactsFromStateUpdates(tt.job, registry, tt.updates)
			if err != nil {
				t.Fatalf("pluginFactsFromStateUpdates() error = %v", err)
			}
			if len(facts) != len(tt.wantTypes) {
				t.Fatalf("len(facts) = %d, want %d", len(facts), len(tt.wantTypes))
			}
			if len(tt.wantTypes) == 0 {
				return
			}

			for i, fact := range facts {
				if fact.Fact.PluginName != tt.job.Plugin || fact.Fact.Command != tt.job.Command || fact.Fact.JobID != tt.job.ID {
					t.Fatalf("unexpected fact identity: %+v", fact.Fact)
				}
				if fact.Fact.FactType != tt.wantTypes[i] {
					t.Fatalf("fact[%d] type = %q, want %q", i, fact.Fact.FactType, tt.wantTypes[i])
				}
				if string(fact.Fact.FactJSON) != string(tt.updates) {
					t.Fatalf("fact[%d] json = %s, want %s", i, string(fact.Fact.FactJSON), string(tt.updates))
				}
			}
			if facts[0].CompatibilityView != tt.wantCompatibilityView {
				t.Fatalf("CompatibilityView = %q, want %q", facts[0].CompatibilityView, tt.wantCompatibilityView)
			}
		})
	}
}

func TestDispatcher_ExecuteJob_PluginError(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	// Plugin that returns error status
	script := `#!/bin/bash
read input
echo '{"status": "error", "error": "something went wrong"}'
`
	plug := createTestPlugin(t, pluginsDir, "failing", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(failing): %v", err)
	}

	disp.cfg.Plugins["failing"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()

	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "failing",
		Command:     "poll",
		MaxAttempts: 1,
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue job: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("failed to dequeue job: %v", err)
	}

	disp.executeJob(ctx, job)

	// Verify job failed
	var status, lastError string
	err = db.QueryRow("SELECT status, last_error FROM job_queue WHERE id = ?", jobID).Scan(&status, &lastError)
	if err != nil {
		t.Fatalf("failed to query job: %v", err)
	}

	if status != "failed" {
		t.Errorf("expected status=failed, got %s", status)
	}

	if lastError != "something went wrong" {
		t.Errorf("expected last_error='something went wrong', got %s", lastError)
	}
}

func TestDispatcher_ExecuteJob_Timeout(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	// Plugin that sleeps longer than timeout
	// Use exec to replace bash with sleep so SIGTERM goes directly to sleep
	script := `#!/bin/bash
read input
exec sleep 10
`
	plug := createTestPlugin(t, pluginsDir, "sleeper", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(sleeper): %v", err)
	}

	disp.cfg.Plugins["sleeper"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Poll: 1 * time.Second, // Very short timeout
		},
	}

	ctx := context.Background()

	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "sleeper",
		Command:     "poll",
		MaxAttempts: 1,
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue job: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("failed to dequeue job: %v", err)
	}

	start := time.Now()
	disp.executeJob(ctx, job)
	elapsed := time.Since(start)

	// Should timeout within reasonable time (1s timeout + 5s grace + some margin)
	if elapsed > 8*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}

	// Verify job timed out
	var status string
	err = db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query job: %v", err)
	}

	if status != "timed_out" {
		t.Errorf("expected status=timed_out, got %s", status)
	}
}

func TestDispatcher_ExecuteJob_ProtocolError(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	// Plugin that outputs invalid JSON
	script := `#!/bin/bash
read input
echo 'not valid json'
`
	plug := createTestPlugin(t, pluginsDir, "broken", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(broken): %v", err)
	}

	disp.cfg.Plugins["broken"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()

	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "broken",
		Command:     "poll",
		MaxAttempts: 1,
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue job: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("failed to dequeue job: %v", err)
	}

	disp.executeJob(ctx, job)

	// Verify job failed due to protocol error
	var status string
	err = db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query job: %v", err)
	}

	if status != "failed" {
		t.Errorf("expected status=failed, got %s", status)
	}
}

func TestDispatcher_ExecuteJob_WithTemplateErrorFailsJob(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status":"ok","result":"ok"}'
`
	plug := createTestPlugin(t, pluginsDir, "echo", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(echo): %v", err)
	}

	set, err := router.LoadFromConfigFiles([]string{writePipelineFile(t, t.TempDir(), `pipelines:
  - name: with-pipeline
    on: event.start
    steps:
      - id: notify
        uses: echo
        with:
          message: "{payload.missing}"
`)}, disp.registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}
	disp.router = set

	disp.cfg.Plugins["echo"] = config.PluginConf{
		Enabled:  true,
		Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second},
	}

	ctx := context.Background()
	eventCtx, err := disp.contexts.Create(ctx, nil, "with-pipeline", "notify", []byte(`{"origin_channel_id":"chan-1"}`))
	if err != nil {
		t.Fatalf("ContextStore.Create(): %v", err)
	}

	payload, err := json.Marshal(protocol.Event{
		Type:    "event.start",
		Payload: map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("json.Marshal(event): %v", err)
	}

	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "echo",
		Command:        "handle",
		Payload:        payload,
		EventContextID: &eventCtx.ID,
		SubmittedBy:    "test",
	})
	if err != nil {
		t.Fatalf("Enqueue(): %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(): %v", err)
	}
	if job == nil {
		t.Fatal("expected queued job")
	}

	disp.executeJob(ctx, job)

	var status, lastError string
	if err := db.QueryRow("SELECT status, last_error FROM job_queue WHERE id = ?", jobID).Scan(&status, &lastError); err != nil {
		t.Fatalf("query job status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("status = %q, want %q", status, "failed")
	}
	if !strings.Contains(lastError, `resolve "payload.missing": path not found`) {
		t.Fatalf("last_error = %q, want path resolution failure", lastError)
	}
}

func writePipelineFile(t *testing.T, dir, body string) string {
	t.Helper()

	path := filepath.Join(dir, "pipelines.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
	return path
}

func TestCalculateBackoffDelay(t *testing.T) {
	base := 2 * time.Second

	delay1 := calculateBackoffDelay(base, 1, 0)
	if delay1 != 2*time.Second {
		t.Fatalf("attempt 1 delay = %v, want %v", delay1, 2*time.Second)
	}

	delay2 := calculateBackoffDelay(base, 2, 500*time.Millisecond)
	if delay2 != 4500*time.Millisecond {
		t.Fatalf("attempt 2 delay = %v, want %v", delay2, 4500*time.Millisecond)
	}

	delay3 := calculateBackoffDelay(base, 3, 0)
	if delay3 != 8*time.Second {
		t.Fatalf("attempt 3 delay = %v, want %v", delay3, 8*time.Second)
	}
}

func TestDispatcher_ExecuteJob_RetryScheduledOnPluginError(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status":"error","error":"transient failure"}'
`
	plug := createTestPlugin(t, pluginsDir, "retrying", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}
	disp.cfg.Plugins["retrying"] = config.PluginConf{
		Enabled: true,
		Retry: &config.RetryConfig{
			MaxAttempts: 3,
			BackoffBase: 1 * time.Second,
		},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "retrying",
		Command:     "poll",
		SubmittedBy: "test",
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	disp.executeJob(ctx, job)

	var (
		status      string
		attempt     int
		nextRetryAt sql.NullString
	)
	if err := db.QueryRow(`SELECT status, attempt, next_retry_at FROM job_queue WHERE id = ?`, jobID).Scan(&status, &attempt, &nextRetryAt); err != nil {
		t.Fatalf("query retry state: %v", err)
	}
	if status != string(queue.StatusQueued) {
		t.Fatalf("status = %s, want queued", status)
	}
	if attempt != 2 {
		t.Fatalf("attempt = %d, want 2", attempt)
	}
	if !nextRetryAt.Valid || nextRetryAt.String == "" {
		t.Fatalf("next_retry_at should be set")
	}

	retryEvents := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_scheduled")
	if len(retryEvents) == 0 {
		t.Fatalf("expected job.retry_scheduled event")
	}
	retryEvent := eventPayload(t, retryEvents[0])
	if retryEvent["reason"] != retryReasonPluginError {
		t.Fatalf("retry reason = %v, want %q", retryEvent["reason"], retryReasonPluginError)
	}
	if retryEvent["retry_policy_owner"] != retryPolicyOwner {
		t.Fatalf("retry_policy_owner = %v, want %q", retryEvent["retry_policy_owner"], retryPolicyOwner)
	}
	if retryEvent["plugin_retry_field"] != pluginRetryField {
		t.Fatalf("plugin_retry_field = %v, want %q", retryEvent["plugin_retry_field"], pluginRetryField)
	}
}

func TestDispatcher_ExecuteJob_NonRetryableResponse(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status":"error","error":"bad request","retry":false}'
`
	plug := createTestPlugin(t, pluginsDir, "noretry", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}
	disp.cfg.Plugins["noretry"] = config.PluginConf{
		Enabled: true,
		Retry: &config.RetryConfig{
			MaxAttempts: 3,
			BackoffBase: 1 * time.Second,
		},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "noretry",
		Command:     "poll",
		SubmittedBy: "test",
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	disp.executeJob(ctx, job)

	var status string
	if err := db.QueryRow(`SELECT status FROM job_queue WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != string(queue.StatusFailed) {
		t.Fatalf("status = %s, want failed", status)
	}
	retryScheduled := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_scheduled")
	if len(retryScheduled) != 0 {
		t.Fatalf("expected no retry_scheduled events, got %d", len(retryScheduled))
	}
	retryExhausted := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_exhausted")
	if len(retryExhausted) == 0 {
		t.Fatalf("expected retry_exhausted event for non-retryable failure")
	}
	retryEvent := eventPayload(t, retryExhausted[0])
	if retryEvent["reason"] != retryReasonPluginRetryFalse {
		t.Fatalf("retry exhausted reason = %v, want %q", retryEvent["reason"], retryReasonPluginRetryFalse)
	}
	if retryEvent["retry_policy_owner"] != retryPolicyOwner {
		t.Fatalf("retry_policy_owner = %v, want %q", retryEvent["retry_policy_owner"], retryPolicyOwner)
	}
}

func TestDispatcher_ExecuteJob_NonRetryableExitCode78(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status":"error","error":"config invalid"}'
exit 78
`
	plug := createTestPlugin(t, pluginsDir, "exit78", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}
	disp.cfg.Plugins["exit78"] = config.PluginConf{
		Enabled: true,
		Retry: &config.RetryConfig{
			MaxAttempts: 3,
			BackoffBase: 1 * time.Second,
		},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "exit78",
		Command:     "poll",
		SubmittedBy: "test",
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	disp.executeJob(ctx, job)

	var status string
	if err := db.QueryRow(`SELECT status FROM job_queue WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != string(queue.StatusFailed) {
		t.Fatalf("status = %s, want failed", status)
	}
	retryScheduled := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_scheduled")
	if len(retryScheduled) != 0 {
		t.Fatalf("expected no retry_scheduled events, got %d", len(retryScheduled))
	}
	retryExhausted := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_exhausted")
	if len(retryExhausted) == 0 {
		t.Fatalf("expected retry_exhausted event")
	}
	retryEvent := eventPayload(t, retryExhausted[0])
	if retryEvent["reason"] != retryReasonExitCode78 {
		t.Fatalf("retry exhausted reason = %v, want %q", retryEvent["reason"], retryReasonExitCode78)
	}
}

func TestDispatcher_ExecuteJob_RetryExhaustionReasonVisible(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	script := `#!/bin/bash
read input
echo '{"status":"error","error":"still broken"}'
`
	plug := createTestPlugin(t, pluginsDir, "exhausted", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}
	disp.cfg.Plugins["exhausted"] = config.PluginConf{
		Enabled: true,
		Retry: &config.RetryConfig{
			MaxAttempts: 1,
			BackoffBase: 1 * time.Second,
		},
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}

	ctx := context.Background()
	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "exhausted",
		Command:     "poll",
		SubmittedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	disp.executeJob(ctx, job)

	var status string
	if err := db.QueryRow(`SELECT status FROM job_queue WHERE id = ?`, jobID).Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != string(queue.StatusFailed) {
		t.Fatalf("status = %s, want failed", status)
	}
	retryScheduled := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_scheduled")
	if len(retryScheduled) != 0 {
		t.Fatalf("expected no retry_scheduled events, got %d", len(retryScheduled))
	}
	retryExhausted := filterEventsByType(disp.events.SnapshotSince(0), "job.retry_exhausted")
	if len(retryExhausted) == 0 {
		t.Fatalf("expected retry_exhausted event")
	}
	retryEvent := eventPayload(t, retryExhausted[0])
	if retryEvent["reason"] != retryReasonAttemptsExhausted {
		t.Fatalf("retry exhausted reason = %v, want %q", retryEvent["reason"], retryReasonAttemptsExhausted)
	}
	if retryEvent["retry_decision_reason"] != retryReasonPluginError {
		t.Fatalf("retry_decision_reason = %v, want %q", retryEvent["retry_decision_reason"], retryReasonPluginError)
	}
}

func TestDispatcher_ExecuteJob_HandleCommand(t *testing.T) {
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	defer cleanup()

	// Plugin that echoes the event type it received
	script := `#!/bin/bash
read input
# Extract event type from JSON (basic bash parsing)
echo '{"status": "ok", "result": "handled event", "logs": [{"level": "info", "message": "handled event"}]}'
`
	plug := createTestPlugin(t, pluginsDir, "handler", script)
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}

	disp.cfg.Plugins["handler"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Handle: 5 * time.Second,
		},
	}

	ctx := context.Background()

	// Create an event payload
	event := protocol.Event{
		Type: "test.event",
		Payload: map[string]any{
			"key": "value",
		},
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	jobID, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "handler",
		Command:     "handle",
		Payload:     eventJSON,
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("failed to enqueue job: %v", err)
	}

	job, err := disp.queue.Dequeue(ctx)
	if err != nil {
		t.Fatalf("failed to dequeue job: %v", err)
	}

	disp.executeJob(ctx, job)

	// Verify job succeeded
	var status string
	err = db.QueryRow("SELECT status FROM job_queue WHERE id = ?", jobID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query job: %v", err)
	}

	if status != "succeeded" {
		t.Errorf("expected status=succeeded, got %s", status)
	}
}

func TestDispatcher_GetTimeout(t *testing.T) {
	disp, _, _, cleanup := setupTestDispatcher(t)
	defer cleanup()

	tests := []struct {
		name     string
		timeouts *config.TimeoutsConfig
		command  string
		want     time.Duration
	}{
		{
			name:     "poll with custom timeout",
			timeouts: &config.TimeoutsConfig{Poll: 90 * time.Second},
			command:  "poll",
			want:     90 * time.Second,
		},
		{
			name:     "poll with default",
			timeouts: nil,
			command:  "poll",
			want:     60 * time.Second,
		},
		{
			name:     "handle with custom timeout",
			timeouts: &config.TimeoutsConfig{Handle: 180 * time.Second},
			command:  "handle",
			want:     180 * time.Second,
		},
		{
			name:     "health with default",
			timeouts: nil,
			command:  "health",
			want:     10 * time.Second,
		},
		{
			name:     "unknown command defaults to 60s",
			timeouts: nil,
			command:  "unknown",
			want:     60 * time.Second,
		},
		{
			name: "override honors operator-defined custom command timeout (P2-05)",
			timeouts: &config.TimeoutsConfig{
				Overrides: map[string]time.Duration{"cpu": 15 * time.Second},
			},
			command: "cpu",
			want:    15 * time.Second,
		},
		{
			name: "override of poll wins over fixed Poll field (P2-05)",
			timeouts: &config.TimeoutsConfig{
				Poll:      60 * time.Second,
				Overrides: map[string]time.Duration{"poll": 5 * time.Second},
			},
			command: "poll",
			want:    5 * time.Second,
		},
		{
			name: "zero override falls through to fixed-field resolution (P2-05)",
			timeouts: &config.TimeoutsConfig{
				Poll:      45 * time.Second,
				Overrides: map[string]time.Duration{"poll": 0},
			},
			command: "poll",
			want:    45 * time.Second,
		},
		{
			name: "custom command with no override still defaults to 60s",
			timeouts: &config.TimeoutsConfig{
				Overrides: map[string]time.Duration{"cpu": 15 * time.Second},
			},
			command: "io",
			want:    60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := disp.getTimeout(tt.timeouts, tt.command)
			if got != tt.want {
				t.Errorf("getTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDispatcher_RoutesTwoHopChainWithContextAndWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("MkdirAll(pluginsDir): %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	scriptA := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"chain start","events":[{"type":"chain.start","dedupe_key":"chain:start:hello","payload":{"origin_channel_id":"chan-1","message":"hello"}}]}'
`
	scriptB := `#!/bin/bash
read input
echo '{"status":"ok","result":"handled by b","logs":[{"level":"info","message":"handled by b"}]}'
`

	registry := plugin.NewRegistry()
	plugA := createTestPlugin(t, pluginsDir, "plugin-a", scriptA)
	plugB := createTestPlugin(t, pluginsDir, "plugin-b", scriptB)
	if err := registry.Add(plugA); err != nil {
		t.Fatalf("registry.Add(plugin-a): %v", err)
	}
	if err := registry.Add(plugB); err != nil {
		t.Fatalf("registry.Add(plugin-b): %v", err)
	}

	pipelineYAML := `pipelines:
  - name: chain
    on: chain.start
    steps:
      - id: step_b
        uses: plugin-b
        baggage:
          origin_channel_id: payload.origin_channel_id
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}
	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Plugins["plugin-a"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Poll: 5 * time.Second,
		},
	}
	cfg.Plugins["plugin-b"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Handle: 5 * time.Second,
		},
	}

	hub := events.NewHub(128)
	disp := New(q, st, contextStore, routerEngine, registry, hub, cfg)
	ctx := context.Background()

	rootJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "plugin-a",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue(root): %v", err)
	}

	rootJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(root): %v", err)
	}
	if rootJob == nil {
		t.Fatalf("expected root job")
	}
	disp.executeJob(ctx, rootJob)

	var rootStatus string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", rootJobID).Scan(&rootStatus); err != nil {
		t.Fatalf("query root status: %v", err)
	}
	if rootStatus != string(queue.StatusSucceeded) {
		t.Fatalf("root status = %s, want %s", rootStatus, queue.StatusSucceeded)
	}

	childJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(child): %v", err)
	}
	if childJob == nil {
		t.Fatalf("expected routed child job")
	} else {
		if childJob.Plugin != "plugin-b" || childJob.Command != "handle" {
			t.Fatalf("unexpected child job: %+v", childJob)
		}
		if childJob.ParentJobID == nil || *childJob.ParentJobID != rootJobID {
			t.Fatalf("child parent_job_id = %v, want %s", childJob.ParentJobID, rootJobID)
		}
		if childJob.EventContextID == nil {
			t.Fatalf("child event_context_id is nil")
		}
		if childJob.DedupeKey == nil || *childJob.DedupeKey != "chain:start:hello" {
			t.Fatalf("child dedupe_key = %v, want %q", childJob.DedupeKey, "chain:start:hello")
		}
	}

	var routedEvent protocol.Event
	if err := json.Unmarshal(childJob.Payload, &routedEvent); err != nil {
		t.Fatalf("unmarshal child payload: %v", err)
	}
	if routedEvent.Type != "chain.start" {
		t.Fatalf("child event type = %q, want %q", routedEvent.Type, "chain.start")
	}
	if routedEvent.Payload["message"] != "hello" {
		t.Fatalf("child event payload missing message: %+v", routedEvent.Payload)
	}

	childContext, err := contextStore.Get(ctx, *childJob.EventContextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(child): %v", err)
	}
	var accumulated map[string]any
	if err := json.Unmarshal(childContext.AccumulatedJSON, &accumulated); err != nil {
		t.Fatalf("unmarshal child context: %v", err)
	}
	if accumulated["origin_channel_id"] != "chan-1" {
		t.Fatalf("origin_channel_id = %#v, want %q", accumulated["origin_channel_id"], "chan-1")
	}

	disp.executeJob(ctx, childJob)

	var childStatus string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE id = ?", childJob.ID).Scan(&childStatus); err != nil {
		t.Fatalf("query child status: %v", err)
	}
	if childStatus != string(queue.StatusSucceeded) {
		t.Fatalf("child status = %s, want %s", childStatus, queue.StatusSucceeded)
	}
}

func TestDispatcherContextUpdatesForDispatchUsesExplicitBaggage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	contextStore := state.NewContextStore(db)
	root, err := contextStore.Create(
		context.Background(),
		nil,
		"chain",
		"root",
		json.RawMessage(`{"origin":{"channel":"chan-1"}}`),
	)
	if err != nil {
		t.Fatalf("ContextStore.Create(root): %v", err)
	}

	pipelineYAML := `pipelines:
  - name: chain
    on: chain.start
    steps:
      - id: step_b
        uses: plugin-b
        baggage:
          summary.text: payload.message
          origin.channel: context.origin.channel
          from: payload.metadata
          namespace: whisper.metadata
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, nil, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	disp := &Dispatcher{
		contexts: contextStore,
		router:   routerEngine,
	}
	updates, err := disp.contextUpdatesForDispatch(context.Background(), router.Dispatch{
		PipelineName:    "chain",
		StepID:          "step_b",
		ParentContextID: root.ID,
		Event: protocol.Event{
			Type: "chain.start",
			Payload: map[string]any{
				"message":   "hello",
				"transient": "do not promote",
				"metadata":  map[string]any{"duration": float64(12)},
			},
		},
	}, routeEventsOptions{})
	if err != nil {
		t.Fatalf("contextUpdatesForDispatch() error = %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(updates, &got); err != nil {
		t.Fatalf("unmarshal updates: %v", err)
	}
	if _, exists := got["message"]; exists {
		t.Fatalf("legacy message promoted into explicit baggage updates: %+v", got)
	}
	if _, exists := got["transient"]; exists {
		t.Fatalf("legacy transient key promoted into explicit baggage updates: %+v", got)
	}
	summary := got["summary"].(map[string]any)
	if summary["text"] != "hello" {
		t.Fatalf("summary.text = %v, want hello", summary["text"])
	}
	origin := got["origin"].(map[string]any)
	if origin["channel"] != "chan-1" {
		t.Fatalf("origin.channel = %v, want chan-1", origin["channel"])
	}
	whisper := got["whisper"].(map[string]any)
	metadata := whisper["metadata"].(map[string]any)
	if metadata["duration"] != float64(12) {
		t.Fatalf("whisper.metadata.duration = %v, want 12", metadata["duration"])
	}
}

func TestDispatcherContextUpdatesForDispatchDoesNotMaskMissingBaggage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	contextStore := state.NewContextStore(db)
	root, err := contextStore.Create(
		context.Background(),
		nil,
		"chain",
		"step_a",
		json.RawMessage(`{"repo":{"name":"demo"}}`),
	)
	if err != nil {
		t.Fatalf("ContextStore.Create(root): %v", err)
	}

	pipelineYAML := `pipelines:
  - name: chain
    on: chain.start
    steps:
      - id: step_b
        uses: plugin-b
        baggage:
          commit.changed: payload.changed
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, nil, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	disp := &Dispatcher{
		contexts: contextStore,
		router:   routerEngine,
	}
	_, err = disp.contextUpdatesForDispatch(context.Background(), router.Dispatch{
		PipelineName:    "chain",
		StepID:          "step_b",
		ParentContextID: root.ID,
		Event: protocol.Event{
			Type:    "ductile.step.skipped",
			Payload: map[string]any{"reason": "if condition evaluated false"},
		},
	}, routeEventsOptions{allowMissingExplicitBaggage: true})
	if err == nil {
		t.Fatal("contextUpdatesForDispatch() error = nil, want missing baggage failure")
	}
	if !strings.Contains(err.Error(), `resolve baggage commit.changed from "payload.changed": path not found`) {
		t.Fatalf("contextUpdatesForDispatch() error = %v, want missing baggage failure", err)
	}
}

func TestDispatcherRoutedChildContextRetainsPipelineInstanceID(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pluginsDir): %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	scriptA := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"step a complete","events":[{"type":"chain.progress","payload":{"origin_channel_id":"chan-1"}}]}'
`
	scriptB := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"step b complete"}'
`

	registry := plugin.NewRegistry()
	plugA := createTestPlugin(t, pluginsDir, "plugin-a", scriptA)
	plugB := createTestPlugin(t, pluginsDir, "plugin-b", scriptB)
	if err := registry.Add(plugA); err != nil {
		t.Fatalf("registry.Add(plugin-a): %v", err)
	}
	if err := registry.Add(plugB); err != nil {
		t.Fatalf("registry.Add(plugin-b): %v", err)
	}

	pipelineYAML := `pipelines:
  - name: chain
    on: chain.start
    steps:
      - id: step_a
        uses: plugin-a
      - id: step_b
        uses: plugin-b
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Plugins["plugin-a"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Handle: 5 * time.Second,
		},
	}
	cfg.Plugins["plugin-b"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Handle: 5 * time.Second,
		},
	}

	disp := New(q, st, contextStore, routerEngine, registry, events.NewHub(32), cfg)
	ctx := context.Background()

	instanceID := "pipeline-instance-123"
	rootUpdates, err := state.WithPipelineInstanceID(nil, instanceID)
	if err != nil {
		t.Fatalf("WithPipelineInstanceID(root): %v", err)
	}
	rootCtx, err := contextStore.Create(ctx, nil, "chain", "", rootUpdates)
	if err != nil {
		t.Fatalf("ContextStore.Create(root): %v", err)
	}
	stepCtx, err := contextStore.Create(ctx, &rootCtx.ID, "chain", "step_a", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ContextStore.Create(step_a): %v", err)
	}

	payload, err := json.Marshal(protocol.Event{Type: "chain.start", Payload: map[string]any{"trigger": "go"}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	rootJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "plugin-a",
		Command:        "handle",
		Payload:        payload,
		SubmittedBy:    "test",
		EventContextID: &stepCtx.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue(root): %v", err)
	}

	rootJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(root): %v", err)
	}
	if rootJob == nil {
		t.Fatal("expected root job")
	}
	disp.executeJob(ctx, rootJob)

	childJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(child): %v", err)
	}
	if childJob == nil {
		t.Fatal("expected routed child job")
	} else {
		if childJob.ParentJobID == nil || *childJob.ParentJobID != rootJobID {
			t.Fatalf("child parent_job_id = %v, want %s", childJob.ParentJobID, rootJobID)
		}
		if childJob.EventContextID == nil {
			t.Fatal("child event_context_id is nil")
		}
	}

	childCtx, err := contextStore.Get(ctx, *childJob.EventContextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(child): %v", err)
	}
	if childCtx.ParentID == nil || *childCtx.ParentID != stepCtx.ID {
		t.Fatalf("child parent_id = %v, want %s", childCtx.ParentID, stepCtx.ID)
	}
	if got := state.PipelineInstanceIDFromAccumulated(childCtx.AccumulatedJSON); got != instanceID {
		t.Fatalf("child pipeline instance id = %q, want %q", got, instanceID)
	}
	var accumulated map[string]any
	if err := json.Unmarshal(childCtx.AccumulatedJSON, &accumulated); err != nil {
		t.Fatalf("unmarshal child accumulated: %v", err)
	}
	if _, exists := accumulated["origin_channel_id"]; exists {
		t.Fatalf("unexpected implicit durable payload in child context: %+v", accumulated)
	}
}

func TestDispatcherCrossPipelineRouteCreatesDestinationRootContext(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pluginsDir): %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)

	scriptA := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"source step complete","events":[{"type":"target.begin","payload":{"content":"hello world"}}]}'
`
	scriptB := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"target step complete"}'
`

	registry := plugin.NewRegistry()
	plugA := createTestPlugin(t, pluginsDir, "plugin-a", scriptA)
	plugB := createTestPlugin(t, pluginsDir, "plugin-b", scriptB)
	if err := registry.Add(plugA); err != nil {
		t.Fatalf("registry.Add(plugin-a): %v", err)
	}
	if err := registry.Add(plugB); err != nil {
		t.Fatalf("registry.Add(plugin-b): %v", err)
	}

	pipelineYAML := `pipelines:
  - name: source
    on: source.start
    steps:
      - id: emit
        uses: plugin-a
  - name: target
    on: target.begin
    steps:
      - id: consume
        uses: plugin-b
        baggage:
          target.content: payload.content
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Plugins["plugin-a"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Handle: 5 * time.Second,
		},
	}
	cfg.Plugins["plugin-b"] = config.PluginConf{
		Enabled: true,
		Timeouts: &config.TimeoutsConfig{
			Handle: 5 * time.Second,
		},
	}

	disp := New(q, st, contextStore, routerEngine, registry, events.NewHub(32), cfg)
	ctx := context.Background()

	sourceInstanceID := "source-instance-123"
	rootUpdates, err := state.WithPipelineInstanceID(nil, sourceInstanceID)
	if err != nil {
		t.Fatalf("WithPipelineInstanceID(root): %v", err)
	}
	rootCtx, err := contextStore.Create(ctx, nil, "source", "", rootUpdates)
	if err != nil {
		t.Fatalf("ContextStore.Create(root): %v", err)
	}
	stepCtx, err := contextStore.Create(ctx, &rootCtx.ID, "source", "emit", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("ContextStore.Create(step): %v", err)
	}

	payload, err := json.Marshal(protocol.Event{Type: "source.start", Payload: map[string]any{"trigger": "go"}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	rootJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:         "plugin-a",
		Command:        "handle",
		Payload:        payload,
		SubmittedBy:    "test",
		EventContextID: &stepCtx.ID,
	})
	if err != nil {
		t.Fatalf("Enqueue(root): %v", err)
	}

	rootJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(root): %v", err)
	}
	if rootJob == nil {
		t.Fatal("expected root job")
	}
	disp.executeJob(ctx, rootJob)

	childJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(child): %v", err)
	}
	if childJob == nil {
		t.Fatal("expected routed child job")
		return
	}
	if childJob.ParentJobID == nil || *childJob.ParentJobID != rootJobID {
		t.Fatalf("child parent_job_id = %v, want %s", childJob.ParentJobID, rootJobID)
	}
	if childJob.EventContextID == nil {
		t.Fatal("child event_context_id is nil")
	}

	childCtx, err := contextStore.Get(ctx, *childJob.EventContextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(child): %v", err)
	}
	if childCtx.ParentID == nil {
		t.Fatal("child parent_id is nil")
	}
	if *childCtx.ParentID == stepCtx.ID {
		t.Fatalf("child parent_id = %s, want destination pipeline root context", *childCtx.ParentID)
	}
	if childCtx.PipelineName != "target" || childCtx.StepID != "consume" {
		t.Fatalf("child context = %s/%s, want target/consume", childCtx.PipelineName, childCtx.StepID)
	}

	targetRootCtx, err := contextStore.Get(ctx, *childCtx.ParentID)
	if err != nil {
		t.Fatalf("ContextStore.Get(target root): %v", err)
	}
	if targetRootCtx.ParentID != nil {
		t.Fatalf("target root parent_id = %v, want nil", targetRootCtx.ParentID)
	}
	if targetRootCtx.PipelineName != "target" || targetRootCtx.StepID != "" {
		t.Fatalf("target root context = %s/%s, want target/<root>", targetRootCtx.PipelineName, targetRootCtx.StepID)
	}

	targetInstanceID := state.PipelineInstanceIDFromAccumulated(childCtx.AccumulatedJSON)
	if targetInstanceID == "" {
		t.Fatal("target pipeline instance id is empty")
	}
	if targetInstanceID == sourceInstanceID {
		t.Fatalf("target pipeline instance id = %q, want a new destination instance id", targetInstanceID)
	}
	if got := state.PipelineInstanceIDFromAccumulated(targetRootCtx.AccumulatedJSON); got != targetInstanceID {
		t.Fatalf("target root pipeline instance id = %q, want %q", got, targetInstanceID)
	}
	if got := state.RouteDepthFromAccumulated(targetRootCtx.AccumulatedJSON); got != 0 {
		t.Fatalf("target root route depth = %d, want 0", got)
	}
	if got := state.RouteDepthFromAccumulated(childCtx.AccumulatedJSON); got != 1 {
		t.Fatalf("child route depth = %d, want 1", got)
	}
}

func TestDispatcher_ExecuteJob_RootExplicitBaggageAndSkippedSuccessorClaimsDoNotFail(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(pluginsDir): %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	registry := plugin.NewRegistry()
	hub := events.NewHub(128)

	starterScript := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"start","events":[{"type":"chain.start","payload":{"repo":"demo"}}]}'
`
	seedScript := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"seeded","events":[{"type":"chain.progress","payload":{"run":false}}]}'
`
	skipScript := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"should-not-run"}'
`
	commitScript := `#!/bin/bash
cat >/dev/null
echo '{"status":"ok","result":"should-also-not-run"}'
`

	starter := createTestPlugin(t, pluginsDir, "starter", starterScript)
	seeder := createTestPlugin(t, pluginsDir, "seeder", seedScript)
	skipper := createTestPlugin(t, pluginsDir, "skipper", skipScript)
	committer := createTestPlugin(t, pluginsDir, "committer", commitScript)
	for _, plug := range []*plugin.Plugin{starter, seeder, skipper, committer} {
		if err := registry.Add(plug); err != nil {
			t.Fatalf("registry.Add(%s): %v", plug.Name, err)
		}
	}

	pipelineYAML := `pipelines:
  - name: chain
    on: chain.start
    steps:
      - id: seed
        uses: seeder
        baggage:
          repo.name: payload.repo
      - id: maybe
        uses: skipper
      - id: commit
        uses: committer
        baggage:
          commit.changed: payload.changed
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	set, err := dsl.LoadAndCompileFiles([]string{pipelinePath})
	if err != nil {
		t.Fatalf("LoadAndCompileFiles: %v", err)
	}
	pipeline := set.Pipelines["chain"]
	if pipeline == nil {
		t.Fatal("compiled pipeline chain not found")
		return
	}
	maybeNode, ok := pipeline.Nodes["maybe"]
	if !ok {
		t.Fatal("compiled node maybe not found")
	}
	maybeNode.Condition = &conditions.Condition{
		Path:  "payload.run",
		Op:    conditions.OpEq,
		Value: true,
	}
	pipeline.Nodes["maybe"] = maybeNode
	commitNode, ok := pipeline.Nodes["commit"]
	if !ok {
		t.Fatal("compiled node commit not found")
	}
	commitNode.Condition = &conditions.Condition{
		Path:  "payload.changed",
		Op:    conditions.OpEq,
		Value: true,
	}
	pipeline.Nodes["commit"] = commitNode
	routerEngine := router.New(set, nil)

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Plugins["starter"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Poll: 5 * time.Second}}
	cfg.Plugins["seeder"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}
	cfg.Plugins["skipper"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}
	cfg.Plugins["committer"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}

	disp := New(q, st, contextStore, routerEngine, registry, hub, cfg)
	ctx := context.Background()

	rootJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "starter",
		Command:     "poll",
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("Enqueue(root): %v", err)
	}

	rootJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(root): %v", err)
	}
	if rootJob == nil {
		t.Fatal("expected root job")
	}
	disp.executeJob(ctx, rootJob)

	seedJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(seed): %v", err)
	}
	if seedJob == nil {
		t.Fatal("expected seed job")
		return
	}
	if seedJob.EventContextID == nil {
		t.Fatal("seed job event_context_id is nil")
	}

	seedCtx, err := contextStore.Get(ctx, *seedJob.EventContextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(seed): %v", err)
	}
	if got := state.RouteDepthFromAccumulated(seedCtx.AccumulatedJSON); got != 1 {
		t.Fatalf("seed route depth = %d, want 1", got)
	}
	if got := state.PipelineInstanceIDFromAccumulated(seedCtx.AccumulatedJSON); got == "" {
		t.Fatal("seed pipeline instance id is empty")
	}
	var seedAccumulated map[string]any
	if err := json.Unmarshal(seedCtx.AccumulatedJSON, &seedAccumulated); err != nil {
		t.Fatalf("unmarshal seed accumulated context: %v", err)
	}
	repo := seedAccumulated["repo"].(map[string]any)
	if repo["name"] != "demo" {
		t.Fatalf("repo.name = %v, want demo", repo["name"])
	}

	disp.executeJob(ctx, seedJob)

	maybeJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(maybe): %v", err)
	}
	if maybeJob == nil {
		t.Fatal("expected maybe job")
		return
	}
	if maybeJob.EventContextID == nil {
		t.Fatal("maybe job event_context_id is nil")
	}

	maybeCtx, err := contextStore.Get(ctx, *maybeJob.EventContextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(maybe): %v", err)
	}
	if got := state.RouteDepthFromAccumulated(maybeCtx.AccumulatedJSON); got != 2 {
		t.Fatalf("maybe route depth = %d, want 2", got)
	}

	disp.executeJob(ctx, maybeJob)

	maybeResult, err := q.GetJobByID(ctx, maybeJob.ID)
	if err != nil {
		t.Fatalf("GetJobByID(maybe pre-commit): %v", err)
	}
	if maybeResult.Status != queue.StatusSkipped {
		t.Fatalf("maybe pre-commit status = %q, want %q (last_error=%q)", maybeResult.Status, queue.StatusSkipped, deref(maybeResult.LastError))
	}

	commitJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue(commit): %v", err)
	}
	if commitJob == nil {
		t.Fatalf("expected commit job after skipped maybe step")
		return
	}
	if commitJob.EventContextID == nil {
		t.Fatal("commit job event_context_id is nil")
	}

	commitCtx, err := contextStore.Get(ctx, *commitJob.EventContextID)
	if err != nil {
		t.Fatalf("ContextStore.Get(commit): %v", err)
	}
	if got := state.RouteDepthFromAccumulated(commitCtx.AccumulatedJSON); got != 3 {
		t.Fatalf("commit route depth = %d, want 3", got)
	}
	var commitAccumulated map[string]any
	if err := json.Unmarshal(commitCtx.AccumulatedJSON, &commitAccumulated); err != nil {
		t.Fatalf("unmarshal commit accumulated context: %v", err)
	}
	if _, exists := commitAccumulated["commit"]; exists {
		t.Fatalf("commit baggage unexpectedly materialized from skipped predecessor: %+v", commitAccumulated["commit"])
	}
	repo = commitAccumulated["repo"].(map[string]any)
	if repo["name"] != "demo" {
		t.Fatalf("inherited repo.name = %v, want demo", repo["name"])
	}

	disp.executeJob(ctx, commitJob)

	rootResult, err := q.GetJobByID(ctx, rootJobID)
	if err != nil {
		t.Fatalf("GetJobByID(root): %v", err)
	}
	if rootResult.Status != queue.StatusSucceeded {
		t.Fatalf("root status = %q, want %q", rootResult.Status, queue.StatusSucceeded)
	}

	seedResult, err := q.GetJobByID(ctx, seedJob.ID)
	if err != nil {
		t.Fatalf("GetJobByID(seed): %v", err)
	}
	if seedResult.Status != queue.StatusSucceeded {
		t.Fatalf("seed status = %q, want %q", seedResult.Status, queue.StatusSucceeded)
	}

	maybeResult, err = q.GetJobByID(ctx, maybeJob.ID)
	if err != nil {
		t.Fatalf("GetJobByID(maybe): %v", err)
	}
	if maybeResult.Status != queue.StatusSkipped {
		t.Fatalf("maybe status = %q, want %q", maybeResult.Status, queue.StatusSkipped)
	}

	commitResult, err := q.GetJobByID(ctx, commitJob.ID)
	if err != nil {
		t.Fatalf("GetJobByID(commit): %v", err)
	}
	if commitResult.Status != queue.StatusSkipped {
		t.Fatalf("commit status = %q, want %q", commitResult.Status, queue.StatusSkipped)
	}

	var failedCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_queue WHERE status = 'failed'`).Scan(&failedCount); err != nil {
		t.Fatalf("count failed jobs: %v", err)
	}
	if failedCount != 0 {
		t.Fatalf("failed job count = %d, want 0", failedCount)
	}
}

func TestDispatcher_ExecuteJob_ConditionalSwitchBypassesFalseStepAndContinues(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("MkdirAll(pluginsDir): %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	registry := plugin.NewRegistry()
	hub := events.NewHub(128)
	scriptA := `#!/bin/bash
read input
echo '{"status":"ok","result":"start","events":[{"type":"chain.start","payload":{"status":"ok"}}]}'
`
	scriptB := `#!/bin/bash
read input
echo '{"status":"ok","result":"should-not-run"}'
`
	scriptC := `#!/bin/bash
read input
echo '{"status":"ok","result":"ran-c"}'
`
	plugA := createTestPlugin(t, pluginsDir, "plugin-a", scriptA)
	plugB := createTestPlugin(t, pluginsDir, "plugin-b", scriptB)
	plugC := createTestPlugin(t, pluginsDir, "plugin-c", scriptC)
	if err := registry.Add(plugA); err != nil {
		t.Fatalf("registry.Add(plugin-a): %v", err)
	}
	if err := registry.Add(plugB); err != nil {
		t.Fatalf("registry.Add(plugin-b): %v", err)
	}
	if err := registry.Add(plugC); err != nil {
		t.Fatalf("registry.Add(plugin-c): %v", err)
	}

	pipelineYAML := `pipelines:
  - name: chain
    on: chain.start
    steps:
      - id: step_b
        uses: plugin-b
        if:
          path: payload.status
          op: contains
          value: error
      - id: step_c
        uses: plugin-c
`
	pipelinePath := filepath.Join(tmpDir, "pipelines.yaml")
	if err := os.WriteFile(pipelinePath, []byte(pipelineYAML), 0644); err != nil {
		t.Fatalf("WriteFile(pipelines.yaml): %v", err)
	}

	routerEngine, err := router.LoadFromConfigFiles([]string{pipelinePath}, registry, nil)
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Plugins["plugin-a"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Poll: 5 * time.Second}}
	cfg.Plugins["plugin-b"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}
	cfg.Plugins["plugin-c"] = config.PluginConf{Enabled: true, Timeouts: &config.TimeoutsConfig{Handle: 5 * time.Second}}

	disp := New(q, st, contextStore, routerEngine, registry, hub, cfg)
	ctx := context.Background()

	rootJobID, err := q.Enqueue(ctx, queue.EnqueueRequest{Plugin: "plugin-a", Command: "poll", SubmittedBy: "test"})
	if err != nil {
		t.Fatalf("Enqueue(root): %v", err)
	}
	rootJob, err := q.Dequeue(ctx)
	if err != nil || rootJob == nil {
		t.Fatalf("Dequeue(root): job=%v err=%v", rootJob, err)
	}
	disp.executeJob(ctx, rootJob)

	switchJob, err := q.Dequeue(ctx)
	if err != nil || switchJob == nil {
		t.Fatalf("Dequeue(step_b switch): job=%v err=%v", switchJob, err)
	}
	if switchJob.Plugin != "core.switch" {
		t.Fatalf("switch job plugin = %q, want %q", switchJob.Plugin, "core.switch")
	}
	disp.executeJob(ctx, switchJob)

	stepCJob, err := q.Dequeue(ctx)
	if err != nil || stepCJob == nil {
		t.Fatalf("Dequeue(step_c): job=%v err=%v", stepCJob, err)
	}
	if stepCJob.Plugin != "plugin-c" {
		t.Fatalf("step_c plugin = %q, want %q", stepCJob.Plugin, "plugin-c")
	}
	disp.executeJob(ctx, stepCJob)

	switchResult, err := q.GetJobByID(ctx, switchJob.ID)
	if err != nil {
		t.Fatalf("GetJobByID(step_b switch): %v", err)
	}
	if switchResult.Status != queue.StatusSucceeded {
		t.Fatalf("switch status = %q, want %q", switchResult.Status, queue.StatusSucceeded)
	}
	if string(switchResult.Result) == "" {
		t.Fatalf("expected switch result payload")
	}
	var queuedStepBCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM job_queue WHERE plugin = ?`, "plugin-b").Scan(&queuedStepBCount); err != nil {
		t.Fatalf("count step_b jobs: %v", err)
	}
	if queuedStepBCount != 0 {
		t.Fatalf("queued step_b job count = %d, want 0", queuedStepBCount)
	}

	stepCResult, err := q.GetJobByID(ctx, stepCJob.ID)
	if err != nil {
		t.Fatalf("GetJobByID(step_c): %v", err)
	}
	if stepCResult.Status != queue.StatusSucceeded {
		t.Fatalf("step_c status = %q, want %q", stepCResult.Status, queue.StatusSucceeded)
	}

	_ = rootJobID
}

func filterEventsByType(eventsIn []events.Event, typ string) []events.Event {
	out := make([]events.Event, 0, len(eventsIn))
	for _, ev := range eventsIn {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func eventPayload(t *testing.T, ev events.Event) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		t.Fatalf("unmarshal event %s payload: %v", ev.Type, err)
	}
	return payload
}

func TestDispatcher_Start_ParallelExecution(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	registry := plugin.NewRegistry()
	hub := events.NewHub(128)

	// Plugin that sleeps 1s then succeeds — long enough to observe concurrency
	script := `#!/bin/bash
read input
sleep 1
echo '{"status":"ok","result":"done"}'
`
	plug := createTestPlugin(t, pluginsDir, "slow", script)
	if err := registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Service.MaxWorkers = 3
	cfg.Plugins["slow"] = config.PluginConf{
		Enabled:     true,
		Parallelism: 3,
		Timeouts:    &config.TimeoutsConfig{Poll: 10 * time.Second},
	}

	disp := New(q, st, contextStore, nil, registry, hub, cfg)
	ctx, cancel := context.WithCancel(context.Background())

	// Enqueue 3 jobs
	for i := 0; i < 3; i++ {
		if _, err := q.Enqueue(ctx, queue.EnqueueRequest{
			Plugin: "slow", Command: "poll", SubmittedBy: "test",
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	// Start dispatcher in background
	dispErr := make(chan error, 1)
	go func() { dispErr <- disp.Start(ctx) }()

	// Wait for all 3 to complete — if parallel, ~1s; if serial, ~3s
	start := time.Now()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for parallel jobs")
		case <-time.After(200 * time.Millisecond):
			var completed int
			if err := db.QueryRow("SELECT COUNT(*) FROM job_queue WHERE status = ?", queue.StatusSucceeded).Scan(&completed); err != nil {
				t.Fatalf("count succeeded jobs: %v", err)
			}
			if completed >= 3 {
				elapsed := time.Since(start)
				cancel()
				<-dispErr
				// If truly parallel: ~1s. If serial: ~3s. Accept up to 2.5s.
				if elapsed > 2500*time.Millisecond {
					t.Fatalf("jobs took %v — likely serial, not parallel", elapsed)
				}
				t.Logf("3 parallel jobs completed in %v", elapsed)
				return
			}
		}
	}
}

func TestDispatcher_Start_PerPluginParallelismCap(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	registry := plugin.NewRegistry()
	hub := events.NewHub(128)

	// Plugin that sleeps 1s
	script := `#!/bin/bash
read input
sleep 1
echo '{"status":"ok","result":"done"}'
`
	plug := createTestPlugin(t, pluginsDir, "capped", script)
	if err := registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}

	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Service.MaxWorkers = 4
	cfg.Plugins["capped"] = config.PluginConf{
		Enabled:     true,
		Parallelism: 1, // Only 1 at a time despite 4 workers
		Timeouts:    &config.TimeoutsConfig{Poll: 10 * time.Second},
	}

	disp := New(q, st, contextStore, nil, registry, hub, cfg)
	ctx, cancel := context.WithCancel(context.Background())

	// Enqueue 2 jobs
	for i := 0; i < 2; i++ {
		if _, err := q.Enqueue(ctx, queue.EnqueueRequest{
			Plugin: "capped", Command: "poll", SubmittedBy: "test",
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	dispErr := make(chan error, 1)
	go func() { dispErr <- disp.Start(ctx) }()

	start := time.Now()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for capped jobs")
		case <-time.After(200 * time.Millisecond):
			var completed int
			if err := db.QueryRow("SELECT COUNT(*) FROM job_queue WHERE status = ?", queue.StatusSucceeded).Scan(&completed); err != nil {
				t.Fatalf("count succeeded jobs: %v", err)
			}
			if completed >= 2 {
				elapsed := time.Since(start)
				cancel()
				<-dispErr
				// With parallelism=1, 2 jobs should take ~2s (serial)
				if elapsed < 1800*time.Millisecond {
					t.Fatalf("jobs took %v — should be serial with parallelism=1", elapsed)
				}
				t.Logf("2 serial-capped jobs completed in %v", elapsed)
				return
			}
		}
	}
}

func TestDispatcher_Start_SerialDefaultBackcompat(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")
	pluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Fatalf("db.Close(): %v", err)
		}
	}()

	q := queue.New(db)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	registry := plugin.NewRegistry()
	hub := events.NewHub(128)

	script := `#!/bin/bash
read input
sleep 1
echo '{"status":"ok","result":"done"}'
`
	plug := createTestPlugin(t, pluginsDir, "serial", script)
	if err := registry.Add(plug); err != nil {
		t.Fatalf("registry.Add(%s): %v", plug.Name, err)
	}

	// This plugin keeps effective parallelism 1, so two jobs should run serially
	// even though the global worker pool default may be larger than one.
	cfg := config.Defaults()
	cfg.PluginRoots = []string{pluginsDir}
	cfg.Plugins["serial"] = config.PluginConf{
		Enabled:  true,
		Timeouts: &config.TimeoutsConfig{Poll: 10 * time.Second},
	}

	disp := New(q, st, contextStore, nil, registry, hub, cfg)
	ctx, cancel := context.WithCancel(context.Background())

	// Enqueue 2 jobs
	for i := 0; i < 2; i++ {
		if _, err := q.Enqueue(ctx, queue.EnqueueRequest{
			Plugin: "serial", Command: "poll", SubmittedBy: "test",
		}); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}

	dispErr := make(chan error, 1)
	go func() { dispErr <- disp.Start(ctx) }()

	start := time.Now()
	deadline := time.After(8 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for serial jobs")
		case <-time.After(200 * time.Millisecond):
			var completed int
			if err := db.QueryRow("SELECT COUNT(*) FROM job_queue WHERE status = ?", queue.StatusSucceeded).Scan(&completed); err != nil {
				t.Fatalf("count succeeded jobs: %v", err)
			}
			if completed >= 2 {
				elapsed := time.Since(start)
				cancel()
				<-dispErr
				// Default serial: 2 jobs at 1s each ≈ 2s minimum
				if elapsed < 1800*time.Millisecond {
					t.Fatalf("jobs took %v — should be serial with default config", elapsed)
				}
				t.Logf("2 default-serial jobs completed in %v", elapsed)
				return
			}
		}
	}
}

func TestDispatcher_PluginParallelism_RespectsConcurrencySafeHint(t *testing.T) {
	disp, _, _, cleanup := setupTestDispatcher(t)
	defer cleanup()

	unsafePlugin := &plugin.Plugin{Name: "unsafe", ConcurrencySafe: false}
	safePlugin := &plugin.Plugin{Name: "safe", ConcurrencySafe: true}
	if err := disp.registry.Add(unsafePlugin); err != nil {
		t.Fatalf("registry.Add(unsafe): %v", err)
	}
	if err := disp.registry.Add(safePlugin); err != nil {
		t.Fatalf("registry.Add(safe): %v", err)
	}

	disp.cfg.Service.MaxWorkers = 6
	disp.cfg.Plugins["unsafe"] = config.PluginConf{Enabled: true, Parallelism: 1}
	disp.cfg.Plugins["safe"] = config.PluginConf{Enabled: true, Parallelism: 6}

	if got := disp.pluginParallelism("unsafe"); got != 1 {
		t.Fatalf("unsafe plugin default should be serial, got %d", got)
	}

	if got := disp.pluginParallelism("safe"); got != 6 {
		t.Fatalf("safe plugin should use configured parallelism, got %d", got)
	}

	// Explicit operator override: allow unsafe plugin >1.
	pc := disp.cfg.Plugins["unsafe"]
	pc.Parallelism = 4
	disp.cfg.Plugins["unsafe"] = pc
	if got := disp.pluginParallelism("unsafe"); got != 4 {
		t.Fatalf("unsafe plugin override should be honored, got %d", got)
	}
}
