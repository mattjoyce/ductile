---
id: 130
status: done
priority: High
tags: [vault, bootstrap, reload, posture, observability]
---

# Activate-on-reload + make the boot posture first-class & observable

> **DONE (branch `feat/129-vault-operable-posture`).**
> - **Activation:** reload (buildRuntime swap) transitions management→gateway through the normal #94
>   fail-closed seam once the minted token resolves; management socket torn down. Trigger in the
>   management posture is **SIGHUP** (the `/system/reload` route isn't on the management surface — deploy
>   tooling sends it). Test: `cmd/ductile/management_posture_activation_test.go`.
> - **Observability (live):** posture is surfaced in `/healthz` on BOTH surfaces (gateway TCP + mgmt
>   socket) via `api.Config.BootPosture`; `system status` probes it (`checkBootPosture`/`probeLivePosture`)
>   and reports the LIVE posture — not a config guess that could lie about a stuck daemon. Named
>   `boot_posture` to avoid colliding with the #111/#112 trust/deployment postures.
> - **Anti-strand:** boot Warn log (from #129) + the live `/healthz` posture + `system status` line make a
>   daemon stuck pre-activation visibly *that*.
>
> Remaining for the ladder: acceptance test + docs reconcile = [[131]].

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
