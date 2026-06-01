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
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok := v.store.Principals[name]; !ok {
		return nil, nil, fmt.Errorf("%w: %q", ErrUnknownPrincipal, name)
	}

	for _, secName := range v.store.SecretNames() {
		sec := v.store.Secrets[secName]
		if !slices.Contains(sec.AuthorizedPrincipals, name) || sec.Status == StatusRevoked {
			continue
		}
		if sec.Pattern != PatternAuto {
			skipped = append(skipped, secName)
			continue
		}
		newValue, mintErr := secrets.GenerateToken(rolledSecretBytes)
		if mintErr != nil {
			return nil, nil, fmt.Errorf("mint %q: %w", secName, mintErr)
		}
		if rollErr := v.store.RollSecret(secName, newValue, now); rollErr != nil {
			return nil, nil, rollErr
		}
		rolled = append(rolled, secName)
	}

	if saveErr := v.Save(); saveErr != nil {
		if rbErr := v.restoreFromLastYAML(); rbErr != nil {
			return nil, nil, fmt.Errorf("%w (rollback also failed: %v)", saveErr, rbErr)
		}
		return nil, nil, saveErr
	}
	return rolled, skipped, nil
}
