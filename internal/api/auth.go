package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/mattjoyce/ductile/internal/auth"
)

// authMiddleware authenticates the bearer token from the Authorization header.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return s.authenticate(false)(next)
}

func (s *Server) authenticate(allowQueryToken bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, err := auth.ExtractBearerToken(r, allowQueryToken)
			if err != nil {
				s.writeError(w, http.StatusUnauthorized, err.Error())
				return
			}

			principal, ok := auth.Authenticate(token, s.config.Tokens)
			if !ok {
				s.writeError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}

// require gates a route on the policy: the authenticated principal must be
// allowed action on capability. A refusal is a 403 whose body carries why the
// policy said no (the rule it walked and the fact it did not find), so an
// operator reads the reason from the response, not from a log.
func (s *Server) require(action, capability string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.PrincipalFromContext(r.Context()); !ok {
				s.writeError(w, http.StatusUnauthorized, "missing Authorization context")
				return
			}
			if d := s.decide(r.Context(), action, capability); !d.Allow {
				s.writeForbidden(w, "insufficient scope", d)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// decide asks the policy whether the request's principal may take action on
// capability. Every error is a deny: a missing principal, a zero policy or an
// engine error all come back Allow=false, and anything but the first is
// logged, since it is the server's fault rather than the caller's.
func (s *Server) decide(ctx context.Context, action, capability string) auth.Decision {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Decision{}
	}
	d, err := s.policy.Decide(ctx, p, action, capability)
	if err != nil {
		s.logger.Error("policy decision failed", "principal", p.Name, "action", action, "capability", capability, "error", err)
		return auth.Decision{}
	}
	return d
}

// writeForbidden is a 403 with the policy's reason attached when it has one.
// The reason is marshalled first: if it cannot be encoded the 403 still goes
// out with its message rather than an empty body, since respondJSON cannot
// recover once the status line is written.
func (s *Server) writeForbidden(w http.ResponseWriter, message string, d auth.Decision) {
	resp := ErrorResponse{Error: message}
	if d.Why.Relation != "" {
		if why, err := json.Marshal(d.Why); err == nil {
			resp.Why = json.RawMessage(why)
		} else {
			s.logger.Error("policy refusal could not be encoded", "error", err)
		}
	}
	respondJSON(w, http.StatusForbidden, resp)
}
