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
	s := storeWithPrincipals("withings")
	_ = s.SetSecret("withings_api", "v1", []string{"withings"}, PatternManual, testTime)
	if err := s.SetSecret("withings_api", "v2", nil, PatternManual, laterTime); err != nil {
		t.Fatalf("update: %v", err)
	}
	sec, _ := s.Secret("withings_api")
	if sec.Value != "v2" {
		t.Fatalf("value = %q, want v2", sec.Value)
	}
	if sec.CreatedAt != "2026-06-01T12:00:00Z" {
		t.Fatalf("created_at changed on update: %q", sec.CreatedAt)
	}
	if sec.UpdatedAt != "2026-06-02T09:00:00Z" {
		t.Fatalf("updated_at not bumped: %q", sec.UpdatedAt)
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
