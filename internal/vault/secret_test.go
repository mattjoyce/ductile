package vault

import (
	"errors"
	"testing"
	"time"
)

var testTime = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
var laterTime = time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)

func storeWithPrincipals(names ...string) *Store {
	s := NewStore()
	for _, n := range names {
		_ = s.RegisterPrincipal(n, KindPlugin)
	}
	return s
}

func TestSetSecretCreate(t *testing.T) {
	s := storeWithPrincipals("withings")
	if err := s.SetSecret("withings_api", "v1", []string{"withings"}, PatternManual, testTime); err != nil {
		t.Fatalf("set: %v", err)
	}
	sec, ok := s.Secret("withings_api")
	if !ok {
		t.Fatal("secret missing after set")
	}
	if sec.Value != "v1" || sec.Status != StatusActive || sec.Pattern != PatternManual {
		t.Fatalf("unexpected secret %+v", sec)
	}
	if sec.CreatedAt != "2026-06-01T12:00:00Z" || sec.UpdatedAt != sec.CreatedAt {
		t.Fatalf("timestamps wrong: created=%q updated=%q", sec.CreatedAt, sec.UpdatedAt)
	}
}

func TestSetSecretUpdateKeepsCreatedBumpsUpdated(t *testing.T) {
	s := storeWithPrincipals("withings", "other")
	_ = s.SetSecret("withings_api", "v1", []string{"withings"}, PatternManual, testTime)
	// Metadata-only update: empty value (leave), change grants. (Value is roll-only
	// now, so a value change via set is rejected — see TestSetSecretValueImmutable.)
	if err := s.SetSecret("withings_api", "", []string{"withings", "other"}, PatternManual, laterTime); err != nil {
		t.Fatalf("update: %v", err)
	}
	sec, _ := s.Secret("withings_api")
	if sec.Value != "v1" {
		t.Fatalf("value changed on a metadata-only set: %q, want v1", sec.Value)
	}
	if len(sec.AuthorizedPrincipals) != 2 {
		t.Fatalf("grants not updated: %v", sec.AuthorizedPrincipals)
	}
	if sec.CreatedAt != "2026-06-01T12:00:00Z" {
		t.Fatalf("created_at changed on update: %q", sec.CreatedAt)
	}
	if sec.UpdatedAt != "2026-06-02T09:00:00Z" {
		t.Fatalf("updated_at not bumped: %q", sec.UpdatedAt)
	}
}

// TestSetSecretValueImmutable proves the #23 decision: set cannot change an
// existing value (that is roll's job); a differing value is refused, an empty or
// equal value leaves it.
func TestSetSecretValueImmutable(t *testing.T) {
	s := storeWithPrincipals("withings")
	_ = s.SetSecret("withings_api", "v1", []string{"withings"}, PatternManual, testTime)

	if err := s.SetSecret("withings_api", "v2", nil, "", laterTime); !errors.Is(err, ErrValueImmutable) {
		t.Fatalf("differing value via set: err = %v, want ErrValueImmutable", err)
	}
	if sec, _ := s.Secret("withings_api"); sec.Value != "v1" {
		t.Fatalf("value mutated despite rejection: %q", sec.Value)
	}
	if err := s.SetSecret("withings_api", "v1", nil, "", laterTime); err != nil {
		t.Fatalf("equal value should be a no-op: %v", err)
	}
	if err := s.SetSecret("withings_api", "", nil, "", laterTime); err != nil {
		t.Fatalf("empty value should leave it unchanged: %v", err)
	}
}

// TestSetSecretGrantsPartialUpdate proves nil leaves grants, [] clears, [list]
// replaces — the grant-wipe footgun fix (#23 facet a).
func TestSetSecretGrantsPartialUpdate(t *testing.T) {
	s := storeWithPrincipals("a", "b")
	_ = s.SetSecret("x", "v1", []string{"a"}, PatternManual, testTime)

	_ = s.SetSecret("x", "", nil, "", laterTime) // nil → leave
	if sec, _ := s.Secret("x"); len(sec.AuthorizedPrincipals) != 1 || sec.AuthorizedPrincipals[0] != "a" {
		t.Fatalf("nil authz should leave grants, got %v", sec.AuthorizedPrincipals)
	}
	_ = s.SetSecret("x", "", []string{"a", "b"}, "", laterTime) // [list] → replace
	if sec, _ := s.Secret("x"); len(sec.AuthorizedPrincipals) != 2 {
		t.Fatalf("list authz should replace, got %v", sec.AuthorizedPrincipals)
	}
	_ = s.SetSecret("x", "", []string{}, "", laterTime) // [] → clear
	if sec, _ := s.Secret("x"); len(sec.AuthorizedPrincipals) != 0 {
		t.Fatalf("empty authz should clear grants, got %v", sec.AuthorizedPrincipals)
	}
}

