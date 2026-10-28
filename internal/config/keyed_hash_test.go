package config

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

// ComputeFileHash is the plain SHA-256 of the bytes, tagged "sha256:".
func TestComputeFileHashIsSHA256(t *testing.T) {
	t.Parallel()
	const content = "config file bytes"
	f := writeTempFile(t, content)

	got, err := ComputeFileHash(f)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

// Same bytes + same key -> stable digest equal to HMAC-SHA256; the keyed digest
// differs from the plain SHA-256 digest (proves the key is actually mixed in).
func TestKeyedHashStableAndDiffersFromPlain(t *testing.T) {
	t.Parallel()
	const content = "plugin entrypoint bytes"
	f := writeTempFile(t, content)
	key := key32(0x01)

	keyed1, err := ComputeKeyedFileHash(f, key)
	if err != nil {
		t.Fatalf("keyed hash: %v", err)
	}
	keyed2, err := ComputeKeyedFileHash(f, key)
	if err != nil {
		t.Fatalf("keyed hash (repeat): %v", err)
	}
	if keyed1 != keyed2 {
		t.Fatalf("keyed hash not stable: %s vs %s", keyed1, keyed2)
	}

	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(content))
	want := "sha256:" + hex.EncodeToString(mac.Sum(nil))
	if keyed1 != want {
		t.Fatalf("keyed hash = %q, want HMAC-SHA256 %q", keyed1, want)
	}

	plain, err := ComputeFileHash(f)
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
	a, err := ComputeKeyedFileHash(f, key32(0x01))
	if err != nil {
		t.Fatalf("hash A: %v", err)
	}
	b, err := ComputeKeyedFileHash(f, key32(0x02))
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
	for _, n := range []int{16, 31, 33} {
		if _, err := ComputeKeyedFileHash(f, make([]byte, n)); err == nil {
			t.Fatalf("expected error for %d-byte key, got nil", n)
		}
	}
	if _, err := ComputeKeyedFileHash(f, nil); err == nil {
		t.Fatal("expected error for nil key, got nil")
	}
}
