package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/auth"
)

// TestRequire_RefusalCarriesWhy: a 403 from the policy names the rule it
// walked and the fact it did not find, so the caller reads the reason from
// the response rather than from a log.
func TestRequire_RefusalCarriesWhy(t *testing.T) {
	t.Parallel()
	tokens := []auth.TokenConfig{{Name: "status-reader", Token: "t-status", Scopes: []string{"jobs:status:ro"}}}
	s := New(Config{Tokens: tokens}, nil, nil, nil, nil, nil, nil, nil, nil, slog.Default())

	h := s.require(auth.ActionWrite, auth.CapSystemReload)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/system/reload", nil)
	req.Header.Set("Authorization", "Bearer t-status")
	rec := httptest.NewRecorder()
	s.authMiddleware(h).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body)
	}
	var body struct {
		Error string          `json:"error"`
		Why   json.RawMessage `json:"why"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body)
	}
	if body.Error != "insufficient scope" {
		t.Errorf("error = %q", body.Error)
	}
	why := string(body.Why)
	for _, want := range []string{`"allow"`, "status-reader", "system/reload/"} {
		if !strings.Contains(why, want) {
			t.Errorf("why must mention %s: %s", want, why)
		}
	}
	if strings.Contains(why, "t-status") {
		t.Errorf("why must never carry the bearer token: %s", why)
	}
}

// TestRequire_AllowPassesThrough: the lattice reaches through the policy as
// the old scope map did (plugin:rw reaches a catalog read).
func TestRequire_AllowPassesThrough(t *testing.T) {
	t.Parallel()
	tokens := []auth.TokenConfig{{Token: "t-rw", Scopes: []string{"plugin:rw"}}}
	s := New(Config{Tokens: tokens}, nil, nil, nil, nil, nil, nil, nil, nil, slog.Default())

	h := s.require(auth.ActionRead, auth.CapPluginCatalog)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/plugin/x", nil)
	req.Header.Set("Authorization", "Bearer t-rw")
	rec := httptest.NewRecorder()
	s.authMiddleware(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", rec.Code, rec.Body)
	}
}

// TestNew_UnknownScopeDeniesEverything: a Server built with a token the
// policy refuses keeps the zero policy, which fails closed. The runtime never
// reaches this (it builds the policy first and fails the boot).
func TestNew_UnknownScopeDeniesEverything(t *testing.T) {
	t.Parallel()
	tokens := []auth.TokenConfig{{Token: "t-bad", Scopes: []string{"*", "jobs:rww"}}}
	s := New(Config{Tokens: tokens}, nil, nil, nil, nil, nil, nil, nil, nil, slog.Default())

	h := s.require(auth.ActionRead, auth.CapJobsStatus)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	req.Header.Set("Authorization", "Bearer t-bad")
	rec := httptest.NewRecorder()
	s.authMiddleware(h).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 from the zero policy; body %s", rec.Code, rec.Body)
	}
}
