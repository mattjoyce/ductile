package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateToken returns a cryptographically random, URL-safe token derived from
// nBytes of entropy from the OS CSPRNG. Used for vault-minted secret material
// (the fingerprint nonce, the initial admin token).
func GenerateToken(nBytes int) (string, error) {
	if nBytes <= 0 {
		return "", fmt.Errorf("generate token: nBytes must be positive, got %d", nBytes)
	}
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: read entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
