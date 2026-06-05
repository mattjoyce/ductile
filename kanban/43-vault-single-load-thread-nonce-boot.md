---
id: 43
status: todo
priority: Normal
blocked_by: []
tags: [vault, boot, toctou, attestation, efficiency, branch-review]
---

### Update (2026-06-06) — partially overtaken by events
- **Back-compat half: DONE (evaporated).** The graft's extra decrypt is gone — #48 slice 3a
  (`0baf11e`) removed `graftVaultSecrets`/`cfg.Tokens`, exactly as predicted below.
- **Reload-path regression: FIXED (`38e1e77`).** The reload path had regressed to a double-decrypt
  (owner-less `config.Load` then a second `LoadVault` in `buildRuntime`); now threads the
  `LoadWithVault` owner through `runtimeBuildOptions.vaultOwner`.
- **Intrinsic half: STILL OPEN.** The boot fingerprint-verify TOCTOU remains —
  `verifyPluginFingerprintsForConfig`/`fingerprintNonceForConfig` re-load + re-decrypt for the nonce
  instead of reusing the one boot owner. Now also tracked as item 1 of [[73-tokens-yaml-retirement-branch-review-punchlist]].

### DEFERRED to its own PR (2026-06-05, operator decision)
Kept out of the 40/41/42 should-fix batch deliberately: this is a cross-cutting refactor
of the critical startup+attestation path (decrypt happens in `config.Load`'s graft, in
`fingerprintNonceForConfig`, and in the runtime owner load — threading one snapshot+nonce
through all three changes signatures up and down boot). It deserves isolation and its own
boot-ordering reasoning rather than riding alongside small security fixes. Still valid;
still should-fix.

### This card is TWO fixes wearing one hat (2026-06-05 design session)
- **Back-compat half** — the graft's extra decrypt. This **evaporates** when
  [[48-epic-retire-tokens-yaml]] removes the graft; don't engineer around it, just let the
  epic delete it.
- **Intrinsic half** — the TOCTOU between the boot fingerprint-verify (re-opens the blob for
  the nonce) and the runtime owner (`runtime.go:589`). Pure startup *ordering*: load the one
  owner first, take the nonce from it, then verify + deliver on the **same snapshot**. This
  stands on its own regardless of `tokens.yaml`.
- Steady-state is already what we want: one in-memory `Vault` owner behind a `sync.RWMutex`,
  handed to the dispatcher / attestation / API for the binary's life. #43 only makes *boot*
  live up to that model.

# Vault · decrypt the blob once at boot and thread the nonce to all consumers

**From the 2026-06-04 branch review (Hickey-Armstrong Rev2 §1.1).** Boot still decrypts the age
blob through **independent paths**: the `config.Load` graft, `verifyPluginFingerprintsForConfig`,
and the runtime owner each decrypt/read separately
(`loader.go:114`, `config_manage.go:1012,1056`, `runtime.go:589`). With keyed-nonce attestation
(#12) the nonce now feeds the fingerprint verify, so these repeated decrypts are both a
TOCTOU window (blob could differ between reads) and an efficiency smell.

**Scope:**
- Load + decrypt the vault **once** during boot orchestration (`runtime.go` / `config_manage.go`),
  before plugin-fingerprint verification (nonce must be available first).
- Thread the decrypted model + nonce to all boot consumers instead of re-decrypting.
- Confirm verify-then-graft sees a single consistent snapshot (closes the TOCTOU window).
