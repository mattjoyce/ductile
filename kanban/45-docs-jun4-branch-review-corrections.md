---
id: 45
status: review
priority: Normal
blocked_by: [35]
tags: [docs, vault, secrets, contract, branch-review]
---

# Docs · SECRETS.md / API-doc corrections from the 2026-06-04 branch review

**From the 2026-06-04 branch reviews (Ousterhout-Liskov §1/§2, Hickey-Armstrong Rev2 §2.2).**
Concrete doc fixes the reviewers flagged *after* the #35 SECRETS.md rewrite. Verify each against
the current docs and fix any that still hold.

- [x] **`secrets` vs `config` envelope (Ousterhout §3).** ~~SECRETS.md §3 said plugin secrets arrive
  in the `config` map~~ — verified the wire (`protocol/types.go` distinct `secrets` field,
  `dispatcher.go` `req.Secrets`) and PLUGIN_DEVELOPMENT.md (correct). Fixed 2026-06-05: SECRETS.md
  lines for Compose / example / spawn-hygiene / See-also now say the `secrets` map. (The env-passthrough
  line legitimately keeps `config` — moving a *non-secret* value there is correct.)
- [x] **Webhook/relay secret-freshness asymmetry (Ousterhout §2).** Fixed 2026-06-05: chose to
  document (OPERATOR_GUIDE.md already covered it at §"Rolling…"; SECRETS.md was silent). Added a
  "Freshness asymmetry" note to SECRETS.md Compose — plugin secrets fresh per spawn, webhook/relay
  freeze at boot and need `system reload` — cross-linked to OPERATOR_GUIDE.md.
- [x] **`authorized_principals` nil-vs-empty wire contract (Ousterhout §1).** Fixed 2026-06-05:
  API_REFERENCE.md `POST /vault/secret` row now states omit=leave, `[]`=clear, list=replace
  (partial update never silently wipes grants).
- [x] **register→grant→lock lifecycle (Hickey §2.2).** Fixed 2026-06-05: added the three-ordered-step
  onboarding note (register principal → grant → `plugin lock`, all required or compose-time identity
  verify fails → no delivery) to the operator handbook and the `ductile` skill vault sections.
