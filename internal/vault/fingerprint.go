package vault

import (
	"encoding/base64"
	"fmt"
)

// FingerprintNonce returns the 32-byte keying material for keyed-BLAKE3 plugin
// attestation. The nonce is held on the reserved `core` principal, seeded at
// genesis, and never delivered to a plugin.
//
// Fail-closed (Armstrong): a missing `core` principal, an empty nonce, an
// undecodable nonce, or a wrong-length nonce are all hard errors — never a
// zero/partial key. The caller (lock and verify) must treat "no nonce" as a
// refusal to attest, not a silent downgrade to an unkeyed hash.
func (v *Vault) FingerprintNonce() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	p, ok := v.store.Principals[CorePrincipal]
	if !ok {
		return nil, fmt.Errorf("vault: no %q principal; cannot source fingerprint nonce", CorePrincipal)
	}
	if p.Nonce == "" {
		return nil, fmt.Errorf("vault: %q principal has no fingerprint nonce", CorePrincipal)
	}
	key, err := base64.RawURLEncoding.DecodeString(p.Nonce)
	if err != nil {
		return nil, fmt.Errorf("vault: decode fingerprint nonce: %w", err)
	}
	if len(key) != nonceBytes {
		return nil, fmt.Errorf("vault: fingerprint nonce is %d bytes, want %d", len(key), nonceBytes)
	}
	return key, nil
}
