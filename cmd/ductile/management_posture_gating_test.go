package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/storage"
)

// #136: "ductile-closed" must close the TRIGGER planes too. In the management
// posture the scheduler and dispatcher stay down — a queued job is not picked
// up (and therefore no pipeline fires and no vault secret is composed) until
// the gateway activates. Decided 2026-06-10: gate, not document-as-open.
func TestManagementPostureGatesTriggerPlanes(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	genesisVaultForTest(t, tmp)

	sockDir, err := os.MkdirTemp("/tmp", "dtl")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "v.sock")
	publicAddr := freeLocalAddr(t)
	statePath := filepath.Join(tmp, "state.db")

	cfgYAML := `
plugin_roots:
  - plugins
service:
  allow_symlinks: true
  unconfined: true
state:
  path: ` + statePath + `
api:
  enabled: true
  listen: ` + publicAddr + `
  management_socket: ` + socket + `
`
	configPath := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, owner, err := config.LoadWithVault(configPath)
	if err != nil {
		t.Fatalf("LoadWithVault: %v", err)
	}
	rt, err := buildRuntime(loaded, configPath, "test", nil, make(chan error, 4), runtimeBuildOptions{vaultOwner: owner})
	if err != nil {
		t.Fatalf("buildRuntime (management posture): %v", err)
	}
	defer rt.Stop()

	// Drop a job straight into the queue: with the dispatcher gated it must
	// STAY queued. (Ungated, the dispatcher picks it up within a tick and the
	// status moves — even a failing ghost plugin leaves 'queued'.)
	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, statePath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q := queue.New(db)
	jobID, err := q.Enqueue(ctx, queue.EnqueueRequest{
		Plugin:      "ghost",
		Command:     "handle",
		Payload:     []byte(`{}`),
		MaxAttempts: 1,
		SubmittedBy: "test",
	})
	if err != nil {
		t.Fatalf("enqueue probe job: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
	res, err := q.GetJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("get probe job: %v", err)
	}
	if res.Status != queue.StatusQueued {
		t.Fatalf("management posture must not dispatch: probe job status = %q, want queued", res.Status)
	}
}
