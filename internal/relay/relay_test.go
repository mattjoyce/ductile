package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func setupRelayDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func setupRelayRouter(t *testing.T, pipelineYAML string) router.Engine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pipelines.yaml")
	if err := os.WriteFile(path, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	engine, err := router.LoadFromConfigFiles([]string{path}, nil, slog.Default())
	if err != nil {
		t.Fatalf("LoadFromConfigFiles: %v", err)
	}
	return engine
}

func setupRelayServer(t *testing.T, receiver *Receiver) *httptest.Server {
	t.Helper()
	mux := chi.NewRouter()
	mux.Post(receiver.RoutePattern(), receiver.HandleHTTP)
	return httptest.NewServer(mux)
}

func TestReceiverRejectsInvalidSignature(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1"},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}

	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	server := setupRelayServer(t, receiver)
	defer server.Close()

	body := []byte(`{"event":{"type":"backup.ready","payload":{"path":"/srv/latest.tar"}},"origin":{"instance":"home-primary"}}`)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/ingest/peer/home-primary", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderPeer, "home-primary")
	req.Header.Set(HeaderKeyID, "v1")
	req.Header.Set(HeaderTimestamp, time.Now().UTC().Format(time.RFC3339Nano))
	req.Header.Set(HeaderSignature, "sha256=deadbeef")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if depth, err := q.Depth(context.Background()); err != nil || depth != 0 {
		t.Fatalf("queue depth = %d, err = %v, want 0,nil", depth, err)
	}
}

