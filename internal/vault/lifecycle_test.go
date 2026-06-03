package vault

import (
	"errors"
	"slices"
	"testing"
)

// lifecycleFixture: principals mailer (active) + other; secrets api (mailer),
// shared (mailer+other), gone (other, will be used for revoke tests).
func lifecycleFixture(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	for _, p := range []string{"mailer", "other"} {
		if err := s.RegisterPrincipal(p, KindPlugin); err != nil {
			t.Fatalf("register %s: %v", p, err)
		}
	}
	if err := s.SetSecret("api", "V0", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecret("shared", "S0", []string{"mailer", "other"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRollSecretSupersedesAndBumpsCount(t *testing.T) {
	s := lifecycleFixture(t)

	if err := s.RollSecret("api", "V1", testTime); err != nil {
		t.Fatalf("roll: %v", err)
	}
	sec, _ := s.Secret("api")
	if sec.Value != "V1" {
		t.Errorf("roll must replace the value, got %q", sec.Value)
	}
	if sec.RollCount != 1 {
		t.Errorf("roll must bump roll_count to 1, got %d", sec.RollCount)
	}

	if err := s.RollSecret("api", "V2", testTime); err != nil {
		t.Fatalf("second roll: %v", err)
	}
	sec, _ = s.Secret("api")
	if sec.Value != "V2" || sec.RollCount != 2 {
		t.Errorf("expected V2/count 2, got %q/%d", sec.Value, sec.RollCount)
	}
}

func TestRollSecretUnknownAndRevoked(t *testing.T) {
	s := lifecycleFixture(t)

	if err := s.RollSecret("ghost", "X", testTime); !errors.Is(err, ErrUnknownSecret) {
		t.Errorf("roll missing secret: want ErrUnknownSecret, got %v", err)
	}

	if err := s.RevokeSecret("api", testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.RollSecret("api", "X", testTime); !errors.Is(err, ErrSecretRevoked) {
		t.Errorf("roll revoked secret must refuse with ErrSecretRevoked, got %v", err)
	}
}

func TestRevokeSecretIsIdempotent(t *testing.T) {
	s := lifecycleFixture(t)

	if err := s.RevokeSecret("api", testTime); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	sec, _ := s.Secret("api")
	if sec.Status != StatusRevoked {
		t.Errorf("expected revoked status, got %q", sec.Status)
	}
	if sec.RevokedAt == "" {
		t.Error("expected revoked_at timestamp")
	}
	firstRevokedAt := sec.RevokedAt

	// Idempotent: revoking again is not an error and does not move the timestamp.
	if err := s.RevokeSecret("api", testTime.Add(1)); err != nil {
		t.Fatalf("second revoke must be idempotent, got %v", err)
	}
	sec, _ = s.Secret("api")
	if sec.RevokedAt != firstRevokedAt {
		t.Errorf("idempotent revoke must not move revoked_at: %q -> %q", firstRevokedAt, sec.RevokedAt)
	}

	if err := s.RevokeSecret("ghost", testTime); !errors.Is(err, ErrUnknownSecret) {
		t.Errorf("revoke missing: want ErrUnknownSecret, got %v", err)
	}
}

func TestRevokeSecretClearsValue(t *testing.T) {
	s := lifecycleFixture(t)

	// Roll to a known plaintext so the assertion can't pass vacuously.
	if err := s.RollSecret("api", "SUPERSECRET", testTime); err != nil {
		t.Fatalf("roll: %v", err)
	}

	if err := s.RevokeSecret("api", testTime); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	sec, _ := s.Secret("api")
	// ADR §3.3: revoke clears the value and tombstones the entry — no plaintext
	// must linger at rest in the (encrypted) blob after revocation.
	if sec.Value != "" {
		t.Errorf("revoke must clear the value, got %q", sec.Value)
	}
	if sec.Status != StatusRevoked {
		t.Errorf("expected revoked status, got %q", sec.Status)
	}
	if sec.RevokedAt == "" {
		t.Error("expected revoked_at tombstone timestamp")
	}
}

func TestRevokePrincipalMarksRevoked(t *testing.T) {
	s := lifecycleFixture(t)

	if err := s.RevokePrincipal("mailer"); err != nil {
		t.Fatalf("revoke principal: %v", err)
	}
	p, _ := s.Principal("mailer")
	if p.Status != StatusRevoked {
		t.Errorf("expected revoked principal, got %q", p.Status)
	}
	// Idempotent.
	if err := s.RevokePrincipal("mailer"); err != nil {
		t.Errorf("second revoke-principal must be idempotent, got %v", err)
	}
	if err := s.RevokePrincipal("ghost"); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("revoke missing principal: want ErrUnknownPrincipal, got %v", err)
	}
}

func TestPurgePrincipalIsAtomicAndLeavesNoOrphanGrants(t *testing.T) {
	s := lifecycleFixture(t)

	if err := s.PurgePrincipal("mailer"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := s.Principal("mailer"); ok {
		t.Error("purged principal must be removed from the registry")
	}
	// 'mailer' must be gone from every secret's authorized list (no orphan grant).
	for _, name := range s.SecretNames() {
		sec, _ := s.Secret(name)
		if slices.Contains(sec.AuthorizedPrincipals, "mailer") {
			t.Errorf("secret %q still authorizes the purged principal", name)
		}
	}
	// 'other' grant on 'shared' must survive.
	shared, _ := s.Secret("shared")
	if !slices.Contains(shared.AuthorizedPrincipals, "other") {
		t.Error("purge must not touch other principals' grants")
	}
	// The model must be clean (no orphan-grant issues).
	if issues := s.Check(); len(issues) != 0 {
		t.Errorf("purge must leave a clean model, got issues: %+v", issues)
	}

	if err := s.PurgePrincipal("ghost"); !errors.Is(err, ErrUnknownPrincipal) {
		t.Errorf("purge missing principal: want ErrUnknownPrincipal, got %v", err)
	}
}
