---
id: 45
status: todo
priority: Normal
blocked_by: [35]
tags: [docs, vault, secrets, contract, branch-review]
---

# Docs · SECRETS.md / API-doc corrections from the 2026-06-04 branch review

**From the 2026-06-04 branch reviews (Ousterhout-Liskov §1/§2, Hickey-Armstrong Rev2 §2.2).**
Concrete doc fixes the reviewers flagged *after* the #35 SECRETS.md rewrite. Verify each against
the current docs and fix any that still hold.

- [ ] **`secrets` vs `config` envelope (Ousterhout §3).** SECRETS.md §3 says plugin secrets arrive
  in the `config` map, but the wire and `PLUGIN_DEVELOPMENT.md` correctly place them in a distinct
  `secrets` field (`dispatcher.go:412`, `protocol/types.go`). A plugin author following SECRETS.md
  reads the wrong field. Correct to the `secrets` envelope.
- [ ] **Webhook/relay secret-freshness asymmetry (Ousterhout §2).** Plugin secrets compose **fresh
  per spawn** (roll takes effect next dispatch); webhook/relay secrets **freeze at boot** (roll needs
  reload). This is undocumented and SECRETS.md currently implies secrets are uniformly live. Either
  resolve `secret_ref` live for webhook/relay at request time, or state the reload requirement in
  SECRETS.md (`config/vault_secrets.go:35`, `cmd/ductile/runtime.go:686`).
- [ ] **`authorized_principals` nil-vs-empty wire contract (Ousterhout §1).** Partial-update `set`
  makes absent ≠ empty: nil = leave, `[]` = clear, `[list]` = replace (survives the wire as
  `*[]string` + `omitempty`). Add one explicit API-doc sentence so future HTTP clients don't wipe
  grants by flattening the distinction.
- [ ] **register→grant→lock lifecycle (Hickey §2.2).** With compose-time attestation, onboarding a
  plugin is three sequential steps — register principal, grant secrets, **and lock the plugin** (a
  principal needs a recorded keyed fingerprint or `VerifyIdentity` fails). Spell this out in the
  operator handbook and the `ductile` skill.
