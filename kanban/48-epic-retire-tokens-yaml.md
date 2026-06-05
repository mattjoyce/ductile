---
id: 48
status: doing
priority: Normal
blocked_by: [9, 14, 68]
tags: [vault, epic, back-compat, decomplect, tokens-yaml]
---

## Progress (2026-06-05) — blockers [9,14,68] all DONE; epic unblocked, execution started

**Slice 1 — parity-verify tool: DONE + tested.** Added `ductile vault import --verify` (read-only,
skips the PID lock, mutates nothing, repeatable while the daemon serves). Pure classifier
`config.VerifyTokenParity` (`internal/config/vault_import.go`) reuses `PlanTokenImport`'s literal-vs-${ENV}
split, then per entry vs the live vault yields: **match** / **vault-only** (env-pointer superseded by an
active vault value — green) / **missing** (resolvable in tokens.yaml, absent in vault) / **drift** (both
present & differ — the idempotency guard: a since-rolled value is NEVER clobbered) / **unresolved**
(${ENV} with no vault value — forces an explicit decision, no silent freeze). Exit 0 only when green; else
exit 2 so a cutover script gates on it. Unit test covers all five verdicts + revoked-not-counted + sorted
output (`go test ./internal/config` green).

**Slice 2 — single-decrypt (snapshot-at-reload, per Matt's call): DONE + tested.** Chose the Hickey-honest
design: instead of stashing a live owner on the `Config` value (complecting a value with a process object),
**thread the owner explicitly**. The load-time graft (`graftVaultSecrets`) now returns the owner it already
decrypts; `config.LoadWithVault` surfaces it; the daemon-start path (`runStart`) passes it via
`runtimeBuildOptions.vaultOwner`, and `buildRuntime` reuses it instead of a second `LoadVault` decrypt
(#43 redundant decrypt killed on the start path). **Default-safe:** a nil opts owner — reload, restore,
keyless, no-vault — falls back to the exact prior `LoadVault`, so reload + all CLIs + boot validation are
byte-for-byte unchanged (the graft still populates `cfg.Tokens` before `ValidateCrossReferences`, so the
armed `validate_config_on_boot` gate sees vault-only refs exactly as before). Removed the now-redundant
`vaultStore` helper. Tests: `TestGraftVaultSecretsReturnsOwner` + existing graft/keyless suite green;
`go build/vet/golangci-lint ./...` clean. NOTE: resolvers still read `cfg.Tokens` (graft-populated) —
fully pointing them at the owner + deleting the graft is slice 3.

Drive-by: fixed one pre-existing repo-wide lint failure (`internal/state/stopwatch_query.go` unchecked
`rows.Close`) so the premerge `golangci-lint run ./...` gate is green.

**Slice 3 — demolish (destructive): GATED on explicit go-ahead.** Unchanged.

# Retire `tokens.yaml` — kill the graft, resolve against the live vault (EPIC)

**Origin (2026-06-05 design session):** while talking through [[43-vault-single-load-thread-nonce-boot]]
we realised most of the vault's residual complexity is the **`tokens.yaml` back-compat tax**, not the
vault itself. The clean test for back-compat: *does it have a reason to exist after the bridge is gone?*
The graft, `cfg.Tokens`, the freshness asymmetry, the fail-open question, the misnamed table — and even
`ductile vault import` — all fail that test. They are scaffolding to be demolished, not standing surface.

**End-state:** every `secret_ref` resolves against the single live vault owner (the one in-memory object,
`runtime.go:589`, already serving plugins via `Compose`). No copied table, no second decrypt, no
load-time freeze.

## What dies when the bridge goes (the back-compat column)
- The graft (`graftVaultSecrets` / `mergeVaultSecrets`) and the `cfg.Tokens` table.
- The graft's redundant decrypt (the back-compat half of [[43-vault-single-load-thread-nonce-boot]]).
- The webhook/relay **freshness asymmetry** (roll-needs-reload) — see [[45-docs-jun4-branch-review-corrections]] / Ousterhout §2.
- The [[41-vault-graft-fail-open-unresolvable-principal]] fail-open question (no graft → no question).
- `ductile vault import` itself — a one-way ratchet, removed *with* the bridge.
- The deferred `cfg.Tokens → resolvedSecrets` rename (#9 final note).

## Slices (tracer-bullet)
1. **Verify/cutover migration tool** (build on #9's `import`). Harden `import` into a one-time tool that
   not only imports but **proves parity**: for every `tokens.yaml` entry, assert the vault yields the same
   resolved value (now possible via [[42-vault-get-reserved-refusal-and-audit]]'s read path). Must:
   force an explicit per-entry decision on `${ENV}` indirections (don't silently freeze); be idempotent
   (never re-clobber a since-rolled value); offer a safe dry-run/verify mode runnable repeatedly *before*
   any destructive step; resolve same-name collisions definitively. **Built to be thrown away.**
2. **Flip the resolvers.** Point the load-time `secret_ref` consumers (webhook/relay) at the live vault
   owner instead of `cfg.Tokens`. Parity proven by slice 1's tool. Removes the freeze/freshness asymmetry.
3. **Demolish (destructive).** In one commit: delete the graft, `cfg.Tokens`, `tokens.yaml` support, the
   `import` command, and rename `→ resolvedSecrets`. Everything in the back-compat column dies together.

**Note:** `import` and the slice-1 tool are the *same lifecycle object* — born, used once, removed in
slice 3. Do not invest in `import` as permanent surface (no deep doc/skill coverage); carry a sunset
marker so it's never mistaken for load-bearing.

**Acceptance:** a fresh deploy with no `tokens.yaml` resolves every `secret_ref` from the vault; the
migration tool reports green parity on a real `tokens.yaml`; `grep -r tokens.yaml` finds only history.

**Sequencing (2026-06-05):** this is the FINAL step, after all three instances are on the vault path —
[[66-redeploy-thinkpad-after-65-fix]] → [[67-deploy-vault-branch-macm1]] → [[68-deploy-vault-branch-unraid]].
All three already import their tokens.yaml into the vault with proven parity (Thinkpad: 6/6), and each
logs the 6 "in both vault and tokens.yaml — remove the entry" graft warnings on every op. Killing the
graft + tokens.yaml clears that cruft. Do not start until #68 is done.

## Hickey × Armstrong review (2026-06-05)

Design review of this epic through structure-at-rest (Hickey) and behaviour-under-fault (Armstrong).
The epic is sound and correctly scoped to *demolish, not execute yet*. Three concrete additions:

**Hickey — the essential move is killing the copy.** The same vault blob is decrypted twice into two
independently-shaped containers: `graftVaultSecrets` (`loader.go:114`) → flat `cfg.Tokens`, *frozen at
load*; and `config.LoadVault` (`runtime.go:589`) → the live `vaultOwner`. That is two copies of one value
with no single source of truth at the point of use — the webhook reads the frozen copy (`runtime.go:686`),
the plugin reads the live one (`Compose`). The freshness asymmetry is the symptom; the duplicated value is
the disease. Everything in `vault_secrets.go` (`activeVaultSecrets`, `mergeVaultSecrets`,
`pluginScopedSecret`, `unregisteredGrantee`, the blast-radius warning, `vaultBlind`) is the *cost of the
copy*, not essential to "resolve a secret_ref" — slice 2 makes it all evaporate. The graft's own comment
("without touching a single resolver") is the tell: easy slid into complex.

**Armstrong — slice 2 introduces the only genuinely new failure mode.** The epic *reduces* failure
surfaces (one decrypt, one owner, already supervised by the fail-closed boot path) — good. But the message
contract for webhook/relay changes shape: today they get a *frozen `map[string]string`* once at
construction (`relay/config.go:149`, `runtime.go:686`) and an HMAC check *cannot* fail "secret vanished".
After slice 2 they ask the *live* owner, which *can* fail at request time (revoke-mid-flight, mid-rotation,
owner lock held by a long write), and webhook/plugin resolution stop failing independently — they share the
owner. **Action (slice 2):** specify the read contract (cheap live read vs. snapshot-at-reload vs.
snapshot-per-request; `Snapshot()` is a deep copy under lock — is it on the webhook hot path now?) and name
the request-time-miss supervisor/fallback.

**`${ENV}` parity trap — implicit state masquerading as data.** tokens.yaml `${ENV}` indirections resolve
at load against *the daemon's environment*; the vault holds a *frozen value*. So slice-1 "parity" is true
only at the import host, at import time — and the three instances are different machines. **Action (slice
1):** state the rule as "refuse to silently freeze an `${ENV}` — the environment is an uncontracted,
host-local input"; parity is host-and-time-scoped, not absolute.

**Demolition gate is a note, not an invariant.** "Do not start until #68 is done" is supervised by Matt
remembering. **Action (slice 3):** have slice 1's tool emit a per-host green-parity attestation and make
slice 3 *require* all three before it will demolish — turn the sequencing note into a checked precondition.
