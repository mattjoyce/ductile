---
id: 27
status: done
priority: Low
tags: [vault, hardening, clarity, backlog]
---

# Vault · hardening / clarity punch-list (split out as each is picked up)

Smaller verified findings from the 2026-06-02 reviews. Each is independently small; grouped here to keep
the board readable — split into its own card when worked.

- [x] **Plaintext lifetime past Compose / redaction type.** `Compose` returns `map[string]string`; the
  dispatcher holds plaintext the vault can't track/zero, and `protocol.Request.Secrets` has no redaction
  type ("never logged" is author discipline — one `logger.Debug(req)` leaks all). Decide the post-Compose
  handling contract; consider zeroing the stdin marshal buffer; consider a redacting type.
  (Ousterhout §2.1/§2.2; design-review Ousterhout §2.1.) blocked_by: [14]
- [x] **Pin check / dump machine-readable output schema.** `vault check` (`[]Issue`) and future `dump
  --values` are "machine-readable" but the wire schema isn't frozen as a contract for the AI operator.
  (Ousterhout design-review §2.5.) blocked_by: [4]
- [x] **Document graft freshness asymmetry.** Rolling a webhook/relay `secret_ref` reaches plugins
  at next spawn but the running webhook/relay server only after a reload (load-time graft is frozen at
  boot, `vault_secrets.go:35` → `runtime.go:659`). One comment + one operator-guide line. (Ousterhout §2.2.)
  blocked_by: [18]
- [x] **`writeFileAtomic` directory fsync.** vault.go:197-226 promises crash-atomic rename but omits
  `dir.Sync()`; not guaranteed on ext4/XFS. One-line fix. (Lamport F8.) → **split out to
  [[38-vault-writefileatomic-dir-fsync]] (2026-06-04).**
- [x] **`RollPrincipal` partial-mutation on mid-loop failure.** `lifecycle_owner.go:71-105` mutates items
  1..N-1 in memory before a mid-loop failure; rollback only fires on `Save` failure. Extract a shared
  `saveOrRollback`/`mutateR[T]` helper (also a DRY fix). (Lamport F6; Ousterhout §4.)
- [ ] **Compose opt-out sentinel vs RPC (deferred).** `composePluginSecrets` uses
  `errors.Is(err, ErrUnknownPrincipal)` to distinguish opt-out from fail-closed; across a form-C RPC the
  sentinel becomes a string and `errors.Is` returns false → unknown principal mis-fails closed. Return a
  serializable typed reason. Only matters if #13 (vaultd) is built. (Ousterhout §3.1.) blocked_by: [13]

## Done (2026-06-04) — punch-list cleared (the 4 unblocked items)
Blockers #4/#14/#18 had since closed, so 4 of the 5 items were actionable:
- **Redaction type (was blocked #14):** `protocol.Request.Secrets` is now a named
  `protocol.Secrets` (map[string]string) implementing `slog.LogValuer` — logging it as
  an attribute redacts (names + count, never values); JSON delivery still carries real
  values. Caveat: covers logging the secrets value/attribute, not a whole-Request dump
  (logging full envelopes is itself the anti-pattern). Buffer-zeroing deferred (defense-in-depth).
- **Check wire contract (was blocked #4):** `vault.Issue` documented as a STABLE,
  additive-only machine-readable contract (kind is a closed enum) — same rule noted for a
  future `dump --values`.
- **Graft-freshness doc (was blocked #18):** comment at `graftVaultSecrets` + an
  OPERATOR_GUIDE line — a rolled webhook/relay `secret_ref` only takes effect after a
  reload; plugin secrets re-resolve at next spawn.
- **RollPrincipal atomicity (Lamport F6):** routed through a new `mutateR[T]` that restores
  the model on ANY failure (not just Save), so a mid-batch roll can't be left resident in
  memory. (DRY: `mutateR` is the batch sibling of `mutate`.)
- Tests added (Secrets redaction + JSON-still-real; RollPrincipal save-rollback).
  gofmt/vet/golangci-lint(0)/gosec clean; `-race` green on protocol+vault+config.

**DEFERRED (1 item):** Compose opt-out sentinel vs RPC — only matters if **#13 vaultd** is
built (blocked_by #13, backlog/maybe-never). Reactivate with #13. Not lost; tracked here.

## Narrative
- **Source:** 2026-06-02 Branch-Reviews (Ousterhout-Liskov, Lamport-Thomas-Hunt) + Design-Reviews.
- Each verified as a real (mostly small / latent / clarity) gap not covered by an existing card.
