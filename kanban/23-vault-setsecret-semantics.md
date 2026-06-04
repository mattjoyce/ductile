---
id: 23
status: done
priority: Normal
blocked_by: [4, 10]
tags: [vault, api, lifecycle, footgun, security]
---

# Vault · SetSecret semantics — partial update + don't let set be a roll/audit side door

Two related, verified facets of the `SetSecret` upsert (`internal/vault/secret.go:42-50`):

**(a) Grant-wipe footgun (Hickey-Armstrong Branch-Review §1.2).** The upsert wholesale-replaces
`existing.AuthorizedPrincipals = authz`, and the API field is `omitempty` (`api/vault.go:78`), so
`POST /vault/secret {"name":"x","value":"y"}` resolves to `authz=[]` and **silently revokes every grant
on x**. A value edit silently stops delivery to every authorized plugin.

**(b) Set-as-roll side door (Lamport-Thomas-Hunt §3.2/§3.3, corroborates design-review).** Both `set`
and `roll` overwrite the value, but `set` bumps no `RollCount` and emits a `set` (not `roll`) audit fact —
so a script can change a secret's value via `set`, bypassing the roll audit semantics. "A control you can
step around is not a control." Note the ADR §3.3 ("set supersedes if the name exists") and card #4's
glossary ("value is immutable; changing it is a roll") currently **contradict** each other.

**Scope:**
- (a) Make `SetSecret` a true partial update: nil slice = leave grants, explicit `[]` = clear; OR have the
  API reject a value change that omits `authorized_principals` on an existing secret.
- (b) DECISION: does `set` change an existing value, or only metadata/grants with value changes forced
  through `roll`? Pick one and align ADR §3.3, card #4 behavior, and the glossary.
- Fold in the empty-value-active check (Lamport F7): refuse `(active, value="")`.

**Acceptance:** a value-only edit cannot silently wipe grants; the set-vs-roll value-change rule is
decided, enforced, and consistent across ADR + glossary + code; empty active values are refused.

## Narrative
- **Source:** Hickey-Armstrong Branch-Review §1.2; Lamport-Thomas-Hunt Branch-Review §3.2-3.3 + design-review §3.3.
- **Not covered by:** #4/#10 (no partial-update or set-vs-roll-audit invariant). This is the one place ADR
  text and a card narrative contradict — needs an explicit operator decision.

## Decision (operator, 2026-06-04): VALUE IS ROLL-ONLY
`set` never changes an existing secret's value — a differing value is refused (`ErrValueImmutable`);
`roll` stays the sole, `roll_count`-audited value-change path. Closes the set-as-roll side door.

## Done (2026-06-04)
- **(b) set-as-roll side door — closed.** `Store.SetSecret` (`internal/vault/secret.go`) now refuses a
  value change on an existing secret (`ErrValueImmutable`); an empty/equal value leaves it. Value changes
  go through `roll` only.
- **(a) grant-wipe footgun — fixed.** `set` is a true partial update: `authorized_principals` nil = leave
  grants, explicit `[]` = clear, `[list]` = replace. The API request field is now `*[]string`
  (`internal/api/vault.go`) so omitempty no longer collapses absent vs `[]`; the CLI uses `flag.Visit` to
  tell `--principal` absent (leave) from given (`cmd/ductile/vault.go`). `pattern` gets the same
  absent-vs-explicit treatment ("" = leave on update / default manual on create) so a metadata set can't
  silently flip an `auto` secret to `manual` — the API stopped pre-defaulting pattern, the model owns it.
- **(c) empty active value — refused.** Creating an active `manual` secret with an empty value is rejected
  (`ErrEmptyValue`); `auto` is exempt (minted by first roll — verified flow).
- **Tests:** rewrote `TestSetSecretUpdateKeepsCreatedBumpsUpdated` (now metadata-only); added value-immutable,
  partial-grants (nil/[]/list), pattern-leave, empty-manual-refused/auto-allowed, and CLI omit-when-absent +
  API `*[]string` cases. gofmt/vet/golangci-lint(0)/gosec clean; `-race` green on vault + api + cmd.
- **Docs:** `docs/SECRETS.md` now states set is a partial update + value is roll-only.
- **ADR aligned (2026-06-04):** ADR §3.3 `set` row + the resolved-decisions entry in Obsidian
  (`Ductile - Vault.md`) amended — `set` is a partial update, value is roll-only. The `retrieve (get)`
  row was also updated when `vault get` shipped (local, key-holding, never over the API).
- **e2e verified (2026-06-04):** full set/get/roll/revoke lifecycle proven on the Dell (x86_64 Linux),
  12/12 — incl. set value-immutability rejection, partial-update, and the local-key `get` reading a
  daemon-rolled value. No code fix needed.
