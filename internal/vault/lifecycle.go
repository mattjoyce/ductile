package vault

import (
	"fmt"
	"slices"
	"time"
)

// This file is the *process* half of the secret/principal lifecycle: roll,
// revoke, and purge. All are pure model mutations (no I/O, no randomness) — the
// caller persists via Save, and the vault owner mints CSPRNG values for auto
// rolls before calling RollSecret. now is injected so the model stays clock-free.

// RollSecret supersedes a secret's value with newValue. Values are immutable:
// the prior value is overwritten (no retained version) and roll_count is bumped
// for audit. Rolling a revoked secret is refused — revocation is terminal; use
// a fresh name. The pure op applies the value it is given; minting an `auto`
// value from a CSPRNG is the owner's job, not the model's.
func (s *Store) RollSecret(name, newValue string, now time.Time) error {
	sec, ok := s.Secrets[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSecret, name)
	}
	if sec.Status == StatusRevoked {
		return fmt.Errorf("%w: %q", ErrSecretRevoked, name)
	}
	sec.Value = newValue
	sec.RollCount++
	sec.UpdatedAt = now.UTC().Format(time.RFC3339)
	return nil
}

// RevokeSecret marks a secret revoked (terminal). Idempotent: revoking an
// already-revoked secret is a no-op that leaves the original revoked_at intact.
func (s *Store) RevokeSecret(name string, now time.Time) error {
	sec, ok := s.Secrets[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSecret, name)
	}
	if sec.Status == StatusRevoked {
		return nil // idempotent
	}
	sec.Status = StatusRevoked
	sec.RevokedAt = now.UTC().Format(time.RFC3339)
	return nil
}

// RevokePrincipal marks a principal revoked, so Compose fails closed for it (its
// secrets stop being delivered). Idempotent. The principal and its grants remain
// in the model — use PurgePrincipal to remove them entirely.
func (s *Store) RevokePrincipal(name string) error {
	p, ok := s.Principals[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownPrincipal, name)
	}
	p.Status = StatusRevoked
	return nil
}

// PurgePrincipal removes a principal from the registry AND strips it from every
// secret's authorized list, atomically (one pass over the in-memory model under
// the owner's write lock), so no orphan grant is left behind.
func (s *Store) PurgePrincipal(name string) error {
	if _, ok := s.Principals[name]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownPrincipal, name)
	}
	delete(s.Principals, name)
	for _, sec := range s.Secrets {
		if i := slices.Index(sec.AuthorizedPrincipals, name); i >= 0 {
			sec.AuthorizedPrincipals = slices.Delete(sec.AuthorizedPrincipals, i, i+1)
		}
	}
	return nil
}
