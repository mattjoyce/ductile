package dispatch

import (
	"errors"
	"io"
	"log/slog"
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
	got, err := composePluginSecrets(nil, "any", discardLogger())
	if err != nil {
		t.Fatalf("nil composer: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("nil composer: expected nil secrets, got %v", got)
	}
}

func TestComposePluginSecretsUnknownPrincipalDeliversNothing(t *testing.T) {
	fc := &fakeComposer{err: vault.ErrUnknownPrincipal}
	got, err := composePluginSecrets(fc, "not-in-vault", discardLogger())
	if err != nil {
		t.Fatalf("unknown principal must not error (vault is opt-in): %v", err)
	}
	if got != nil {
		t.Fatalf("unknown principal: expected nil secrets, got %v", got)
	}
}

func TestComposePluginSecretsRevokedPrincipalFailsClosed(t *testing.T) {
	fc := &fakeComposer{err: vault.ErrPrincipalInactive}
	got, err := composePluginSecrets(fc, "revoked", discardLogger())
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
	got, err := composePluginSecrets(fc, "mailer", discardLogger())
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
	got, err := composePluginSecrets(fc, "mailer", discardLogger())
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
	got, err := composePluginSecrets(fc, "mailer", discardLogger())
	if err == nil {
		t.Fatalf("unexpected Compose error must fail closed, got nil")
	}
	if got != nil {
		t.Fatalf("expected no secrets on error, got %v", got)
	}
}