// TestSetSecretPatternLeftOnUpdate proves "" pattern leaves an existing pattern
// (so a metadata set can't silently flip an auto secret to manual).
func TestSetSecretPatternLeftOnUpdate(t *testing.T) {
	s := storeWithPrincipals("a")
	_ = s.SetSecret("x", "v1", []string{"a"}, PatternAuto, testTime)
	_ = s.SetSecret("x", "", nil, "", laterTime)
	if sec, _ := s.Secret("x"); sec.Pattern != PatternAuto {
		t.Fatalf("pattern flipped on a metadata set: %q, want auto", sec.Pattern)
	}
}

// TestSetSecretEmptyActiveManualRefused proves F7: an active manual secret can't
// be created empty; an auto secret can (it is minted by the first roll).
func TestSetSecretEmptyActiveManualRefused(t *testing.T) {
	s := storeWithPrincipals("a")
	if err := s.SetSecret("m", "", []string{"a"}, PatternManual, testTime); !errors.Is(err, ErrEmptyValue) {
		t.Fatalf("empty manual create: err = %v, want ErrEmptyValue", err)
	}
	if err := s.SetSecret("auto", "", []string{"a"}, PatternAuto, testTime); err != nil {
		t.Fatalf("empty auto create should be allowed (minted by roll): %v", err)
	}
}

func TestSetSecretUnknownPrincipalFails(t *testing.T) {
	s := storeWithPrincipals("withings")
	err := s.SetSecret("withings_api", "v1", []string{"withings", "ghost"}, PatternManual, testTime)
	if !errors.Is(err, ErrUnknownPrincipal) {
		t.Fatalf("err = %v, want ErrUnknownPrincipal", err)
	}
	if _, ok := s.Secret("withings_api"); ok {
		t.Fatal("secret was created despite an orphan grant (should fail-closed before mutation)")
	}
}

func TestSetSecretRevokedFails(t *testing.T) {
	s := storeWithPrincipals("withings")
	_ = s.SetSecret("withings_api", "v1", []string{"withings"}, PatternManual, testTime)
	sec, _ := s.Secret("withings_api")
	sec.Status = StatusRevoked // simulate a revoke (lifecycle op is a later rung)
	if err := s.SetSecret("withings_api", "v2", nil, PatternManual, laterTime); !errors.Is(err, ErrSecretRevoked) {
		t.Fatalf("err = %v, want ErrSecretRevoked", err)
	}
}

func TestSetSecretInvalidNameAndPattern(t *testing.T) {
	s := NewStore()
	if err := s.SetSecret("Bad Name", "v", nil, PatternManual, testTime); !errors.Is(err, ErrInvalidName) {
		t.Errorf("name err = %v, want ErrInvalidName", err)
	}
	if err := s.SetSecret("ok_name", "v", nil, "weird", testTime); !errors.Is(err, ErrInvalidPattern) {
		t.Errorf("pattern err = %v, want ErrInvalidPattern", err)
	}
}

func TestSetSecretDefensiveCopyOfPrincipals(t *testing.T) {
	s := storeWithPrincipals("withings", "other")
	authz := []string{"withings"}
	_ = s.SetSecret("withings_api", "v1", authz, PatternManual, testTime)
	authz[0] = "other" // mutate caller's slice
	sec, _ := s.Secret("withings_api")
	if sec.AuthorizedPrincipals[0] != "withings" {
		t.Fatal("model aliased the caller's slice (no defensive copy)")
	}
}

func TestCheckHealthyAndOrphan(t *testing.T) {
	s := storeWithPrincipals("withings")
	_ = s.SetSecret("withings_api", "v1", []string{"withings"}, PatternManual, testTime)
	if got := s.Check(); len(got) != 0 {
		t.Fatalf("healthy store reported issues: %+v", got)
	}

	// Simulate an orphan (e.g. a future purge-principal removing a principal a
	// secret still references) by injecting state directly.
	s.Secrets["withings_api"].AuthorizedPrincipals = []string{"withings", "gone"}
	issues := s.Check()
	if len(issues) != 1 || issues[0].Kind != "secret" || issues[0].Target != "withings_api" {
		t.Fatalf("Check did not flag the orphan grant: %+v", issues)
	}
}
