package vault

import (
	"fmt"
	"slices"
)

// DenialReason is the typed vocabulary for why a secret was withheld. A withheld
// secret is a named signal, not a silent omission (Armstrong).
type DenialReason string

const (
	// DenialSecretRevoked: the principal is authorized for the secret, but the
	// secret is revoked. Emitted by Compose.
	DenialSecretRevoked DenialReason = "secret_revoked"
	// DenialPrincipalNotAuthorized: a specific secret was requested for a
	// principal not in its authorized list. Emitted by caller layers that ask
	// for a named secret (Compose composes a principal's whole grant, so it does
	// not emit this).
	DenialPrincipalNotAuthorized DenialReason = "principal_not_authorized"
	// DenialFingerprintMismatch: the principal's identity failed verification.
	// In Rung 1 the gateway verifies against the existing .checksums machinery at
	// spawn, so this is emitted by that caller layer, not by Compose (the vault
	// stores no fingerprint yet — keyed-nonce is Rung 4).
	DenialFingerprintMismatch DenialReason = "fingerprint_mismatch"
)

// Denial records one withheld secret and why.
type Denial struct {
	Secret string       `json:"secret"`
	Reason DenialReason `json:"reason"`
}

// Composition is the result of resolving a principal's secrets: the delivered
// set plus the typed denials for anything withheld.
type Composition struct {
	Secrets map[string]string `json:"secrets"`
	Denials []Denial          `json:"denials"`
}

// Compose resolves the secrets a principal may receive. It is a pure query over
// the in-memory model (no decryption, no I/O — the store is already decrypted).
//
// Fail-closed (Armstrong): the principal must be REGISTERED and ACTIVE, else a
// hard error — never an empty Composition, which a caller could mistake for "no
// secrets" rather than "this principal must not run." For an authorized secret
// that is revoked, the name appears in Denials (reason secret_revoked) rather
// than being silently dropped.
//
// Compose reasons only about the vault's own data (registration, authorization,
// status). It does NOT verify plugin identity/fingerprint — that is a separate
// concern performed by the caller (the gateway, via .checksums in Rung 1; the
// keyed-nonce upgrade is Rung 4). Keeping it out avoids complecting secret
// authorization with attestation.
func (s *Store) Compose(principal string) (Composition, error) {
	p, ok := s.Principals[principal]
	if !ok {
		return Composition{}, fmt.Errorf("%w: %q", ErrUnknownPrincipal, principal)
	}
	if p.Status != StatusActive {
		return Composition{}, fmt.Errorf("%w: %q is %s", ErrPrincipalInactive, principal, p.Status)
	}

	comp := Composition{Secrets: make(map[string]string)}
	// Iterate in sorted name order so Denials are deterministic.
	for _, name := range s.SecretNames() {
		sec := s.Secrets[name]
		if !slices.Contains(sec.AuthorizedPrincipals, principal) {
			continue // not for this principal; not a denial
		}
		if sec.Status == StatusActive {
			comp.Secrets[name] = sec.Value
		} else {
			comp.Denials = append(comp.Denials, Denial{Secret: name, Reason: DenialSecretRevoked})
		}
	}
	return comp, nil
}
