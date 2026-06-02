package vault

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

// secretNameRE allows lowercase identifiers with `_`/`-` (e.g. withings_api),
// starting and ending alphanumeric.
var secretNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9_-]*[a-z0-9])?$`)

func validPattern(pattern string) bool {
	return pattern == PatternAuto || pattern == PatternManual
}

// SetSecret registers a new secret or updates an existing active one (upsert).
// Pure model mutation; the caller persists via Save. `now` is injected so the
// model stays clock-free and tests are deterministic.
//
// Fail-closed preconditions: a kebab/identifier name, a known pattern, and every
// authorized principal must already be registered (an orphan grant is refused at
// write, not just flagged later by Check). Updating a *revoked* secret is
// refused — revocation is terminal; use a fresh name.
func (s *Store) SetSecret(name, value string, authorizedPrincipals []string, pattern string, now time.Time) error {
	if !secretNameRE.MatchString(name) {
		return fmt.Errorf("%w: secret %q", ErrInvalidName, name)
	}
	if isReservedSecret(name) {
		return fmt.Errorf("%w: secret %q (use RotateAdminToken)", ErrReservedEntity, name)
	}
	if !validPattern(pattern) {
		return fmt.Errorf("%w: %q", ErrInvalidPattern, pattern)
	}
	for _, p := range authorizedPrincipals {
		if _, ok := s.Principals[p]; !ok {
			return fmt.Errorf("%w: secret %q authorizes %q", ErrUnknownPrincipal, name, p)
		}
	}
	ts := now.UTC().Format(time.RFC3339)
	// Defensive copy so later caller mutation of the slice can't reach the model.
	authz := append([]string(nil), authorizedPrincipals...)

	if existing, ok := s.Secrets[name]; ok {
		if existing.Status == StatusRevoked {
			return fmt.Errorf("%w: %q", ErrSecretRevoked, name)
		}
		existing.Value = value
		existing.AuthorizedPrincipals = authz
		existing.Pattern = pattern
		existing.UpdatedAt = ts
		return nil
	}

	s.Secrets[name] = &Secret{
		Value:                value,
		AuthorizedPrincipals: authz,
		Status:               StatusActive,
		Pattern:              pattern,
		CreatedAt:            ts,
		UpdatedAt:            ts,
	}
	return nil
}

// Secret returns a secret by name.
func (s *Store) Secret(name string) (*Secret, bool) {
	sec, ok := s.Secrets[name]
	return sec, ok
}

// SecretNames returns the secret names, sorted.
func (s *Store) SecretNames() []string {
	names := make([]string, 0, len(s.Secrets))
	for n := range s.Secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Issue is one integrity problem found by Check. Structured so a caller (or an
// AI operator) can act on it, not just print it.
type Issue struct {
	Kind   string `json:"kind"`   // "secret" | "principal"
	Target string `json:"target"` // the offending name
	Detail string `json:"detail"`
}

// Check validates the in-memory model: enum sanity and orphaned grants (a secret
// authorizing a principal that is not registered). An empty result means healthy.
// Pure and deterministic (issues sorted) — no decryption, no I/O.
func (s *Store) Check() []Issue {
	var issues []Issue

	for name, sec := range s.Secrets {
		if !validStatus(sec.Status) {
			issues = append(issues, Issue{"secret", name, "invalid status " + quote(sec.Status)})
		}
		if !validPattern(sec.Pattern) {
			issues = append(issues, Issue{"secret", name, "invalid pattern " + quote(sec.Pattern)})
		}
		for _, p := range sec.AuthorizedPrincipals {
			if _, ok := s.Principals[p]; !ok {
				issues = append(issues, Issue{"secret", name, "authorizes unregistered principal " + quote(p)})
			}
		}
	}
	for name, p := range s.Principals {
		if !validKind(p.Kind) {
			issues = append(issues, Issue{"principal", name, "invalid kind " + quote(p.Kind)})
		}
		if !validStatus(p.Status) {
			issues = append(issues, Issue{"principal", name, "invalid status " + quote(p.Status)})
		}
	}

	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Kind != issues[j].Kind {
			return issues[i].Kind < issues[j].Kind
		}
		if issues[i].Target != issues[j].Target {
			return issues[i].Target < issues[j].Target
		}
		return issues[i].Detail < issues[j].Detail
	})
	return issues
}

func quote(s string) string { return fmt.Sprintf("%q", s) }
