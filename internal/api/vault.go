package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/vault"
)

// VaultManager is the narrow management surface the API needs from the vault
// owner: authenticate the resident admin token, and apply guarded mutations.
// Satisfied by *vault.Vault. The API never reads secret values back out — value
// exfiltration (dump) stays a local, key-touching operation, never over HTTP.
type VaultManager interface {
	AuthenticateAdmin(presented string) bool
	RegisterPrincipal(name, kind string) error
	SetSecret(name, value string, authorizedPrincipals []string, pattern string, now time.Time) error
	Roll(name, operatorValue string, now time.Time) error
	Revoke(name string, now time.Time) error
	RevokePrincipal(name string) error
	PurgePrincipal(name string) error
	RollPrincipal(name string, now time.Time) (rolled, skipped []string, err error)
}

// VaultAuditor records vault lifecycle facts to the append-only audit log. The
// narrow surface (one append) is defined here, at the point of use; satisfied
// by *state.Store. nil disables audit (the op still succeeds — audit is
// observability, never a precondition).
type VaultAuditor interface {
	AppendVaultAudit(ctx context.Context, ev state.VaultAuditEvent) error
}

// vaultAdminActor is the actor recorded for management-API mutations: every
// authenticated /vault/* write is by the holder of the resident admin token.
const vaultAdminActor = "core-admin-token"

// auditVault records one vault lifecycle fact, best-effort. A failed audit
// write never rolls back the op (the blob is already saved and the response is
// about to be 200) — it is logged loudly so a lost row is visible, not silent.
func (s *Server) auditVault(ctx context.Context, ev state.VaultAuditEvent) {
	if s.auditor == nil {
		return
	}
	if err := s.auditor.AppendVaultAudit(ctx, ev); err != nil {
		s.logger.Error("vault audit write failed", "op", ev.Op, "outcome", ev.Outcome, "error", err)
	}
}

// authenticateVaultAdmin gates vault management routes on the vault's resident
// admin token (minted by `vault init`), NOT the config API tokens. This keeps
// the vault's write-gate credential inside the vault — rotatable without
// re-keying and never sitting in config.
func (s *Server) authenticateVaultAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.vault == nil {
			s.writeError(w, http.StatusServiceUnavailable, "vault not available")
			return
		}
		token, err := auth.ExtractBearerToken(r, false)
		if err != nil {
			s.writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if !s.vault.AuthenticateAdmin(token) {
			s.writeError(w, http.StatusUnauthorized, "invalid vault admin token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// vaultSetRequest is the POST /vault/secret body.
type vaultSetRequest struct {
	Name                 string   `json:"name"`
	Value                string   `json:"value"`
	AuthorizedPrincipals []string `json:"authorized_principals,omitempty"`
	Pattern              string   `json:"pattern,omitempty"`
}

// vaultSetResponse is the value-free success body.
type vaultSetResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// handleVaultSet upserts a secret over the authenticated management API. The
// daemon is the sole writer: it mutates its own resident model and persists the
// blob (no reload). The value is never echoed back. Pattern defaults to manual.
func (s *Server) handleVaultSet(w http.ResponseWriter, r *http.Request) {
	var req vaultSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	pattern := req.Pattern
	if pattern == "" {
		pattern = vault.PatternManual
	}
	if err := s.vault.SetSecret(req.Name, req.Value, req.AuthorizedPrincipals, pattern, time.Now()); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "set", SecretName: req.Name, Actor: vaultAdminActor, Outcome: "ok", Detail: "pattern=" + pattern,
	})
	respondJSON(w, http.StatusOK, vaultSetResponse{Name: req.Name, Status: "set"})
}

// vaultRegisterPrincipalRequest is the POST /vault/principal body.
type vaultRegisterPrincipalRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // plugin | consumer | gateway
}

func (s *Server) handleVaultRegisterPrincipal(w http.ResponseWriter, r *http.Request) {
	var req vaultRegisterPrincipalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.vault.RegisterPrincipal(req.Name, req.Kind); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "register", Principal: req.Name, Actor: vaultAdminActor, Outcome: "ok", Detail: "kind=" + req.Kind,
	})
	respondJSON(w, http.StatusOK, vaultStatusResponse{Name: req.Name, Status: "principal_registered"})
}

// vaultRollRequest is the POST /vault/secret/roll body. Value is used only for
// manual-pattern secrets; auto-pattern secrets are minted by the daemon.
type vaultRollRequest struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// vaultNameRequest is the body for ops keyed only by a name (revoke, purge).
type vaultNameRequest struct {
	Name string `json:"name"`
}

// vaultStatusResponse is the value-free success body for lifecycle mutations.
type vaultStatusResponse struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (s *Server) handleVaultRoll(w http.ResponseWriter, r *http.Request) {
	var req vaultRollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.vault.Roll(req.Name, req.Value, time.Now()); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "roll", SecretName: req.Name, Actor: vaultAdminActor, Outcome: "ok",
	})
	respondJSON(w, http.StatusOK, vaultStatusResponse{Name: req.Name, Status: "rolled"})
}

func (s *Server) handleVaultRevoke(w http.ResponseWriter, r *http.Request) {
	var req vaultNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.vault.Revoke(req.Name, time.Now()); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "revoke", SecretName: req.Name, Actor: vaultAdminActor, Outcome: "ok",
	})
	respondJSON(w, http.StatusOK, vaultStatusResponse{Name: req.Name, Status: "revoked"})
}

func (s *Server) handleVaultRevokePrincipal(w http.ResponseWriter, r *http.Request) {
	var req vaultNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.vault.RevokePrincipal(req.Name); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "revoke_principal", Principal: req.Name, Actor: vaultAdminActor, Outcome: "ok",
	})
	respondJSON(w, http.StatusOK, vaultStatusResponse{Name: req.Name, Status: "principal_revoked"})
}

func (s *Server) handleVaultPurgePrincipal(w http.ResponseWriter, r *http.Request) {
	var req vaultNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.vault.PurgePrincipal(req.Name); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "purge_principal", Principal: req.Name, Actor: vaultAdminActor, Outcome: "ok",
	})
	respondJSON(w, http.StatusOK, vaultStatusResponse{Name: req.Name, Status: "principal_purged"})
}

// vaultRollPrincipalResponse reports which of the principal's secrets were
// rolled (auto) and which were skipped (manual — they need an operator value).
type vaultRollPrincipalResponse struct {
	Name    string   `json:"name"`
	Rolled  []string `json:"rolled"`
	Skipped []string `json:"skipped"`
}

func (s *Server) handleVaultRollPrincipal(w http.ResponseWriter, r *http.Request) {
	var req vaultNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rolled, skipped, err := s.vault.RollPrincipal(req.Name, time.Now())
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.auditVault(r.Context(), state.VaultAuditEvent{
		Op: "roll_principal", Principal: req.Name, Actor: vaultAdminActor, Outcome: "ok",
		Detail: fmt.Sprintf("rolled=%d skipped=%d", len(rolled), len(skipped)),
	})
	respondJSON(w, http.StatusOK, vaultRollPrincipalResponse{Name: req.Name, Rolled: rolled, Skipped: skipped})
}
