package vault

import (
	"fmt"
	"slices"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
)

// rolledSecretBytes is the entropy of an auto-pattern rolled value, matching the
// genesis admin-token width.
const rolledSecretBytes = 32

// Roll supersedes a secret's value as a guarded, persisted mutation. For a
// manual secret the operator supplies operatorValue; for an auto secret the
// vault mints a fresh CSPRNG value and operatorValue is ignored. Minting is the
// owner's job (randomness lives here, not in the pure model).
func (v *Vault) Roll(name, operatorValue string, now time.Time) error {
	return v.mutate(func(s *Store) error {
		newValue, err := rolledValue(s, name, operatorValue)
		if err != nil {
			return err
		}
		return s.RollSecret(name, newValue, now)
	})
}

// rolledValue resolves the value a roll should apply: a minted CSPRNG token for
// an auto secret, or the operator-supplied value for a manual one.
func rolledValue(s *Store, name, operatorValue string) (string, error) {
	sec, ok := s.Secrets[name]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownSecret, name)
	}
	if sec.Pattern == PatternAuto {
		return secrets.GenerateToken(rolledSecretBytes)
	}
	return operatorValue, nil
}

// RegisterPrincipal adds a new deliver-to principal as a guarded, persisted
// mutation. Registration is the operator's act of admitting an identity into
// the secret-authorization model (registration = authorization intent); a
// secret can only grant to a principal that exists.
func (v *Vault) RegisterPrincipal(name, kind string) error {
	return v.mutate(func(s *Store) error { return s.RegisterPrincipal(name, kind) })
}

// Revoke marks a secret revoked as a guarded, persisted mutation (idempotent).
func (v *Vault) Revoke(name string, now time.Time) error {
	return v.mutate(func(s *Store) error { return s.RevokeSecret(name, now) })
}

// RevokePrincipal marks a principal revoked as a guarded, persisted mutation.
func (v *Vault) RevokePrincipal(name string) error {
	return v.mutate(func(s *Store) error { return s.RevokePrincipal(name) })
}

// PurgePrincipal removes a principal and its grants as a guarded, persisted
// mutation.
func (v *Vault) PurgePrincipal(name string) error {
	return v.mutate(func(s *Store) error { return s.PurgePrincipal(name) })
}

// RollPrincipal rolls every auto-pattern secret the principal is authorized for,
// minting a fresh value for each, in one atomic persisted batch. Manual secrets
// cannot be auto-rolled (they need an operator value) and are returned in
// skipped rather than silently ignored (Armstrong: name what was not done).
// Revoked secrets are not part of the live set and are left untouched.
func (v *Vault) RollPrincipal(name string, now time.Time) (rolled, skipped []string, err error) {
	type result struct{ rolled, skipped []string }
	// mutateR makes the whole batch atomic: a mint/roll failure partway through
	// restores the model, so a partial roll can't be left resident in memory (F6).
	res, err := mutateR(v, func(s *Store) (result, error) {
		if _, ok := s.Principals[name]; !ok {
			return result{}, fmt.Errorf("%w: %q", ErrUnknownPrincipal, name)
		}
		var r result
		for _, secName := range s.SecretNames() {
			sec := s.Secrets[secName]
			if !slices.Contains(sec.AuthorizedPrincipals, name) || sec.Status == StatusRevoked {
				continue
			}
			if sec.Pattern != PatternAuto {
				r.skipped = append(r.skipped, secName)
				continue
			}
			newValue, mintErr := secrets.GenerateToken(rolledSecretBytes)
			if mintErr != nil {
				return result{}, fmt.Errorf("mint %q: %w", secName, mintErr)
			}
			if rollErr := s.RollSecret(secName, newValue, now); rollErr != nil {
				return result{}, rollErr
			}
			r.rolled = append(r.rolled, secName)
		}
		return r, nil
	})
	return res.rolled, res.skipped, err
}
