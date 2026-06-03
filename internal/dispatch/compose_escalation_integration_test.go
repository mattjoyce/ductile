package dispatch

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/vault"
)

// runComposeFailureJob registers + configures a plugin, wires the given composer +
// verifier, runs one poll job through executeJob, and returns the db for assertions.
func runComposeFailureJob(t *testing.T, composer SecretComposer, verifier PluginVerifier) (*Dispatcher, *sql.DB) {
	t.Helper()
	disp, db, pluginsDir, cleanup := setupTestDispatcher(t)
	t.Cleanup(cleanup)

	plug := createTestPlugin(t, pluginsDir, "mailer", "#!/bin/bash\necho '{\"status\":\"ok\"}'\n")
	if err := disp.registry.Add(plug); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}
	disp.cfg.Plugins["mailer"] = config.PluginConf{
		Enabled:  true,
		Timeouts: &config.TimeoutsConfig{Poll: 5 * time.Second},
	}
	disp.secretComposer = composer
	disp.pluginVerifier = verifier

	ctx := context.Background()
	if _, err := disp.queue.Enqueue(ctx, queue.EnqueueRequest{Plugin: "mailer", Command: "poll", SubmittedBy: "test"}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job, err := disp.queue.Dequeue(ctx)
	if err != nil || job == nil {
		t.Fatalf("Dequeue: job=%v err=%v", job, err)
	}
	disp.executeJob(ctx, job)
	return disp, db
}

func hubHasEvent(disp *Dispatcher, eventType string) bool {
	for _, ev := range disp.events.SnapshotSince(0) {
		if ev.Type == eventType {
			return true
		}
	}
	return false
}

func latestAudit(t *testing.T, disp *Dispatcher) (op, outcome string) {
	t.Helper()
	rows, err := disp.state.ListVaultAudit(context.Background(), "", 20)
	if err != nil {
		t.Fatalf("ListVaultAudit: %v", err)
	}
	for _, r := range rows {
		if r.Principal == "mailer" {
			return r.Op, r.Outcome
		}
	}
	t.Fatalf("no vault_audit row for mailer; rows=%+v", rows)
	return "", ""
}

// #25: a fingerprint mismatch at spawn must escalate on all three channels — a
// distinct audit fact, a live hub event — and still fail the job closed.
func TestExecuteJobFingerprintMismatchEscalates(t *testing.T) {
	composer := &fakeComposer{comp: vault.Composition{Secrets: map[string]string{"API_KEY": "v1"}}}
	verifier := &fakeVerifier{err: errors.New("entrypoint hash mismatch at /p/mailer")}

	disp, db := runComposeFailureJob(t, composer, verifier)

	// (job failed closed)
	var status string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE plugin = 'mailer'").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("mismatch must fail the job closed, got status=%q", status)
	}

	// (distinct audit fact)
	op, outcome := latestAudit(t, disp)
	if op != "fingerprint_mismatch" || outcome != "security_alert" {
		t.Fatalf("audit must be distinct, got op=%q outcome=%q", op, outcome)
	}

	// (live hub event)
	if !hubHasEvent(disp, eventPluginFingerprintMismatch) {
		t.Fatal("a plugin.fingerprint_mismatch hub event must be published")
	}
}

// A benign denial (revoked principal) must stay on the quiet path: compose_denial
// audit, NO security hub event — distinct from the swap case (ISC-A1/A2).
func TestExecuteJobBenignComposeDenialIsQuiet(t *testing.T) {
	composer := &fakeComposer{err: vault.ErrPrincipalInactive}

	disp, db := runComposeFailureJob(t, composer, &fakeVerifier{err: errors.New("must not be reached")})

	var status string
	if err := db.QueryRow("SELECT status FROM job_queue WHERE plugin = 'mailer'").Scan(&status); err != nil {
		t.Fatalf("query status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("revoked principal must fail closed, got status=%q", status)
	}

	op, outcome := latestAudit(t, disp)
	if op != "compose_denial" || outcome != "denied" {
		t.Fatalf("benign denial must stay quiet, got op=%q outcome=%q", op, outcome)
	}
	if hubHasEvent(disp, eventPluginFingerprintMismatch) {
		t.Fatal("a benign denial must NOT publish the security hub event")
	}
}
