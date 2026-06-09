package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

// startManagementServer builds a Server wired to fv, serving the management-only
// posture on a temp unix socket, and returns an http.Client that dials it plus a
// cancel func. It blocks until the socket answers so callers race nothing.
func startManagementServer(t *testing.T, fv VaultManager) (*http.Client, func()) {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Unix socket paths are capped at ~104 bytes (sun_path); the standard temp
	// dir under /var/folders blows past that on macOS, so use a short base.
	sockDir, err := os.MkdirTemp("/tmp", "dtl")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "v.sock")
	cfg := Config{ManagementSocket: socket, Vault: fv, BootPosture: "management-only"}
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	srv := New(cfg, q, &mockRegistry{}, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), nil, hub, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.StartManagement(ctx); err != nil && err != context.Canceled {
			t.Errorf("StartManagement: %v", err)
		}
	}()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}

	// Wait for the listener to answer rather than sleeping a fixed interval.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, err := client.Get("http://unix/healthz")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("management socket never became ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cleanup := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("management server did not shut down")
		}
	}
	return client, cleanup
}

func mgmtPost(t *testing.T, client *http.Client, path, token string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://unix"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// TestManagementPostureServesVaultWithAdminToken is the heart of #129: a daemon in
// the management posture lets the admin token operate /vault/* over the local
// socket, and the api token can be minted through it (here, set as a secret).
func TestManagementPostureServesVaultWithAdminToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-secret"}
	client, cleanup := startManagementServer(t, fv)
	defer cleanup()

	resp := mgmtPost(t, client, "/vault/secret", "admin-secret", map[string]any{
		"name":                  "core-api-token",
		"value":                 "minted-api-token",
		"authorized_principals": []string{},
		"pattern":               "manual",
	})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set with admin token: status = %d, want 200", resp.StatusCode)
	}
	if len(fv.calls) != 1 || fv.calls[0].name != "core-api-token" {
		t.Fatalf("expected one SetSecret(core-api-token), got %+v", fv.calls)
	}
}

// TestManagementHealthzReportsPosture: /healthz on the management socket carries
// the live posture, so a probe can tell pre-activation from a fully-serving
// gateway (#130 anti-strand).
func TestManagementHealthzReportsPosture(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-secret"}
	client, cleanup := startManagementServer(t, fv)
	defer cleanup()

	resp, err := client.Get("http://unix/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body HealthzResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if body.Posture != "management-only" {
		t.Fatalf("healthz posture = %q, want management-only", body.Posture)
	}
}

// TestManagementPostureRejectsWithoutAdminToken: the surface is authenticated —
// never an unauthenticated local surface (ADR invariant 2).
func TestManagementPostureRejectsWithoutAdminToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-secret"}
	client, cleanup := startManagementServer(t, fv)
	defer cleanup()

	resp := mgmtPost(t, client, "/vault/secret", "", map[string]any{"name": "x", "value": "y"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("set without token: status = %d, want 401", resp.StatusCode)
	}
	if len(fv.calls) != 0 {
		t.Fatalf("unauthenticated request reached the vault: %+v", fv.calls)
	}
}

// TestGatewayHealthzReportsPosture: the gateway posture reports itself in /healthz
// via the same handler, so the live signal is symmetric across both surfaces.
func TestGatewayHealthzReportsPosture(t *testing.T) {
	db, err := storage.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	srv := New(Config{BootPosture: "gateway"}, q, &mockRegistry{}, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), nil, hub, slog.Default())

	rec := httptest.NewRecorder()
	srv.handleHealthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
	var body HealthzResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if body.Posture != "gateway" {
		t.Fatalf("healthz posture = %q, want gateway", body.Posture)
	}
}

// TestManagementPostureDoesNotServeGatewayPlane proves "ductile closed": the
// gateway routes are ABSENT from the management router, not merely unauthenticated.
func TestManagementPostureDoesNotServeGatewayPlane(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-secret"}
	client, cleanup := startManagementServer(t, fv)
	defer cleanup()

	for _, path := range []string{"/topology", "/plugins", "/jobs"} {
		resp, err := client.Get("http://unix" + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("gateway route %s reachable in management posture: status = %d, want 404", path, resp.StatusCode)
		}
	}
}
