---
id: 130
status: todo
priority: High
tags: [vault, bootstrap, reload, posture, observability]
---

# Activate-on-reload + make the boot posture first-class & observable

> **Nav:** child of [[128-vault-native-bootstrap-no-offline-seed]]; design root
> [[../docs/adr/vault-credential-ladder]]. Depends on [[129-vault-operable-boot-posture]].
> Posture vocabulary aligns with [[111-root-gateway-halfway-tier-nopasswd-check]] and
> [[112-deployment-postures-doc]].

## Problem
Once the daemon is in the vault-operable / ductile-closed posture ([[129]]) and the admin token has
minted the api token, the gateway plane must come up — and the in-between state must be a **first-class,
observable posture**, not an accidental half-booted condition a bug can strand (threat #3 in the ADR).

## Do
1. **Activation:** `ductile system reload` re-resolves `api.auth.tokens` (now present) and brings up the
   public gateway listener through the existing fail-closed seam (`runtime.go:725`, `ResolveAPITokens`).
   No new bypass — activation IS the normal #94 boot path succeeding because the secret now resolves.
2. **Observability:** surface the posture in `system status` / `/system/doctor` / `selfcheck` —
   distinguish **pre-activation (management-only)** from **activated (gateway serving)**. Reuse the
   deployment-posture vocabulary from #111/#112 rather than inventing a new term.
3. **Anti-strand:** make the pre-activation posture intentional and logged at boot (a clear log line +
   a status field), so an operator/AI can tell "waiting for api token" apart from "wedged."

## Done when
- After minting the api token in the management posture, `system reload` activates the gateway plane and
  it serves authenticated requests.
- `system status` (and doctor/selfcheck) clearly report which posture the gateway is in.
- A gateway stuck pre-activation is visibly *that*, not a silent failure.
