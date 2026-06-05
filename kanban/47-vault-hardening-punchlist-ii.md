---
id: 47
status: done
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
- [x] **`RollPrincipal` rollback on mid-loop error + DRY (Lamport F6 / §6).** ~~rolls back only on `Save`
  failure~~ — verified 2026-06-05 **already fixed**: `RollPrincipal` uses `mutateR`, which restores the
  model from the last persisted state on ANY error (fn mid-loop OR Save), covered by
  `rollprincipal_rollback_test.go`. The optional `saveOrRollback` DRY extraction (share with `mutate`)
  is left as cosmetic polish — not a correctness gap.
- [x] **Log `RotateKey` final-read error (Lamport N2).** Fixed 2026-06-05: moved the baseline-hash
  refresh into `reencryptTo`, derived from the **ciphertext just written** (never a re-read). This
  removes the silent stale-baseline window on every write path (bridge/rollback/finalise), so a failed
  re-read can no longer leave the next `Save` mistaking the rotation for an out-of-band write. Guarded
  by `TestRotateKeyAdoptsNewKeyringForLaterSaves` (Save-after-rotate succeeds → baseline coherent).
- [x] **Stale `genesis.go` docstring (Hickey / Lamport N5).** ~~Comment at `genesis.go:30-31` says
  "RegisterPrincipal + SetSecret + Save" but the code correctly calls `RotateAdminToken`.~~ Fixed
  2026-06-05 as a side effect of [[40-vault-registerprincipal-reserved-guard]] — now reads
  "SeedCorePrincipal + RotateAdminToken + Save".
- [x] **`isReservedSecret` set-based (Lamport §8.2).** ~~`reserved.go:14` hard-coded `AdminTokenSecret`
  by name~~ — now `reservedSecrets` set-membership (2026-06-05) so a future second reserved secret
  can't silently slip the guard.
- [x] **`roll_count`: drop or add a reader (Brooks-Beck §2.2).** Decision 2026-06-05: **keep.** It is
  not consumer-less — the vault audit `Detail` is its reader, and #69's rotate-admin-token test asserts
  it bumps. Dropping it would churn the model + tests for no gain; not YAGNI.
- [x] **Mark the principal-lifecycle trio as built-ahead-of-need (Brooks-Beck §2.2).** Noted 2026-06-05:
  `revoke-principal` / `purge-principal` / `roll-principal` (Rung 3) are **built ahead of a second
  consumer** — operator lifecycle surface, not load-bearing dependencies; don't mistake them for such.
