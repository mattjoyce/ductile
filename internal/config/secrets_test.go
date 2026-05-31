package config

import (
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/secrets"
)

func makeKeyringForTest(t *testing.T) (*secrets.Keyring, *age.X25519Identity) {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	path := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod key: %v", err)
	}
	kr, err := secrets.LoadKeyringFromFile(path)
	if err != nil {
		t.Fatalf("load keyring: %v", err)
	}
	return kr, id
}

func TestReadConfigBytesPlaintextPassthrough(t *testing.T) {
	t.Parallel()
	kr, _ := makeKeyringForTest(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	plaintext := []byte("service:\n  name: ductile\n")
	if err := os.WriteFile(path, plaintext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readConfigBytes(kr, path)
	if err != nil {
		t.Fatalf("readConfigBytes: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("plaintext changed: %q", got)
	}
}

func TestReadConfigBytesDecryptsEncrypted(t *testing.T) {
	t.Parallel()
	kr, id := makeKeyringForTest(t)
	plaintext := []byte("service:\n  name: ductile\n")
	ciphertext, err := secrets.Encrypt(plaintext, []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readConfigBytes(kr, path)
	if err != nil {
		t.Fatalf("readConfigBytes: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("decrypted mismatch: %q", got)
	}
}

func TestReadConfigBytesEncryptedNoKeyFails(t *testing.T) {
	t.Parallel()
	_, id := makeKeyringForTest(t)
	ciphertext, err := secrets.Encrypt([]byte("x"), []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readConfigBytes(&secrets.Keyring{}, path); err == nil {
		t.Fatal("encrypted file with empty keyring decrypted; want hard failure")
	}
}

// TestGraftTokensEncryptedThenInterpolated proves the load ordering: an encrypted
// tokens file is decrypted first, then ${ENV} interpolation runs over the
// plaintext, then YAML is parsed.
func TestGraftTokensEncryptedThenInterpolated(t *testing.T) {
	kr, id := makeKeyringForTest(t)
	t.Setenv("TEST_WITHINGS_TOKEN", "interpolated-secret")

	plaintext := []byte("tokens:\n  - name: withings\n    key: ${TEST_WITHINGS_TOKEN}\n")
	ciphertext, err := secrets.Encrypt(plaintext, []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg := &Config{}
	if err := graftTokens(cfg, path, kr); err != nil {
		t.Fatalf("graftTokens: %v", err)
	}
	if len(cfg.Tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(cfg.Tokens))
	}
	if cfg.Tokens[0].Key != "interpolated-secret" {
		t.Fatalf("token key = %q, want interpolated-secret (decrypt-then-interpolate)", cfg.Tokens[0].Key)
	}
}

func TestGraftTokensPlaintextBackwardCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.yaml")
	if err := os.WriteFile(path, []byte("tokens:\n  - name: a\n    key: plain\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg := &Config{}
	// An empty keyring must still load plaintext tokens (encryption off).
	if err := graftTokens(cfg, path, &secrets.Keyring{}); err != nil {
		t.Fatalf("graftTokens plaintext: %v", err)
	}
	if len(cfg.Tokens) != 1 || cfg.Tokens[0].Key != "plain" {
		t.Fatalf("plaintext tokens not loaded: %+v", cfg.Tokens)
	}
}

// TestLoadEnvFileDecryptsEncrypted proves the altitude fix: an age-encrypted
// .env include is decrypted before its KEY=VALUE lines are parsed.
func TestLoadEnvFileDecryptsEncrypted(t *testing.T) {
	kr, id := makeKeyringForTest(t)
	const envName = "DUCTILE_TEST_ENC_ENV_VALUE"
	t.Cleanup(func() { _ = os.Unsetenv(envName) })

	plaintext := []byte(envName + "=decrypted-env-secret\n")
	ciphertext, err := secrets.Encrypt(plaintext, []age.Recipient{id.Recipient()})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	path := filepath.Join(t.TempDir(), "secret.env")
	if err := os.WriteFile(path, ciphertext, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := loadEnvFile(path, kr); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv(envName); got != "decrypted-env-secret" {
		t.Fatalf("env %s = %q, want decrypted-env-secret", envName, got)
	}
}

func TestResolveKeyringFromEnv(t *testing.T) {
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	keyPath := filepath.Join(t.TempDir(), "age.key")
	if err := os.WriteFile(keyPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Setenv(ageKeyEnvVar, keyPath)

	kr, err := resolveKeyring(t.TempDir(), &Config{})
	if err != nil {
		t.Fatalf("resolveKeyring: %v", err)
	}
	if kr.Empty() {
		t.Fatal("keyring empty; env key should have loaded")
	}
}

func TestResolveKeyringNoneConfiguredIsEmpty(t *testing.T) {
	// No env var, no config field, no default file → empty keyring, no error.
	t.Setenv(ageKeyEnvVar, "")
	kr, err := resolveKeyring(t.TempDir(), &Config{})
	if err != nil {
		t.Fatalf("resolveKeyring: %v", err)
	}
	if !kr.Empty() {
		t.Fatal("expected empty keyring when nothing configured")
	}
}

func TestResolveKeyringExplicitMissingFails(t *testing.T) {
	t.Setenv(ageKeyEnvVar, filepath.Join(t.TempDir(), "absent.key"))
	if _, err := resolveKeyring(t.TempDir(), &Config{}); err == nil {
		t.Fatal("explicitly-named missing key should fail, not be ignored")
	}
}
