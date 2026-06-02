package vault

import "time"

// Reserved entities are the gateway's own bootstrap identities: the `core`
// principal (holds the fingerprint nonce) and the `core-admin-token` secret
// (the management-API credential). They are seeded at genesis and must NEVER be
// mutated through the data plane (set/roll/revoke/purge) — otherwise an operator
// or a compromised admin token could grant the admin credential to a plugin
// (privilege escalation), brick attestation, or brick the API. Identity is
// enforced here, not merely asserted in a comment.

// isReservedSecret reports whether name is a reserved secret entry.
func isReservedSecret(name string) bool { return name == AdminTokenSecret }

// isReservedPrincipal reports whether name is a reserved principal.
func isReservedPrincipal(name string) bool { return name == CorePrincipal }

// RotateAdminToken is the ONLY sanctioned path to write the reserved
// core-admin-token secret. It create-or-updates the token, forcing nil
// authorized_principals and active status — so the credential can never be
// granted to a principal (the data-plane SetSecret/RollSecret/RevokeSecret all
// refuse it via the reserved guard). Used by genesis and by admin-token rotation.
func (s *Store) RotateAdminToken(newValue string, now time.Time) error {
	ts := now.UTC().Format(time.RFC3339)
	if existing, ok := s.Secrets[AdminTokenSecret]; ok {
		existing.Value = newValue
		existing.AuthorizedPrincipals = nil
		existing.Status = StatusActive
		existing.RollCount++
		existing.UpdatedAt = ts
		return nil
	}
	s.Secrets[AdminTokenSecret] = &Secret{
		Value:                newValue,
		AuthorizedPrincipals: nil,
		Status:               StatusActive,
		Pattern:              PatternAuto,
		CreatedAt:            ts,
		UpdatedAt:            ts,
	}
	return nil
}
