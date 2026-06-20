package api

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/mattjoyce/ductile/internal/events"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddlewarePreflight(t *testing.T) {
	handler := corsMiddleware([]string{"https://example.test"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("preflight should not call next handler")
	}))

	req := httptest.NewRequest(http.MethodOptions, "/plugin/echo/poll", nil)
	req.Header.Set("Origin", "https://example.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusNoContent)
	}
	assertHeader(t, resp, "Access-Control-Allow-Origin", "https://example.test")
	assertHeader(t, resp, "Access-Control-Allow-Credentials", "true")
	assertHeader(t, resp, "Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	assertHeader(t, resp, "Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-CSRF-Token")
	assertHeader(t, resp, "Access-Control-Max-Age", "300")
}

func TestCORSMiddlewareActualRequest(t *testing.T) {
	called := false
	handler := corsMiddleware([]string{"https://example.test"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Link", "</jobs>; rel=next")
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/pipeline/default", nil)
	req.Header.Set("Origin", "https://example.test")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if !called {
		t.Fatal("actual request did not call next handler")
	}
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", resp.Code, http.StatusAccepted)
	}
	assertHeader(t, resp, "Access-Control-Allow-Origin", "https://example.test")
	assertHeader(t, resp, "Access-Control-Allow-Credentials", "true")
	assertHeader(t, resp, "Access-Control-Expose-Headers", "Link")
}

func TestCORSMiddlewareNoOrigin(t *testing.T) {
	handler := corsMiddleware([]string{"https://example.test"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}

// TestCORSMiddlewareDisallowedOrigin verifies that an origin not in the allowed
// list receives no credentialed CORS headers — the disallowed-origin scenario
// from the F-003 security finding.
func TestCORSMiddlewareDisallowedOrigin(t *testing.T) {
	called := false
	handler := corsMiddleware([]string{"https://allowed.example"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Origin", "https://attacker.example")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if !called {
		t.Fatal("disallowed origin should still pass through to next handler")
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want empty for disallowed origin", got)
	}
}

// TestCORSMiddlewareEmptyAllowList verifies that no origin receives credentialed
// headers when AllowedOrigins is empty — the safe production default.
func TestCORSMiddlewareEmptyAllowList(t *testing.T) {
	called := false
	handler := corsMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Origin", "https://any.example")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if !called {
		t.Fatal("empty allow list should pass through to next handler")
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty when allow list is empty", got)
	}
}

func assertHeader(t *testing.T, resp *httptest.ResponseRecorder, key, want string) {
	t.Helper()
	if got := resp.Header().Get(key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

// #140: a gateway bind failure must be synchronous — the activation reload
// calls Bind inside buildRuntime, so a taken port fails the reload (restore
// path) instead of surfacing on errCh after the reload answered "ok".
func TestBindFailsFastOnOccupiedPort(t *testing.T) {
	occupier, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer func() { _ = occupier.Close() }()

	srv := New(Config{Listen: occupier.Addr().String()}, nil, &mockRegistry{}, &mockRouter{}, &mockWaiter{}, nil, nil, nil, events.NewHub(10), slog.Default())
	if err := srv.Bind(); err == nil {
		t.Fatal("Bind must fail synchronously on an occupied port")
	}
	// serveDone must be closed so WaitServeStopped does not burn its deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := srv.WaitServeStopped(ctx); err != nil {
		t.Fatalf("WaitServeStopped after failed Bind must return immediately, got %v", err)
	}
}

// TestCORSMiddlewareWildcard verifies that when the wildcard "*" is in the
// AllowedOrigins list, any origin is accepted and Access-Control-Allow-Credentials
// is NOT sent, preventing credential leakage to untrusted origins.
func TestCORSMiddlewareWildcard(t *testing.T) {
	called := false
	handler := corsMiddleware([]string{"*"}) (http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Origin", "https://attacker.example")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if !called {
		t.Fatal("wildcard should pass through to next handler")
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("Access-Control-Allow-Credentials should not be set for wildcard")
	}
}
