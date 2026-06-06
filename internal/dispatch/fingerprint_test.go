package dispatch

import (
	"errors"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// fakeFingerprintVerifier fails attestation for the named plugins, passes for the rest.
type fakeFingerprintVerifier struct{ fail map[string]bool }

func (f fakeFingerprintVerifier) VerifyIdentity(plugin string) error {
	if f.fail[plugin] {
		return errors.New("fingerprint mismatch")
	}
	return nil
}

func TestBindAccountToFingerprint(t *testing.T) {
	cfg := &config.Config{
		Accounts: map[string]config.AccountConf{
			"default":   {UID: 1001, GID: 1001, StateDir: "/w/default"},
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/w/untrusted"},
		},
	}
	grantedDefault := ResolvedAccount{Name: "default", UID: 1001, GID: 1001, StateDir: "/w/default", Confined: true, Source: AccountGranted}

	t.Run("matching fingerprint keeps the grant", func(t *testing.T) {
		got, err := bindAccountToFingerprint(grantedDefault, cfg, "ok-plugin", fakeFingerprintVerifier{})
		if err != nil || got != grantedDefault {
			t.Fatalf("expected grant kept, got %+v err=%v", got, err)
		}
	})

	t.Run("mismatch downgrades a trusted grant to the most-restricted tier", func(t *testing.T) {
		v := fakeFingerprintVerifier{fail: map[string]bool{"swapped": true}}
		got, err := bindAccountToFingerprint(grantedDefault, cfg, "swapped", v)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "untrusted" || got.Source != AccountDowngraded || got.UID != 1002 {
			t.Fatalf("expected downgrade to untrusted, got %+v", got)
		}
	})

	t.Run("mismatch with no most-restricted tier fails closed (terminal)", func(t *testing.T) {
		noUntrusted := &config.Config{Accounts: map[string]config.AccountConf{
			"default": {UID: 1001, GID: 1001, StateDir: "/w/default"},
		}}
		_, err := bindAccountToFingerprint(grantedDefault, noUntrusted, "swapped", fakeFingerprintVerifier{fail: map[string]bool{"swapped": true}})
		if !errors.Is(err, ErrAccountDropFailed) {
			t.Fatalf("expected ErrAccountDropFailed (terminal), got %v", err)
		}
	})

	t.Run("unconfined passes through untouched (no attestation)", func(t *testing.T) {
		un := ResolvedAccount{Source: AccountUnconfined}
		got, err := bindAccountToFingerprint(un, cfg, "anything", fakeFingerprintVerifier{fail: map[string]bool{"anything": true}})
		if err != nil || got != un {
			t.Fatalf("unconfined must be untouched, got %+v err=%v", got, err)
		}
	})

	t.Run("nil verifier (attestation not wired) keeps the grant", func(t *testing.T) {
		got, err := bindAccountToFingerprint(grantedDefault, cfg, "swapped", nil)
		if err != nil || got != grantedDefault {
			t.Fatalf("nil verifier must keep grant, got %+v err=%v", got, err)
		}
	})
}
