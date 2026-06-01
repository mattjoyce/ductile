package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/vault"
)

// VaultManager is the narrow management surface the API needs from the vault
// owner: authenticate the resident admin token, and apply guarded mutations.
// Satisfied by *vault.Vault. The API never reads secret values back out — value
// exfiltration (dump) stays a local, key-touching operation, never over HTTP.
type VaultManager interface {
	AuthenticateAdmin(presented string) bool
	SetSecret(name, value string, authorizedPrincipals []string, pattern string, now time.Time) error
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
	respondJSON(w, http.StatusOK, vaultSetResponse{Name: req.Name, Status: "set"})
}