func TestReceiverAcceptsRelayAndEnqueuesRootDispatch(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
        baggage:
          trace_id: context.trace_id
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			MaxBodySize:      "1MB",
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

	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	server := setupRelayServer(t, receiver)
	defer server.Close()

	envelope := Envelope{
		Event: EnvelopeEvent{
			Type:      "backup.ready",
			Payload:   map[string]any{"path": "/srv/latest.tar"},
			DedupeKey: "backup.ready:2026-05-03",
		},
		Origin:  EnvelopeOrigin{Instance: "home-primary", JobID: "job-123", EventID: "evt-456"},
		Baggage: map[string]any{"trace_id": "tr-789", "ignored": "nope"},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	signature, err := sign(http.MethodPost, "/ingest/peer/home-primary", timestamp, body, "shared-secret")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, server.URL+"/ingest/peer/home-primary", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderPeer, "home-primary")
	req.Header.Set(HeaderKeyID, "v1")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, signature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusAccepted)
	}

	var accepted AcceptanceResponse
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if accepted.JobID == "" {
		t.Fatal("expected accepted job id")
	}

	job, err := q.GetJobByID(context.Background(), accepted.JobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if job.Plugin != "echo" {
		t.Fatalf("job.Plugin = %q, want echo", job.Plugin)
	}

	var eventContextID string
	if err := db.QueryRow(`SELECT event_context_id FROM job_queue WHERE id = ?`, accepted.JobID).Scan(&eventContextID); err != nil {
		t.Fatalf("query event_context_id: %v", err)
	}
	if strings.TrimSpace(eventContextID) == "" {
		t.Fatal("expected relay-enqueued job to have event_context_id")
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
	if _, ok := scope["ignored"]; ok {
		t.Fatalf("unexpected admitted baggage key: ignored")
	}
}

func TestSenderToReceiverEndToEnd(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	receiverCfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1", Accept: []string{"backup.ready"}},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}

	receiver, err := NewReceiver(receiverCfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	server := setupRelayServer(t, receiver)
	defer server.Close()

	senderCfg := &config.Config{
		Service: config.ServiceConfig{Name: "home-primary"},
		RelayInstances: []config.RelayInstanceConfig{
			{
				Name:        "lab",
				Enabled:     true,
				BaseURL:     server.URL,
				IngressPath: "/ingest/peer/home-primary",
				SecretRef:   "relay-lab-v1",
				KeyID:       "v1",
				Timeout:     5 * time.Second,
				Allow:       []string{"backup.ready"},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}

	sender, err := NewSender(senderCfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	accepted, err := sender.Send(context.Background(), "lab", Envelope{
		Event: EnvelopeEvent{
			Type:      "backup.ready",
			Payload:   map[string]any{"path": "/srv/backups/latest.tar.zst"},
			DedupeKey: "backup.ready:2026-05-03",
		},
		Origin: EnvelopeOrigin{
			Plugin:  "backup-runner",
			JobID:   "job-123",
			EventID: "evt-456",
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
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
	if err := db.QueryRow(`SELECT payload FROM job_queue WHERE id = ?`, accepted.JobID).Scan(&payload); err != nil {
		t.Fatalf("query payload: %v", err)
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
}

func TestReceiverRejectsExpiredTimestamp(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 1 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1", Accept: []string{"backup.ready"}},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	server := setupRelayServer(t, receiver)
	defer server.Close()

	body := []byte(`{"event":{"type":"backup.ready","payload":{"path":"/srv/latest.tar"}},"origin":{"instance":"home-primary"}}`)
	timestamp := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	signature, err := sign(http.MethodPost, "/ingest/peer/home-primary", timestamp, body, "shared-secret")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/ingest/peer/home-primary", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderPeer, "home-primary")
	req.Header.Set(HeaderKeyID, "v1")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, signature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestReceiverRejectsDisallowedEventType(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1", Accept: []string{"backup.ready"}},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	server := setupRelayServer(t, receiver)
	defer server.Close()

	body := []byte(`{"event":{"type":"report.generated","payload":{"path":"/srv/report.txt"}},"origin":{"instance":"home-primary"}}`)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	signature, err := sign(http.MethodPost, "/ingest/peer/home-primary", timestamp, body, "shared-secret")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/ingest/peer/home-primary", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderPeer, "home-primary")
	req.Header.Set(HeaderKeyID, "v1")
	req.Header.Set(HeaderTimestamp, timestamp)
	req.Header.Set(HeaderSignature, signature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
}

func TestReceiverDeduplicatesRepeatedRelayByEventDedupeKey(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1", Accept: []string{"backup.ready"}},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	server := setupRelayServer(t, receiver)
	defer server.Close()

	senderCfg := &config.Config{
		Service: config.ServiceConfig{Name: "home-primary"},
		RelayInstances: []config.RelayInstanceConfig{
			{
				Name:        "lab",
				Enabled:     true,
				BaseURL:     server.URL,
				IngressPath: "/ingest/peer/home-primary",
				SecretRef:   "relay-lab-v1",
				KeyID:       "v1",
				Timeout:     5 * time.Second,
				Allow:       []string{"backup.ready"},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	sender, err := NewSender(senderCfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	envelope := Envelope{
		Event: EnvelopeEvent{
			Type:      "backup.ready",
			Payload:   map[string]any{"path": "/srv/backups/latest.tar.zst"},
			DedupeKey: "backup.ready:2026-05-03",
		},
		Origin: EnvelopeOrigin{Plugin: "backup-runner", JobID: "job-123", EventID: "evt-456"},
	}

	first, err := sender.Send(context.Background(), "lab", envelope)
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if _, err := db.Exec(`UPDATE job_queue SET status = ?, completed_at = ? WHERE id = ?`, queue.StatusSucceeded, time.Now().UTC().Format(time.RFC3339Nano), first.JobID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	second, err := sender.Send(context.Background(), "lab", envelope)
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if second.JobID != first.JobID {
		t.Fatalf("second accepted job id = %q, want %q", second.JobID, first.JobID)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_queue`).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("job count = %d, want 1", count)
	}
}

// TestReceiverDerivedDedupeKeyPreventsReplay is the F-002 regression: a relay
// envelope that carries no explicit DedupeKey but does carry origin.event_id
// must not enqueue a second job when the same envelope is resubmitted. The
// receiver derives a stable dedupe key from peer + origin identity.
func TestReceiverDerivedDedupeKeyPreventsReplay(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1", Accept: []string{"backup.ready"}},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	server := setupRelayServer(t, receiver)
	defer server.Close()

	senderCfg := &config.Config{
		Service: config.ServiceConfig{Name: "home-primary"},
		RelayInstances: []config.RelayInstanceConfig{
			{
				Name:        "lab",
				Enabled:     true,
				BaseURL:     server.URL,
				IngressPath: "/ingest/peer/home-primary",
				SecretRef:   "relay-lab-v1",
				KeyID:       "v1",
				Timeout:     5 * time.Second,
				Allow:       []string{"backup.ready"},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	sender, err := NewSender(senderCfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	// No explicit DedupeKey; the receiver must derive one from origin identity.
	envelope := Envelope{
		Event: EnvelopeEvent{
			Type:    "backup.ready",
			Payload: map[string]any{"path": "/srv/backups/latest.tar.zst"},
		},
		Origin: EnvelopeOrigin{Plugin: "backup-runner", JobID: "job-123", EventID: "evt-replay-001"},
	}

	first, err := sender.Send(context.Background(), "lab", envelope)
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := q.Dequeue(context.Background()); err != nil {
		t.Fatalf("Dequeue: %v", err)
	}
	if _, err := db.Exec(`UPDATE job_queue SET status = ?, completed_at = ? WHERE id = ?`, queue.StatusSucceeded, time.Now().UTC().Format(time.RFC3339Nano), first.JobID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	second, err := sender.Send(context.Background(), "lab", envelope)
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if second.JobID != first.JobID {
		t.Fatalf("replay created new job: second.JobID = %q, want %q (first)", second.JobID, first.JobID)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_queue`).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 1 {
		t.Fatalf("job count = %d, want 1 — derived dedupe key did not prevent replay", count)
	}
}

// TestReceiverDifferentOriginEventIDsEnqueueDistinct verifies that replay
// protection does not conflate envelopes that carry distinct origin.event_ids.
// Each distinct origin identity must produce a separate local job.
func TestReceiverDifferentOriginEventIDsEnqueueDistinct(t *testing.T) {
	db := setupRelayDB(t)
	q := queue.New(db)
	contexts := state.NewContextStore(db)
	engine := setupRelayRouter(t, `
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: echo
`)

	cfg := &config.Config{
		Service: config.ServiceConfig{Name: "lab"},
		RemoteIngress: &config.RemoteIngressConfig{
			ListenPath:       "/ingest/peer",
			AllowedClockSkew: 5 * time.Minute,
			RequireKeyID:     true,
			TrustedPeers: []config.RelayPeerConfig{
				{Name: "home-primary", Enabled: true, SecretRef: "relay-lab-v1", KeyID: "v1", Accept: []string{"backup.ready"}},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	receiver, err := NewReceiver(cfg, q, engine, contexts, state.NewAdmitter(q, state.DefaultMaxContextBytes), slog.Default())
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	server := setupRelayServer(t, receiver)
	defer server.Close()

	senderCfg := &config.Config{
		Service: config.ServiceConfig{Name: "home-primary"},
		RelayInstances: []config.RelayInstanceConfig{
			{
				Name:        "lab",
				Enabled:     true,
				BaseURL:     server.URL,
				IngressPath: "/ingest/peer/home-primary",
				SecretRef:   "relay-lab-v1",
				KeyID:       "v1",
				Timeout:     5 * time.Second,
				Allow:       []string{"backup.ready"},
			},
		},
		ResolvedSecrets: map[string]string{"relay-lab-v1": "shared-secret"},
	}
	sender, err := NewSender(senderCfg)
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}

	first, err := sender.Send(context.Background(), "lab", Envelope{
		Event:  EnvelopeEvent{Type: "backup.ready", Payload: map[string]any{"path": "/backups/a"}},
		Origin: EnvelopeOrigin{EventID: "evt-distinct-001"},
	})
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}

	second, err := sender.Send(context.Background(), "lab", Envelope{
		Event:  EnvelopeEvent{Type: "backup.ready", Payload: map[string]any{"path": "/backups/b"}},
		Origin: EnvelopeOrigin{EventID: "evt-distinct-002"},
	})
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}

	if first.JobID == second.JobID {
		t.Fatalf("distinct origin event IDs produced the same job ID %q", first.JobID)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM job_queue`).Scan(&count); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if count != 2 {
		t.Fatalf("job count = %d, want 2 — distinct origin events must create distinct jobs", count)
	}
}
