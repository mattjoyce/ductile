package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// TokenConfig is a bearer token with a set of scopes. Name is the principal the
// token acts as in policy decisions and in the "why" of a refusal; the runtime
// sets it to the token's secret_ref so a secret value never names a principal.
// An empty Name falls back to the token's position in the list.
type TokenConfig struct {
	Name   string
	Token  string
	Scopes []string
}

// Principal is an authenticated caller: the name the policy knows it by. It
// carries no scopes; what a principal may do is the Policy's to decide.
type Principal struct {
	Name string
}

type principalKey struct{}

// WithPrincipal stores the authenticated principal on the context.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext reads the authenticated principal from the context.
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

// Authenticate matches a presented bearer token against configured tokens and
// returns the principal it acts as. The same tokens, in the same order, must
// build the Policy, since the principal name is positional when unnamed.
func Authenticate(presented string, tokens []TokenConfig) (Principal, bool) {
	for i, t := range tokens {
		if constantTimeEqual(presented, t.Token) {
			return Principal{Name: principalName(i, t)}, true
		}
	}
	return Principal{}, false
}

// principalName is the one rule for naming a token's principal, shared by
// Authenticate and NewPolicy so the two cannot disagree.
func principalName(i int, t TokenConfig) string {
	if name := strings.TrimSpace(t.Name); name != "" {
		return name
	}
	return fmt.Sprintf("tokens[%d]", i)
}
