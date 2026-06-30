package webhook

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/storage"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestHandleWebhook_ValidSignature(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"event":"push"}`)
	signature := formatGitHubSignature(computeExpectedSignature(body, secret))

	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{
			{
				Path:            "/webhook/github",
				Plugin:          "github-handler",
				Command:         "handle",
				Secret:          secret,
				SignatureHeader: "X-Hub-Signature-256",
				MaxBodySize:     1048576,
			},
		},
	}

	db := setupTestDB(t)
	q := queue.New(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, q, logger)

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()

	server.handleWebhook(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	var resp TriggerResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("expected non-empty job id")
	}

	job, err := q.GetJobByID(context.Background(), resp.JobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if job.Plugin != "github-handler" {
		t.Fatalf("Plugin = %v, want github-handler", job.Plugin)
	}
	if job.Command != "handle" {
		t.Fatalf("Command = %v, want handle", job.Command)
	}

	var payload []byte
	if err := db.QueryRow("SELECT payload FROM job_queue WHERE id = ?", resp.JobID).Scan(&payload); err != nil {
		t.Fatalf("query payload: %v", err)
	}
	if string(payload) != string(body) {
		t.Fatalf("Payload = %v, want %v", string(payload), string(body))
	}
}

// TestHandleWebhook_SurvivesRequestCancellation reproduces #152: a verified
// webhook must still enqueue even when the HTTP request context is already
// cancelled (a client hang-up or keep-alive connection-reuse race). Before the
// fix the handler passed the request context straight into the enqueue
// transaction, so BeginTx/Commit on a cancelled context rolled back and the
// caller got a 500 ("transaction has already been committed or rolled back").
func TestHandleWebhook_SurvivesRequestCancellation(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"event":"push"}`)
	signature := formatGitHubSignature(computeExpectedSignature(body, secret))

	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{
			{
				Path:            "/webhook/github",
				Plugin:          "github-handler",
				Command:         "handle",
				Secret:          secret,
				SignatureHeader: "X-Hub-Signature-256",
				MaxBodySize:     1048576,
			},
		},
	}

	db := setupTestDB(t)
	q := queue.New(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, q, logger)

	// Request whose context is already cancelled — the client connection is gone.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()

	server.handleWebhook(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (enqueue must survive a cancelled request ctx, #152)", rec.Code, http.StatusAccepted)
	}
	var resp TriggerResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("expected non-empty job id")
	}
	if _, err := q.GetJobByID(context.Background(), resp.JobID); err != nil {
		t.Fatalf("job should be durably enqueued despite a cancelled request ctx: %v", err)
	}
}

func TestHandleWebhook_InvalidSignature(t *testing.T) {
	secret := "test-secret"
	body := []byte(`{"event":"push"}`)
	wrongSignature := "sha256=0000000000000000000000000000000000000000000000000000000000000000"

	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{{
			Path:            "/webhook/github",
			Plugin:          "github-handler",
			Command:         "handle",
			Secret:          secret,
			SignatureHeader: "X-Hub-Signature-256",
		}},
	}

	db := setupTestDB(t)
	q := queue.New(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, q, logger)

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", wrongSignature)
	rec := httptest.NewRecorder()

	server.handleWebhook(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	var resp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.Error != "forbidden" {
		t.Fatalf("Error = %v, want generic 'forbidden'", resp.Error)
	}

	depth, err := q.Depth(context.Background())
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("expected no enqueued jobs, got depth %d", depth)
	}
}

func TestHandleWebhook_MissingSignature(t *testing.T) {
	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{{
			Path:            "/webhook/github",
			Plugin:          "github-handler",
			Secret:          "secret",
			SignatureHeader: "X-Hub-Signature-256",
		}},
	}

	db := setupTestDB(t)
	q := queue.New(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, q, logger)

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	server.handleWebhook(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	depth, err := q.Depth(context.Background())
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("expected no enqueued jobs, got depth %d", depth)
	}
}

func TestHandleWebhook_BodyTooLarge(t *testing.T) {
	secret := "test-secret"
	body := bytes.Repeat([]byte("a"), 2*1024*1024)
	signature := formatGitHubSignature(computeExpectedSignature(body, secret))

	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{{
			Path:            "/webhook/github",
			Plugin:          "github-handler",
			Secret:          secret,
			SignatureHeader: "X-Hub-Signature-256",
			MaxBodySize:     1048576,
		}},
	}

	db := setupTestDB(t)
	q := queue.New(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, q, logger)

	req := httptest.NewRequest("POST", "/webhook/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()
	server.handleWebhook(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	depth, err := q.Depth(context.Background())
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 0 {
		t.Fatalf("expected no enqueued jobs, got depth %d", depth)
	}
}

func TestHandleWebhook_UnknownPath(t *testing.T) {
	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{{
			Path:   "/webhook/github",
			Plugin: "github-handler",
			Secret: "secret",
		}},
	}

	db := setupTestDB(t)
	q := queue.New(db)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, q, logger)

	req := httptest.NewRequest("POST", "/webhook/unknown", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	server.handleWebhook(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestNew_AppliesDefaults(t *testing.T) {
	config := Config{
		Listen: "127.0.0.1:0",
		Endpoints: []EndpointConfig{{
			Path:   "/webhook/test",
			Plugin: "test-plugin",
			Secret: "secret",
		}},
	}

	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	server := New(config, queue.New(db), logger)

	ep := server.endpoints["/webhook/test"]
	if ep.MaxBodySize != DefaultMaxBodySize {
		t.Fatalf("MaxBodySize = %d, want %d", ep.MaxBodySize, DefaultMaxBodySize)
	}
	if ep.Command != DefaultCommand {
		t.Fatalf("Command = %v, want %v", ep.Command, DefaultCommand)
	}
}
