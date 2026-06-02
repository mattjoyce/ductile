package dispatch

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/vault"
)

// fakeComposer is a test double for SecretComposer. It returns a fixed
// composition/error regardless of the principal asked for.
type fakeComposer struct {
	comp vault.Composition
	err  error
	got  string // records the principal it was asked to compose for
}

func (f *fakeComposer) Compose(principal string) (vault.Composition, error) {
	f.got = principal
	return f.comp, f.err
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Contract (registration = authorization only; identity is the registry/.checksums):
//   - composer nil         -> no secrets, no error (vault not wired)
//   - unknown principal    -> no secrets, no error (plugin opted out of the vault)
//   - registered + revoked -> error (fail closed; explicit revocation stops the spawn)
//   - registered + active  -> the composed secrets; denials are logged, not fatal

func TestComposePluginSecretsNilComposerDeliversNothing(t *testing.T) {
	got, err := composePluginSecrets(nil, nil, "any", discardLogger())
	if err != nil {
		t.Fatalf("nil composer: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("nil composer: expected nil secrets, got %v", got)
	}
}

func TestComposePluginSecretsUnknownPrincipalDeliversNothing(t *testing.T) {
	fc := &fakeComposer{err: vault.ErrUnknownPrincipal}
	got, err := composePluginSecrets(fc, nil, "not-in-vault", discardLogger())
	if err != nil {
		t.Fatalf("unknown principal must not error (vault is opt-in): %v", err)
	}
	if got != nil {
		t.Fatalf("unknown principal: expected nil secrets, got %v", got)
	}
}

func TestComposePluginSecretsRevokedPrincipalFailsClosed(t *testing.T) {
	fc := &fakeComposer{err: vault.ErrPrincipalInactive}
	got, err := composePluginSecrets(fc, nil, "revoked", discardLogger())
	if err == nil {
		t.Fatalf("revoked principal must fail closed, got nil error")
	}
	if !errors.Is(err, vault.ErrPrincipalInactive) {
		t.Fatalf("expected ErrPrincipalInactive, got %v", err)
	}
	if got != nil {
		t.Fatalf("revoked principal: must deliver no secrets, got %v", got)
	}
}

func TestComposePluginSecretsActivePrincipalDeliversComposed(t *testing.T) {
	fc := &fakeComposer{comp: vault.Composition{
		Secrets: map[string]string{"API_KEY": "v1", "DB_URL": "v2"},
	}}
	got, err := composePluginSecrets(fc, nil, "mailer", discardLogger())
	if err != nil {
		t.Fatalf("active principal: unexpected error: %v", err)
	}
	if fc.got != "mailer" {
		t.Fatalf("expected Compose called with plugin name as principal, got %q", fc.got)
	}
	if len(got) != 2 || got["API_KEY"] != "v1" || got["DB_URL"] != "v2" {
		t.Fatalf("expected composed secrets delivered verbatim, got %v", got)
	}
}

func TestComposePluginSecretsDeliversDespiteDenials(t *testing.T) {
	fc := &fakeComposer{comp: vault.Composition{
		Secrets: map[string]string{"API_KEY": "v1"},
		Denials: []vault.Denial{{Secret: "OLD_KEY", Reason: vault.DenialSecretRevoked}},
	}}
	got, err := composePluginSecrets(fc, nil, "mailer", discardLogger())
	if err != nil {
		t.Fatalf("denials must not be fatal: %v", err)
	}
	if len(got) != 1 || got["API_KEY"] != "v1" {
		t.Fatalf("expected active secret delivered alongside denial, got %v", got)
	}
}

// A generic, non-vocabulary error from Compose must fail closed rather than
// silently delivering nothing — only unknown-principal is a benign opt-out.
func TestComposePluginSecretsUnexpectedErrorFailsClosed(t *testing.T) {
	fc := &fakeComposer{err: errors.New("store corrupt")}
	got, err := composePluginSecrets(fc, nil, "mailer", discardLogger())
	if err == nil {
		t.Fatalf("unexpected Compose error must fail closed, got nil")
	}
	if got != nil {
		t.Fatalf("expected no secrets on error, got %v", got)
	}
}

// fakeVerifier is a test double for PluginVerifier. It records the plugin it was
// asked about and returns a fixed error.
type fakeVerifier struct {
	err    error
	called string
}

func (f *fakeVerifier) VerifyIdentity(plugin string) error {
	f.called = plugin
	return f.err
}

// §3.3: a vault principal whose live bytes fail re-verification must fail closed
// (no secrets) with a fingerprint_mismatch signal — the swapped binary is caught
// right before its secrets would be handed over.
func TestComposePluginSecretsFingerprintMismatchFailsClosed(t *testing.T) {
	fc := &fakeComposer{comp: vault.Composition{Secrets: map[string]string{"API_KEY": "v1"}}}
	fv := &fakeVerifier{err: errors.New("entrypoint hash mismatch at /p/x")}

	got, err := composePluginSecrets(fc, fv, "mailer", discardLogger())
	if err == nil {
		t.Fatal("fingerprint mismatch must fail closed")
	}
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("error must wrap ErrFingerprintMismatch for #25/audit branching: %v", err)
	}
	if !strings.Contains(err.Error(), "fingerprint_mismatch") {
		t.Fatalf("error/audit detail must carry the reason: %v", err)
	}
	if got != nil {
		t.Fatalf("no secrets may be delivered on mismatch, got %v", got)
	}
	if fv.called != "mailer" {
		t.Fatalf("verifier must be asked about the spawning plugin, got %q", fv.called)
	}
}

// A passing verifier delivers exactly the composed secrets (identity gate is
// transparent on success).
func TestComposePluginSecretsVerifierPassDelivers(t *testing.T) {
	fc := &fakeComposer{comp: vault.Composition{Secrets: map[string]string{"API_KEY": "v1"}}}
	fv := &fakeVerifier{err: nil}

	got, err := composePluginSecrets(fc, fv, "mailer", discardLogger())
	if err != nil {
		t.Fatalf("passing verifier must not block delivery: %v", err)
	}
	if len(got) != 1 || got["API_KEY"] != "v1" {
		t.Fatalf("expected composed secrets delivered, got %v", got)
	}
}

// ISC-A1: an opt-out plugin (not a vault principal) is NEVER verified — identity
// attestation gates the secret path, and there are no secrets here.
func TestComposePluginSecretsUnknownPrincipalSkipsVerifier(t *testing.T) {
	fc := &fakeComposer{err: vault.ErrUnknownPrincipal}
	fv := &fakeVerifier{err: errors.New("would fail if called")}

	got, err := composePluginSecrets(fc, fv, "not-in-vault", discardLogger())
	if err != nil || got != nil {
		t.Fatalf("opt-out plugin must deliver nothing without error: got=%v err=%v", got, err)
	}
	if fv.called != "" {
		t.Fatalf("verifier must NOT run for a non-principal, but was called for %q", fv.called)
	}
}
