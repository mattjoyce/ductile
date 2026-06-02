package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func key32(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

// Same bytes + same key -> stable digest; the keyed digest differs from the
// plain BLAKE3 digest of the same bytes (proves the key is actually mixed in).
func TestKeyedHashStableAndDiffersFromPlain(t *testing.T) {
	t.Parallel()
	f := writeTempFile(t, "plugin entrypoint bytes")

	keyed1, err := ComputeKeyedBlake3Hash(f, key32(0x01))
	if err != nil {
		t.Fatalf("keyed hash: %v", err)
	}
	keyed2, err := ComputeKeyedBlake3Hash(f, key32(0x01))
	if err != nil {
		t.Fatalf("keyed hash (repeat): %v", err)
	}
	if keyed1 != keyed2 {
		t.Fatalf("keyed hash not stable: %s vs %s", keyed1, keyed2)
	}

	plain, err := ComputeBlake3Hash(f)
	if err != nil {
		t.Fatalf("plain hash: %v", err)
	}
	if keyed1 == plain {
		t.Fatal("keyed digest equals plain digest — key was not mixed in")
	}
}

// A different key over the same bytes yields a different digest — this is what
// makes the fingerprint unforgeable without the vault nonce.
func TestKeyedHashDiffersByKey(t *testing.T) {
	t.Parallel()
	f := writeTempFile(t, "same bytes")
	a, err := ComputeKeyedBlake3Hash(f, key32(0x01))
	if err != nil {
		t.Fatalf("hash A: %v", err)
	}
	b, err := ComputeKeyedBlake3Hash(f, key32(0x02))
	if err != nil {
		t.Fatalf("hash B: %v", err)
	}
	if a == b {
		t.Fatal("different keys produced the same digest")
	}
}

// A wrong-length key is a hard error, never a silent unkeyed digest.
func TestKeyedHashRejectsWrongKeyLength(t *testing.T) {
	t.Parallel()
	f := writeTempFile(t, "x")
	if _, err := ComputeKeyedBlake3Hash(f, make([]byte, 16)); err == nil {
		t.Fatal("expected error for 16-byte key, got nil")
	}
	if _, err := ComputeKeyedBlake3Hash(f, nil); err == nil {
		t.Fatal("expected error for nil key, got nil")
	}
}
