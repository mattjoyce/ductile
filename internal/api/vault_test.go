package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

type fakeVaultSetCall struct {
	name       string
	value      string
	principals []string
	pattern    string
}

type fakeVault struct {
	adminToken       string
	setErr           error
	calls            []fakeVaultSetCall
	registered       []string
	rolled           []string
	revoked          []string
	rollPrincRolled  []string
	rollPrincSkipped []string
}

func (f *fakeVault) AuthenticateAdmin(presented string) bool {
	return presented != "" && presented == f.adminToken
}

func (f *fakeVault) RegisterPrincipal(name, _ string) error {
	f.registered = append(f.registered, name)
	return nil
}

func (f *fakeVault) SetSecret(name, value string, principals []string, pattern string, _ time.Time) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.calls = append(f.calls, fakeVaultSetCall{name, value, principals, pattern})
	return nil
}

func (f *fakeVault) Roll(name, _ string, _ time.Time) error {
	f.rolled = append(f.rolled, name)
	return nil
}

func (f *fakeVault) Revoke(name string, _ time.Time) error {
	f.revoked = append(f.revoked, name)
	return nil
}

func (f *fakeVault) RevokePrincipal(string) error { return nil }
func (f *fakeVault) PurgePrincipal(string) error  { return nil }

func (f *fakeVault) RollPrincipal(string, time.Time) (rolled, skipped []string, err error) {
	return f.rollPrincRolled, f.rollPrincSkipped, nil
}

func setupVaultTestServer(t *testing.T, fv VaultManager) *Server {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := Config{
		Listen: "localhost:8080",
		Tokens: []auth.TokenConfig{{Token: "cfg-token", Scopes: []string{"*"}}},
		Vault:  fv,
	}
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	return New(cfg, q, &mockRegistry{}, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), nil, hub, slog.Default())
}

func vaultSetRequestJSON(t *testing.T, body any, token string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/vault/secret", bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestVaultSet_ValidAdminToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	body := map[string]any{"name": "api_key", "value": "shh", "authorized_principals": []string{"mailer"}}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, "admin-tok"))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if len(fv.calls) != 1 {
		t.Fatalf("expected exactly one SetSecret call, got %d", len(fv.calls))
	}
	got := fv.calls[0]
	if got.name != "api_key" || got.value != "shh" || got.pattern != "manual" {
		t.Fatalf("unexpected SetSecret args: %+v", got)
	}
	// The response must never echo the secret value back.
	if strings.Contains(rr.Body.String(), "shh") {
		t.Fatalf("response leaked the secret value: %s", rr.Body.String())
	}
}

func TestVaultSet_WrongAdminToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	body := map[string]any{"name": "api_key", "value": "shh"}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, "wrong"))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong admin token, got %d", rr.Code)
	}
	if len(fv.calls) != 0 {
		t.Fatalf("a rejected request must not mutate the vault")
	}
}

func TestVaultSet_NoToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	body := map[string]any{"name": "api_key", "value": "shh"}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, ""))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", rr.Code)
	}
}

// A config API token (even with wildcard scope) must NOT authorize vault
// mutations — the vault gate is the vault's own admin token, not config tokens.
func TestVaultSet_ConfigTokenRejected(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	body := map[string]any{"name": "api_key", "value": "shh"}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, "cfg-token"))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("config token must not authorize vault writes, got %d", rr.Code)
	}
}

func TestVaultRegisterPrincipal_Authorized(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	req := httptest.NewRequest(http.MethodPost, "/vault/principal", strings.NewReader(`{"name":"worker","kind":"plugin"}`))
	req.Header.Set("Authorization", "Bearer admin-tok")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if len(fv.registered) != 1 || fv.registered[0] != "worker" {
		t.Fatalf("expected RegisterPrincipal(worker), got %v", fv.registered)
	}
}

func TestVaultRegisterPrincipal_RejectsConfigToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	req := httptest.NewRequest(http.MethodPost, "/vault/principal", strings.NewReader(`{"name":"worker","kind":"plugin"}`))
	req.Header.Set("Authorization", "Bearer cfg-token")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("config token must not authorize register, got %d", rr.Code)
	}
	if len(fv.registered) != 0 {
		t.Fatal("rejected request must not register")
	}
}

