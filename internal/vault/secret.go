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

// SetSecret registers a new secret or partially updates an existing active one
// (upsert). Pure model mutation; the caller persists via Save. `now` is injected
// so the model stays clock-free and tests are deterministic.
//
// Semantics (#23):
//   - Value is ROLL-ONLY. On update, a `value` that differs from the current is
//     refused (ErrValueImmutable) — roll is the sole, RollCount-audited value
//     path, so set cannot be a side door. An empty value on update means "leave
//     unchanged"; an equal value is a no-op.
//   - Grants are a PARTIAL update: authorizedPrincipals nil = leave the existing
//     grants; non-nil (including empty) = replace them (empty clears). This stops
//     a value/metadata edit from silently wiping every grant.
//   - pattern "" on update = leave; on create, "" defaults to manual.
//   - An active MANUAL secret must not be created with an empty value
//     (ErrEmptyValue); auto-pattern secrets are exempt — they are minted by roll.
//
// Fail-closed preconditions: a kebab/identifier name, a known pattern (when
// given), and every named principal must already be registered (an orphan grant
// is refused at write). Updating a *revoked* secret is refused — revocation is
// terminal; use a fresh name.
func (s *Store) SetSecret(name, value string, authorizedPrincipals []string, pattern string, now time.Time) error {
	if !secretNameRE.MatchString(name) {
		return fmt.Errorf("%w: secret %q", ErrInvalidName, name)
	}
	if isReservedSecret(name) {
		return fmt.Errorf("%w: secret %q (use RotateAdminToken)", ErrReservedEntity, name)
	}
	if pattern != "" && !validPattern(pattern) {
		return fmt.Errorf("%w: %q", ErrInvalidPattern, pattern)
	}
	for _, p := range authorizedPrincipals {
		if _, ok := s.Principals[p]; !ok {
			return fmt.Errorf("%w: secret %q authorizes %q", ErrUnknownPrincipal, name, p)
		}
	}
	ts := now.UTC().Format(time.RFC3339)

	if existing, ok := s.Secrets[name]; ok {
		if existing.Status == StatusRevoked {
			return fmt.Errorf("%w: %q", ErrSecretRevoked, name)
		}
		// Value is roll-only: a differing value is refused; "" or equal leaves it.
		if value != "" && value != existing.Value {
			return fmt.Errorf("%w: secret %q", ErrValueImmutable, name)
		}
		// Grants: nil leaves existing; non-nil (incl. empty) replaces.
		if authorizedPrincipals != nil {
			existing.AuthorizedPrincipals = append([]string(nil), authorizedPrincipals...)
		}
		// Pattern: "" leaves existing.
		if pattern != "" {
			existing.Pattern = pattern
		}
		existing.UpdatedAt = ts
		return nil
	}

	// Create.
	if pattern == "" {
		pattern = PatternManual
	}
	if value == "" && pattern != PatternAuto {
		return fmt.Errorf("%w: secret %q", ErrEmptyValue, name)
	}
	s.Secrets[name] = &Secret{
		Value:                value,
		AuthorizedPrincipals: append([]string(nil), authorizedPrincipals...),
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
//
// WIRE CONTRACT (#27, Ousterhout §2.5): the json field names below are a STABLE,
// machine-readable contract for any operator/AI consuming Check output — treat
// them as frozen. `kind` is a closed enum ("secret" | "principal"); new fields
// may be ADDED (additive, optional) but existing names/meanings must not change
// or be removed without a versioning decision. Same rule applies to any future
// `dump --values` output schema.
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
