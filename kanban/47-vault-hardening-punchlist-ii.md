---
id: 47
status: todo
priority: Low
tags: [vault, hardening, clarity, dry, branch-review, backlog]
---

# Vault · hardening / clarity punch-list II (2026-06-04 branch review)

Smaller nice-to-have findings from the 2026-06-04 branch reviews. Each is independently small;
grouped to keep the board readable — split into its own card when picked up. (Follows the pattern
of [[27-vault-hardening-punchlist]], now closed.)

- [x] **Timing-flat `AuthenticateAdmin` (Lamport F9).** ~~`vault.go` early-returned before the
  constant-time compare when the token is absent/revoked, leaking "does the admin token exist and
  is it active."~~ Fixed 2026-06-05: always runs the constant-time compare against the resident
  (or zero) value; an `active` guard rejects absent/revoked. Edge covered by
  `TestAuthenticateAdminTimingFlatRejectsAbsent`.
- [ ] **`RollPrincipal` rollback on mid-loop error + DRY (Lamport F6 / §6).** `lifecycle_owner.go:79-103`
  rolls back only on `Save` failure, not on a mid-loop error. Extract a `saveOrRollback` helper and
  reuse it from `mutate` (`vault.go:211-216`) — fixes the rollback gap and the duplication together.
- [ ] **Log `RotateKey` final-read error (Lamport N2).** `rotate_key.go:103` only refreshes
  `lastDiskHash` when `rerr == nil`; on error the in-memory baseline goes stale silently. Derive the
  baseline hash from the bytes just written instead of re-reading, and log the error.
- [x] **Stale `genesis.go` docstring (Hickey / Lamport N5).** ~~Comment at `genesis.go:30-31` says
  "RegisterPrincipal + SetSecret + Save" but the code correctly calls `RotateAdminToken`.~~ Fixed
  2026-06-05 as a side effect of [[40-vault-registerprincipal-reserved-guard]] — now reads
  "SeedCorePrincipal + RotateAdminToken + Save".
- [x] **`isReservedSecret` set-based (Lamport §8.2).** ~~`reserved.go:14` hard-coded `AdminTokenSecret`
  by name~~ — now `reservedSecrets` set-membership (2026-06-05) so a future second reserved secret
  can't silently slip the guard.
- [ ] **`roll_count`: drop or add a reader (Brooks-Beck §2.2).** `Secret.RollCount` is "audit-only"
  with no functional consumer beyond an audit Detail string. Either drop it (YAGNI) or implement the
  audit reader.
- [ ] **Mark the principal-lifecycle trio as built-ahead-of-need (Brooks-Beck §2.2).** `revoke-principal`
  / `purge-principal` / `roll-principal` (Rung 3) were built without a second consumer; note this so
  they aren't mistaken for load-bearing.
