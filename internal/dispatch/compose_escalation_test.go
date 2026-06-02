package dispatch

import (
	"errors"
	"fmt"
	"testing"
)

// composeFailureEscalation classifies a fail-closed compose error for audit +
// alerting: a fingerprint mismatch (a possible plugin swap) is a SECURITY event,
// escalated distinctly from a benign authorization denial (#25).
func TestComposeFailureEscalationFingerprintMismatchIsSecurity(t *testing.T) {
	// Wrapped exactly as composePluginSecrets wraps it.
	err := fmt.Errorf("%w: plugin %q: entrypoint hash mismatch", ErrFingerprintMismatch, "mailer")
	op, outcome, security := composeFailureEscalation(err)
	if !security {
		t.Fatal("a fingerprint mismatch must classify as a security event")
	}
	if op != "fingerprint_mismatch" {
		t.Fatalf("op = %q, want fingerprint_mismatch", op)
	}
	if outcome != "security_alert" {
		t.Fatalf("outcome = %q, want security_alert", outcome)
	}
}

func TestComposeFailureEscalationNestedMismatchStillSecurity(t *testing.T) {
	// Double-wrapped: must match via errors.Is, not ==.
	inner := fmt.Errorf("%w: bytes changed", ErrFingerprintMismatch)
	err := fmt.Errorf("secret composition failed: %w", inner)
	_, _, security := composeFailureEscalation(err)
	if !security {
		t.Fatal("a nested fingerprint mismatch must still classify as security (errors.Is)")
	}
}

func TestComposeFailureEscalationBenignDenialIsQuiet(t *testing.T) {
	op, outcome, security := composeFailureEscalation(errors.New("vault: principal is not active"))
	if security {
		t.Fatal("a benign denial must NOT be a security event")
	}
	if op != "compose_denial" || outcome != "denied" {
		t.Fatalf("benign denial got op=%q outcome=%q, want compose_denial/denied", op, outcome)
	}
}