func TestVaultRoll_Authorized(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	req := httptest.NewRequest(http.MethodPost, "/vault/secret/roll", strings.NewReader(`{"name":"hmac"}`))
	req.Header.Set("Authorization", "Bearer admin-tok")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	if len(fv.rolled) != 1 || fv.rolled[0] != "hmac" {
		t.Fatalf("expected Roll(hmac), got %v", fv.rolled)
	}
}

func TestVaultRoll_RejectsConfigToken(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server := setupVaultTestServer(t, fv)

	req := httptest.NewRequest(http.MethodPost, "/vault/secret/roll", strings.NewReader(`{"name":"hmac"}`))
	req.Header.Set("Authorization", "Bearer cfg-token")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("config token must not authorize roll, got %d", rr.Code)
	}
	if len(fv.rolled) != 0 {
		t.Fatal("rejected request must not roll")
	}
}

func TestVaultRollPrincipal_ReportsRolledAndSkipped(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok", rollPrincRolled: []string{"a", "b"}, rollPrincSkipped: []string{"m"}}
	server := setupVaultTestServer(t, fv)

	req := httptest.NewRequest(http.MethodPost, "/vault/principal/roll", strings.NewReader(`{"name":"mailer"}`))
	req.Header.Set("Authorization", "Bearer admin-tok")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp vaultRollPrincipalResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(strings.Join(resp.Rolled, ","), "a") || len(resp.Skipped) != 1 || resp.Skipped[0] != "m" {
		t.Fatalf("unexpected roll-principal response: %+v", resp)
	}
}

// setupAuditedVaultServer wires a real state.Store as the audit sink so we can
// assert that handler success emits an audit fact.
func setupAuditedVaultServer(t *testing.T, fv VaultManager) (*Server, *state.Store) {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := state.NewStore(db)
	cfg := Config{
		Listen:       "localhost:8080",
		Tokens:       []auth.TokenConfig{{Token: "cfg-token", Scopes: []string{"*"}}},
		Vault:        fv,
		VaultAuditor: st,
	}
	q := queue.New(db)
	cs := state.NewContextStore(db)
	hub := events.NewHub(10)
	srv := New(cfg, q, &mockRegistry{}, &mockRouter{}, &mockWaiter{}, cs, state.NewAdmitter(q, state.DefaultMaxContextBytes), nil, hub, slog.Default())
	return srv, st
}

// A successful management mutation appends exactly one audit fact carrying the
// op, secret name, and actor — and never the secret value.
func TestVaultSet_EmitsAuditFactWithoutValue(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server, st := setupAuditedVaultServer(t, fv)

	const secretValue = "shh-do-not-log"
	body := map[string]any{"name": "api_key", "value": secretValue, "authorized_principals": []string{"mailer"}}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, "admin-tok"))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rr.Code, rr.Body.String())
	}

	rows, err := st.ListVaultAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListVaultAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one audit fact, got %d", len(rows))
	}
	got := rows[0]
	if got.Op != "set" || got.SecretName != "api_key" || got.Actor != vaultAdminActor || got.Outcome != "ok" {
		t.Fatalf("unexpected audit fact: %+v", got)
	}
	for _, f := range []string{got.Op, got.Principal, got.SecretName, got.Actor, got.Outcome, got.Detail} {
		if strings.Contains(f, secretValue) {
			t.Fatalf("secret value leaked into audit fact: %q", f)
		}
	}
}

// A rejected request (wrong token) mutates nothing and emits no audit fact.
func TestVaultSet_RejectedRequestEmitsNoAudit(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok"}
	server, st := setupAuditedVaultServer(t, fv)

	body := map[string]any{"name": "api_key", "value": "v"}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, "wrong"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	rows, err := st.ListVaultAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListVaultAudit: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rejected request must emit no audit fact, got %d", len(rows))
	}
}

func TestVaultSet_SetSecretErrorIsBadRequest(t *testing.T) {
	fv := &fakeVault{adminToken: "admin-tok", setErr: context.DeadlineExceeded}
	server := setupVaultTestServer(t, fv)

	body := map[string]any{"name": "bad", "value": "v"}
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, vaultSetRequestJSON(t, body, "admin-tok"))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on SetSecret error, got %d", rr.Code)
	}
}
