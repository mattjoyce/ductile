---
id: 25
status: done
priority: Normal
blocked_by: [12]
tags: [vault, attestation, security, observability]
---

### DONE (2026-06-03, commit 86ba00f), full suite green
- A compose-time fingerprint mismatch now escalates over **three channels**, distinct from a
  benign denial: (1) vault_audit `Op=fingerprint_mismatch, Outcome=security_alert` (vs benign
  `compose_denial/denied`); (2) a SECURITY-marked Error log with `event=plugin.fingerprint_mismatch`;
  (3) a live `plugin.fingerprint_mismatch` hub event on the existing SSE channel (`system watch`).
- **Owner = operator** via those three; external alerting wires onto the SSE event or the audit
  query. Built-in webhook push declined (operator call).
- Pure `composeFailureEscalation(err)` classifier (errors.Is on `ErrFingerprintMismatch`) split from
  the effects; `executeJob` integration test proves audit row + hub event + fail-closed, and that a
  revoked principal stays on the quiet path. The orphan `DenialFingerprintMismatch` reason now has
  both a producer (§3.3) and a distinct consumer (this).
- PRD: `~/.claude/MEMORY/WORK/20260603-074500_vault-25-escalate-fingerprint-mismatch/PRD.md` (12/12).

# Vault · escalate a fingerprint mismatch as a security event (not a quiet denial)

Three Compose/spawn outcomes must not look alike (Hickey-Armstrong §3.4): (1) principal legitimately has
no grants, (2) `secret_ref` names a missing/revoked secret (config error), (3) **fingerprint mismatch —
a possible plugin swap, a SECURITY event**. The `{secrets, denials}` shape carries the *distinction*, but
nothing *acts* on (3) differently: `DenialFingerprintMismatch` is defined (`internal/vault/errors.go:21-25`)
**with no producer**, and #11 records a `compose_denial` fact at the same quiet level as benign denials.

**Scope:**
- Once #12 §3.3 produces a real mismatch verdict, route it to a loud, escalating path — distinct
  log level/channel + the existing `vault_audit` `compose_denial` row — separate from "no grants."
- Define who is alerted on a swap and how.

**Acceptance:** a fingerprint mismatch at compose/spawn escalates loudly (distinct from a benign denial),
is recorded, and has a defined owner/alert; the orphan `DenialFingerprintMismatch` reason gains its producer.

## Narrative
- **Source:** Hickey-Armstrong (Design + Branch) §3.4/§3.1; Lamport notes the producerless denial reason.
- **Not covered by:** #5 (built the typed-denial vocabulary), #11 (records the fact), #12 §3.3 (produces the
  verdict) — none card the *escalation/alerting*. Best sequenced as a slice of, or right after, #12 §3.3.
