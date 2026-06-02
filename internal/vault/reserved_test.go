package vault

import (
	"errors"
	"testing"
	"time"
)

// seedReservedStore builds a genesis-like store: core principal + nonce, the
// reserved admin-token secret, and one ordinary plugin principal.
func seedReservedStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	if err := s.RegisterPrincipal(CorePrincipal, KindGateway); err != nil {
		t.Fatalf("register core: %v", err)
	}
	if err := s.RotateAdminToken("initial-admin-token", time.Now()); err != nil {
		t.Fatalf("seed admin token: %v", err)
	}
	if err := s.RegisterPrincipal("mailer", KindPlugin); err != nil {
		t.Fatalf("register plugin: %v", err)
	}
	return s
}

// F1 — the privilege-escalation hole: granting the admin token to a plugin must
// be refused, so the management credential can never be composed to a principal.
func TestSetSecretRefusesReservedAdminToken(t *testing.T) {
	s := seedReservedStore(t)
	err := s.SetSecret(AdminTokenSecret, "attacker", []string{"mailer"}, PatternManual, time.Now())
	if !errors.Is(err, ErrReservedEntity) {
		t.Fatalf("expected ErrReservedEntity, got %v", err)
	}
	// The admin token must still have no authorized principals.
	if got := s.Secrets[AdminTokenSecret].AuthorizedPrincipals; len(got) != 0 {
		t.Fatalf("admin token gained authorized principals: %v", got)
	}
	// And it must never appear in a principal's Composition.
	comp, err := s.Compose("mailer")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if _, leaked := comp.Secrets[AdminTokenSecret]; leaked {
		t.Fatal("admin token leaked into a plugin Composition")
	}
}

// F3 — value overwrite / revoke / roll of the reserved token via the data plane
// must be refused (no bricking the API through set/roll/revoke).
func TestReservedAdminTokenDataPlaneMutationsRefused(t *testing.T) {
	s := seedReservedStore(t)
	now := time.Now()
	if err := s.SetSecret(AdminTokenSecret, "x", nil, PatternManual, now); !errors.Is(err, ErrReservedEntity) {
		t.Fatalf("set: expected ErrReservedEntity, got %v", err)
	}
	if err := s.RollSecret(AdminTokenSecret, "x", now); !errors.Is(err, ErrReservedEntity) {
		t.Fatalf("roll: expected ErrReservedEntity, got %v", err)
	}
	if err := s.RevokeSecret(AdminTokenSecret, now); !errors.Is(err, ErrReservedEntity) {
		t.Fatalf("revoke: expected ErrReservedEntity, got %v", err)
	}
	if s.Secrets[AdminTokenSecret].Value != "initial-admin-token" {
		t.Fatal("admin token value was mutated despite the guard")
	}
}

// F2 — the reserved core principal cannot be revoked or purged via the data plane.
func TestReservedCorePrincipalMutationsRefused(t *testing.T) {
	s := seedReservedStore(t)
	if err := s.RevokePrincipal(CorePrincipal); !errors.Is(err, ErrReservedEntity) {
		t.Fatalf("revoke-principal core: expected ErrReservedEntity, got %v", err)
	}
	if err := s.PurgePrincipal(CorePrincipal); !errors.Is(err, ErrReservedEntity) {
		t.Fatalf("purge-principal core: expected ErrReservedEntity, got %v", err)
	}
	if _, ok := s.Principals[CorePrincipal]; !ok {
		t.Fatal("core principal was removed despite the guard")
	}
}

// RotateAdminToken is the sanctioned path: it updates the value, bumps the roll
// count, and keeps the token un-granted.
func TestRotateAdminTokenIsSanctionedPath(t *testing.T) {
	s := seedReservedStore(t)
	before := s.Secrets[AdminTokenSecret].RollCount
	if err := s.RotateAdminToken("rotated-token", time.Now()); err != nil {
		t.Fatalf("RotateAdminToken: %v", err)
	}
	sec := s.Secrets[AdminTokenSecret]
	if sec.Value != "rotated-token" {
		t.Fatalf("value not rotated: %q", sec.Value)
	}
	if sec.RollCount != before+1 {
		t.Fatalf("roll count not bumped: %d", sec.RollCount)
	}
	if len(sec.AuthorizedPrincipals) != 0 || sec.Status != StatusActive {
		t.Fatalf("rotated token must stay active and un-granted: %+v", sec)
	}
}

// Ordinary secrets are unaffected by the reserved guard.
func TestNonReservedSecretsStillMutable(t *testing.T) {
	s := seedReservedStore(t)
	now := time.Now()
	if err := s.SetSecret("api_key", "v1", []string{"mailer"}, PatternManual, now); err != nil {
		t.Fatalf("set normal secret: %v", err)
	}
	if err := s.RollSecret("api_key", "v2", now); err != nil {
		t.Fatalf("roll normal secret: %v", err)
	}
	if err := s.RevokeSecret("api_key", now); err != nil {
		t.Fatalf("revoke normal secret: %v", err)
	}
}
