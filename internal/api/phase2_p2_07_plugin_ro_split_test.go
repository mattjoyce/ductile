package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
)

// setupTestServerWithTokens wires a Server with caller-supplied tokens so
// individual P2-07 cases can exercise scope-by-scope behavior. The plugin
// registry contains a fake plugin with a single read-class command and a
// single write-class command so both scope branches are reachable.
func setupTestServerWithTokens(t *testing.T, tokens []auth.TokenConfig) *Server {
	t.Helper()

	db := setupTestDB(t)
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)

	readClass := plugin.Command{
		Name:        "ping",
		Type:        plugin.CommandTypeRead,
		Description: "Read-class command for P2-07 invocation tests.",
	}
	writeClass := plugin.Command{
		Name:        "mutate",
		Type:        plugin.CommandTypeWrite,
		Description: "Write-class command for P2-07 invocation tests.",
	}

	reg := &mockRegistry{
		plugins: map[string]*plugin.Plugin{
			"phase2p207": {
				Name:        "phase2p207",
				Version:     "0.0.1",
				Description: "Fixture plugin for P2-07 scope-split tests.",
				Commands:    []plugin.Command{readClass, writeClass},
			},
		},
	}

	return New(
		Config{
			Listen: "localhost:0",
			Tokens: tokens,
		},
		Deps{
			Queue:        q,
			Registry:     reg,
			Router:       &mockRouter{},
			Waiter:       &mockWaiter{},
			ContextStore: cs,
			Admitter:     state.NewAdmitter(q, state.DefaultMaxContextBytes),
			Hub:          hub,
			Logger:       slog.Default(),
		},
	)
}

// requestPlugin issues a POST /plugin/phase2p207/{command} with the given
// token. Returns the recorded response.
func requestPlugin(t *testing.T, srv *Server, token, command string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"payload": map[string]any{}})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/plugin/phase2p207/"+command, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)
	return rr
}

// requestGetPlugin issues GET /plugin/phase2p207 with the given token.
func requestGetPlugin(t *testing.T, srv *Server, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/plugin/phase2p207", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.setupRoutes().ServeHTTP(rr, req)
	return rr
}

// TestPhase2P2_07_PluginRoCannotInvokeReadClassCommand characterizes the
// P2-07 fix: a plain `plugin:ro` token can NO LONGER invoke read-class
// plugin commands. Before the split, plugin:ro was accepted as sufficient
// to enqueue stress.cpu and similar resource-expensive read-class jobs.
func TestPhase2P2_07_PluginRoCannotInvokeReadClassCommand(t *testing.T) {
	srv := setupTestServerWithTokens(t, []auth.TokenConfig{
		{Token: "ro-only", Scopes: []string{"plugin:ro"}},
	})

	rr := requestPlugin(t, srv, "ro-only", "ping")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for plugin:ro invoking read-class command, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPhase2P2_07_PluginInvokeRoCanInvokeReadClassCommand confirms the
// fix path: an operator who explicitly grants `plugin:invoke:ro` regains
// the ability to invoke read-class commands.
func TestPhase2P2_07_PluginInvokeRoCanInvokeReadClassCommand(t *testing.T) {
	srv := setupTestServerWithTokens(t, []auth.TokenConfig{
		{Token: "invoke-ro", Scopes: []string{"plugin:invoke:ro"}},
	})

	rr := requestPlugin(t, srv, "invoke-ro", "ping")

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for plugin:invoke:ro invoking read-class command, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPhase2P2_07_PluginInvokeRoCannotInvokeWriteCommand confirms the
// invoke:ro scope does NOT bypass the write-class gate at handlers.go —
// write commands still require plugin:rw.
func TestPhase2P2_07_PluginInvokeRoCannotInvokeWriteCommand(t *testing.T) {
	srv := setupTestServerWithTokens(t, []auth.TokenConfig{
		{Token: "invoke-ro", Scopes: []string{"plugin:invoke:ro"}},
	})

	rr := requestPlugin(t, srv, "invoke-ro", "mutate")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for plugin:invoke:ro invoking write-class command, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPhase2P2_07_PluginCatalogRoCanReadCatalog confirms catalog reads are
// gated by `plugin:catalog:ro` (and implied by `plugin:ro`).
func TestPhase2P2_07_PluginCatalogRoCanReadCatalog(t *testing.T) {
	srv := setupTestServerWithTokens(t, []auth.TokenConfig{
		{Token: "catalog-ro", Scopes: []string{"plugin:catalog:ro"}},
	})

	rr := requestGetPlugin(t, srv, "catalog-ro")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for plugin:catalog:ro reading catalog, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPhase2P2_07_PluginCatalogRoCannotInvoke confirms the inverse — a
// catalog-only token cannot reach the invocation handler.
func TestPhase2P2_07_PluginCatalogRoCannotInvoke(t *testing.T) {
	srv := setupTestServerWithTokens(t, []auth.TokenConfig{
		{Token: "catalog-ro", Scopes: []string{"plugin:catalog:ro"}},
	})

	rr := requestPlugin(t, srv, "catalog-ro", "ping")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for plugin:catalog:ro invoking, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestPhase2P2_07_PluginRwInvokesEverything confirms the super-scope still
// works — operators with plugin:rw retain full invocation power for both
// read-class and write-class commands.
func TestPhase2P2_07_PluginRwInvokesEverything(t *testing.T) {
	srv := setupTestServerWithTokens(t, []auth.TokenConfig{
		{Token: "rw", Scopes: []string{"plugin:rw"}},
	})

	if rr := requestPlugin(t, srv, "rw", "ping"); rr.Code != http.StatusAccepted {
		t.Fatalf("plugin:rw read-class: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := requestPlugin(t, srv, "rw", "mutate"); rr.Code != http.StatusAccepted {
		t.Fatalf("plugin:rw write-class: expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := requestGetPlugin(t, srv, "rw"); rr.Code != http.StatusOK {
		t.Fatalf("plugin:rw catalog: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

