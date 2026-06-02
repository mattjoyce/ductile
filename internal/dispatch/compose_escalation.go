package dispatch

import "errors"

// eventPluginFingerprintMismatch is the hub/SSE event type and the structured-log
// `event` value for a compose-time plugin fingerprint mismatch. Operators (and
// any SSE subscriber, e.g. `system watch`) match on this to alert on a possible
// plugin swap.
const eventPluginFingerprintMismatch = "plugin.fingerprint_mismatch"

// composeFailureEscalation classifies a fail-closed secret-composition error for
// audit and alerting. A fingerprint mismatch means a plugin's live bytes no longer
// match its attestation — a possible swap, and therefore a SECURITY event that
// must be escalated distinctly from a benign authorization denial (e.g. a revoked
// principal). It returns the audit Op + Outcome to record and whether the failure
// is a security event the caller should escalate loudly.
func composeFailureEscalation(err error) (op, outcome string, security bool) {
	if errors.Is(err, ErrFingerprintMismatch) {
		return "fingerprint_mismatch", "security_alert", true
	}
	return "compose_denial", "denied", false
}
