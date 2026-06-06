package secrets

import (
	"strings"
	"testing"

	"filippo.io/age"
)

func newTestIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	plaintext := []byte("tokens:\n  - name: withings\n    key: super-secret\n")

	ciphertext, err := Encrypt(plaintext, []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !IsEncrypted(ciphertext) {
		t.Fatal("Encrypt output not detected as encrypted")
	}
	if strings.Contains(string(ciphertext), "super-secret") {
		t.Fatal("plaintext secret leaked into ciphertext")
	}

	got, err := Decrypt(ciphertext, []age.Identity{id})
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	wrong := newTestIdentity(t)

	ciphertext, err := Encrypt([]byte("secret"), []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(ciphertext, []age.Identity{wrong}); err == nil {
		t.Fatal("Decrypt with wrong key succeeded; want hard failure")
	}
}

func TestDecryptNoIdentitiesFails(t *testing.T) {
	t.Parallel()
	if _, err := Decrypt([]byte("anything"), nil); err == nil {
		t.Fatal("Decrypt with no identities succeeded; want error")
	}
}

func TestDecryptCorruptCiphertextFails(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	if _, err := Decrypt([]byte("age-encryption.org/v1\nnot a real body"), []age.Identity{id}); err == nil {
		t.Fatal("Decrypt of corrupt ciphertext succeeded; want error")
	}
}

func TestIsEncrypted(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	ciphertext, err := Encrypt([]byte("x"), []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	cases := map[string]struct {
		data []byte
		want bool
	}{
		"armored ciphertext":    {ciphertext, true},
		"binary header":         {[]byte("age-encryption.org/v1\n..."), true},
		"plaintext yaml":        {[]byte("tokens:\n  - name: x\n"), false},
		"empty":                 {[]byte(""), false},
		"leading-whitespace cy": {append([]byte("\n  "), ciphertext...), true},
	}
	for name, tc := range cases {
		if got := IsEncrypted(tc.data); got != tc.want {
			t.Errorf("%s: IsEncrypted = %v, want %v", name, got, tc.want)
		}
	}
}

func TestParseIdentitiesRoundTrip(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	keyFile := "# comment\n\n" + id.String() + "\n"
	ids, err := ParseIdentities([]byte(keyFile))
	if err != nil {
		t.Fatalf("ParseIdentities: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("got %d identities, want 1", len(ids))
	}
}

func TestParseRecipientsRoundTrip(t *testing.T) {
	t.Parallel()
	id := newTestIdentity(t)
	body := "# hosts\n" + id.Recipient().String() + "\n"
	recs, err := ParseRecipients([]byte(body))
	if err != nil {
		t.Fatalf("ParseRecipients: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d recipients, want 1", len(recs))
	}
}
