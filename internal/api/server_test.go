package api

import (
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

// TestCORSMiddlewareWildcard verifies that wildcard allows all and disables credentials.
func TestCORSMiddlewareWildcard(t *testing.T) {
	called := false
	handler := corsMiddleware([]string{"*"})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Origin", "https://any.example")
	resp := httptest.NewRecorder()

	handler.ServeHTTP(resp, req)

	if !called {
		t.Fatal("handler was not called")
	}
	if got := resp.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := resp.Header().Get("Access-Control-Allow-Credentials"); got != "false" {
		t.Fatalf("Access-Control-Allow-Credentials = %q, want false", got)
	}
}
