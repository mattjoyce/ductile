---
id: 12
status: done
priority: Normal
blocked_by: [5]
tags: [vault, rung4, attestation, deferred]
---

# Rung 4 · attestation upgrade (keyed-nonce + per-plugin lock)

Strengthen plugin identity verification before composing secrets.

**Scope:**
- Keyed-nonce fingerprints + decoupled per-plugin lock, per the **`Ductile - Plugin Attestation and Keyed Fingerprints.md`** ADR.
- The vault verifies plugin identity (keyed-nonce; `core` holds the nonce) before `Compose`.
- **Lands with privsep.** Rung 1–3 ride the **existing plain-fingerprint** check.

**Acceptance:** Compose rejects a plugin whose keyed-nonce fingerprint doesn't verify; existing plain-fingerprint path remains until this lands.

## Narrative
- **Source:** handoff §"Build sequence — Rung 4"; ADR `Ductile - Plugin Attestation and Keyed Fingerprints.md` (the integrity dependency).
- Deferred — **not Rung 1**; sequenced with privsep (explicitly out of current scope).

### In progress (2026-06-02)
- **Operator decision (captain's call, DIVERGES from ADR §3.2/§4):** keyed nonce is **vault-held or
  fail** — always keyed, NO plain-BLAKE3 fallback, NO config-resident nonce. One source / one mode /
  one path (Hickey); fail-closed on missing nonce, no silent downgrade (Armstrong). Accepted cost:
  plugin attestation now requires the vault (age key = boot dependency when fingerprints exist).
  Boundary: zero-fingerprint deployments still boot vault-less. **ADR needs a one-line amendment to
  retire the plain fallback (offer pending — owner=Matt, status: proposed).**
- **Landed (commit bf6e17b) — §3.2 crypto foundations, TDD:**
  - `vault.FingerprintNonce()` — fail-closed 32-byte nonce accessor off the `core` principal.
  - `config.ComputeKeyedBlake3Hash(path,key)` — keyed BLAKE3 (`blake3.NewKeyed`); plain
    `ComputeBlake3Hash` (config-file surface) left untouched.
- **§3.2 keyed-nonce attestation — DONE (commit 9db2b90), full suite green:**
  `ComputePluginFingerprint(rp, nonce)` keyed; `GenerateChecksumsWithPlugins`/`VerifyPluginFingerprints`
  thread the nonce; verify **fails closed** when fingerprints exist but the nonce is absent/short
  (downgrade guard, new test). CLI `fingerprintNonceForConfig` loads the vault for the nonce (fail-closed
  when none); lock sources it only when plugins exist, verify whenever fingerprints exist — which
  decrypts the vault on demand, so **load-ordering is handled without reordering boot**. Wiring tests
  seed a real age key + `vault.Init`.
- **Remaining for #12:** ~~§3.1~~ ~~§3.3~~ — both DONE (see below). §3.2 landed earlier
  (9db2b90/bf6e17b). **#12 is now complete.**

### §3.3 — DONE (2026-06-03, commit 877dff7), full suite + security-review green
- **The missing producer for `DenialFingerprintMismatch`.** Compose-time re-verification:
  a vault principal's live bytes are re-hashed (keyed) against its recorded fingerprint
  right before its secrets are delivered; mismatch fails the spawn closed → closes the
  runtime-swap window for the secret path.
- **Gate in dispatch, vault stays pure** (`Store.Compose` unchanged): new
  `dispatch.PluginVerifier` injected via `WithPluginVerifier`, gated in
  `composePluginSecrets` AFTER principal-ness, BEFORE delivery. Non-principals never verified.
- **`config.VerifyResolvedPluginFingerprint`** (focused per-plugin keyed re-hash + compare,
  reuses `ComputePluginFingerprint`). Runtime adapter `pluginIdentityVerifier` reads
  `.checksums` fresh, resolves bytes via the registry, keys with the vault nonce; wired only
  when the vault loads.
- **Mismatch → `ErrFingerprintMismatch`** (text == `fingerprint_mismatch`) flows through the
  existing dispatcher fail-closed path → `compose_denial` audit fact carries the reason;
  `errors.Is` lets **#25** branch for loud escalation.
- **Operator consequence (flag for review):** a vault principal with no recorded fingerprint
  is denied → using the vault for secrets now effectively requires `ductile plugin lock`.
- **Security review:** fail-closed, no unkeyed downgrade, no typed-nil, no secret leak. Known
  residual: sub-ms verify→exec TOCTOU (was "since last reload"); full closure needs fd-hashing.
- PRD: `~/.claude/MEMORY/WORK/20260603-071500_vault-12-33-compose-reverify/PRD.md` (26/26).

### §3.1 — DONE (2026-06-03, commit ccea759), full suite + race-shuffle green, binary-verified
- **`config lock` decoupled:** writes config-file hashes only; PRESERVES recorded
  `plugin_fingerprints` for still-configured plugins, PRUNES de-configured ones. Never re-hashes
  plugin bytes → **Threat A closed** (a routine lock can't bless a swapped binary). Config-only path
  needs no vault. (`PreservePluginFingerprints`, `GenerateChecksumsWithFingerprints`.)
- **New `ductile plugin lock`** (the explicit, per-plugin attest; only producer of plugin_fingerprints):
  - `plugin lock <name>` — keyed re-hash of one plugin, merged in; leaves others untouched (ISC-A3);
    errors on not-configured / not-discoverable; fails closed without a vault.
  - `plugin lock --all` — previews changed/new plugins + a 5-char confirm code (first 5 chars of the
    proposed-set hash → bound to the bytes, self-invalidates on drift = TOCTOU guard); writes nothing.
  - `plugin lock --all <code>` — commits iff the code still matches; also prunes de-configured.
- **Operator decisions (this session):** `--all` with the diff+code gate (not per-plugin only);
  config lock AND `--all` prune de-configured entries; code = first 5 alphanum of the new set hash.
- **Verify guidance retargeted:** re-attestation remedies → `plugin lock <name>`; stale-entry remedy
  stays `config lock` (it prunes). Wiring tests migrated to a `config lock` + `plugin lock` two-step.
- **Bug found via binary smoke + fixed:** stdlib `flag` dropped a trailing `--config-dir` (stopped at
  the first positional); now uses the existing `parseFlagsAndPositionals` so flags work in any order.
- PRD: `~/.claude/MEMORY/WORK/20260603-060637_vault-12-31-plugin-lock-decouple/PRD.md` (29/29).
- **Branch-review flag (2026-06-02):** §3.3 is the missing PRODUCER for `DenialFingerprintMismatch`
  (`internal/vault/errors.go:21-25` is defined but nothing emits it). Three reviewers rank compose-time
  re-attestation top-tier; until it lands the Attestation ADR §3.3 *overclaims* (a binary swapped after the
  last reload still receives secrets at next spawn). Treat §3.3 as security-gating; the escalation/alerting
  of a mismatch is carded separately as #25.
