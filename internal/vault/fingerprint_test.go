package vault

import (
	"encoding/base64"
	"testing"
)

func TestFingerprintNonceReturns32Bytes(t *testing.T) {
	t.Parallel()
	store := NewStore()
	store.SeedCorePrincipal("") // sanctioned seed; nonce set explicitly below
	raw := make([]byte, nonceBytes)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	store.Principals[CorePrincipal].Nonce = base64.RawURLEncoding.EncodeToString(raw)

	v := New("", nil, store)
	key, err := v.FingerprintNonce()
	if err != nil {
		t.Fatalf("FingerprintNonce: %v", err)
	}
	if len(key) != nonceBytes {
		t.Fatalf("expected %d-byte key, got %d", nonceBytes, len(key))
	}
	if string(key) != string(raw) {
		t.Fatalf("key does not round-trip the stored nonce bytes")
	}
}

func TestFingerprintNonceFailsClosedWithoutCore(t *testing.T) {
	t.Parallel()
	v := New("", nil, NewStore())
	if _, err := v.FingerprintNonce(); err == nil {
		t.Fatal("expected a hard error when no core principal exists, got nil")
	}
}

func TestFingerprintNonceFailsClosedWithoutNonce(t *testing.T) {
	t.Parallel()
	store := NewStore()
	store.SeedCorePrincipal("") // core exists but no nonce was seeded.
	v := New("", nil, store)
	if _, err := v.FingerprintNonce(); err == nil {
		t.Fatal("expected a hard error when core has no nonce, got nil")
	}
}
