package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// TokenConfig is a bearer token with a set of scopes.
type TokenConfig struct {
	Token  string
	Scopes []string
}

type Principal struct {
	Token  string
	Scopes map[string]struct{}
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// ExtractBearerToken reads the bearer token from the Authorization header.
// When allowQueryToken is true it also falls back to the ?token= query
// parameter — this must only be set for endpoints where browser APIs cannot
// send custom headers (e.g. EventSource/SSE). All other callers must pass
// false so URL tokens are not accepted and cannot leak through logs or history.
func ExtractBearerToken(r *http.Request, allowQueryToken bool) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			return "", errors.New("invalid Authorization header format")
		}
		token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
		if token != "" {
			return token, nil
		}
	}

	if allowQueryToken {
		if qToken := r.URL.Query().Get("token"); qToken != "" {
			return qToken, nil
		}
	}

	return "", errors.New("missing or invalid Authorization header")
}

func constantTimeEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Authenticate matches a presented bearer token against configured tokens.
func Authenticate(presented string, tokens []TokenConfig) (Principal, bool) {
	for _, t := range tokens {
		if constantTimeEqual(presented, t.Token) {
			return Principal{
				Token:  presented,
				Scopes: normalizeScopes(t.Scopes),
			}, true
		}
	}
	return Principal{}, false
}

func normalizeScopes(scopes []string) map[string]struct{} {
	out := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out[s] = struct{}{}
	}

	// Write implies read for well-known resources.
	if _, ok := out["plugin:rw"]; ok {
		out["plugin:ro"] = struct{}{}
		// plugin:rw is a strict superset of both narrower plugin:*:ro scopes
		// introduced for P2-07 — catalog reads AND read-class invocation.
		out["plugin:catalog:ro"] = struct{}{}
		out["plugin:invoke:ro"] = struct{}{}
	}
	// Legacy plugin:ro is split into catalog (read manifests / list commands)
	// vs invoke (actually call read-class commands). P2-07: a plain plugin:ro
	// token grants catalog access only — operators who relied on plugin:ro
	// for read-class invocation must add plugin:invoke:ro explicitly. This is
	// a deliberate breaking change so least-privilege is the default.
	if _, ok := out["plugin:ro"]; ok {
		out["plugin:catalog:ro"] = struct{}{}
	}
	if _, ok := out["jobs:rw"]; ok {
		out["jobs:ro"] = struct{}{}
	}
	if _, ok := out["events:rw"]; ok {
		out["events:ro"] = struct{}{}
	}
	return out
}

func HasAnyScope(p Principal, required ...string) bool {
	if len(required) == 0 {
		return true
	}
	if _, ok := p.Scopes["*"]; ok {
		return true
	}
	for _, s := range required {
		if _, ok := p.Scopes[s]; ok {
			return true
		}
	}
	return false
}
