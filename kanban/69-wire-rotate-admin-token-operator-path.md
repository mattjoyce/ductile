---
id: 69
status: done
priority: Normal
blocked_by: []
tags: [vault, security, admin-token, cli, api, field-trial-finding, gap]
---

# Wire RotateAdminToken to an operator path (CLI + management API)

**Found during the Unraid field-trial deploy (2026-06-05), via Thinkpad.unraid_admin.**

The genesis admin token (reserved secret `core-admin-token`) is the vault management-API credential,
printed ONCE at `vault init`. There is currently **no operator path to rotate its value** in the branch
(@04463d8), so a token that's been exposed (e.g. captured from `genesis.out`, or leaked) cannot be
rolled in place — the only "rotation" is re-genesis, which mints a new vault and isn't viable on a
populated one.

## Evidence (04463d8)
- `Store.RotateAdminToken` (internal/vault/reserved.go:42) is the ONLY sanctioned writer of the reserved
  admin-token — but it is wired ONLY to genesis (internal/vault/genesis.go:63).
- No CLI subcommand exposes it: cmd/ductile/vault.go cases are init/import/get/set/register-principal/
  roll/revoke/revoke-principal/purge-principal/roll-principal/rotate-key/help — no rotate-admin.
- No management-API route exposes it (only /vault/{principal,secret}).
- `vault roll --name core-admin-token` is refused by the reserved guard (lifecycle.go:21 "use RotateAdminToken").
- `vault rotate-key` rotates the age IDENTITY (re-encrypts the blob), NOT the token value.

## To build
- [x] Local key-touching CLI `vault rotate-admin-token` (daemon-stopped via PID lock, like init/rotate-key):
      `Vault.RotateAdminToken` owner wrapper (lifecycle_owner.go) → `mutate(store.RotateAdminToken)`;
      `runVaultRotateAdminToken` (cmd/ductile/vault.go) resolves vault+key paths, loads the age key.
- [x] Mint a fresh 32-byte CSPRNG token, print once to stdout, audit op=rotate-admin-token (no value).
- [x] Auth model decided: **local path, gated by holding the age key** (operator chose local-CLI-only).
- [N] **Management-API route DEFERRED** — would be the first secret value emitted over HTTP, breaking the
      value-free-API invariant (api/vault.go:17, server.go:296). Documented in SECRETS.md as deliberately absent.
- [x] Docs: SECRETS.md "Rotating the admin token" + DEPLOYMENT.md §11 capture-and-rotate hygiene.
- [x] Tests: internal/vault/rotate_admin_token_test.go (old token dies, new authenticates, RollCount++,
      persisted; reserved guard still refuses data-plane writes post-rotation). build + vet + tests green.

## Acceptance
- An operator can rotate the genesis admin token in place (no re-genesis), the old token stops working,
  the new one is printed once + audited, and the path is documented.

## Interim
Until then: keep the printed token in 0600/0700 custody, gate the management API to LAN/tailnet, and
treat the genesis token as long-lived. Acceptable for a home-lab boundary node; flagged so it isn't
mistaken for a solved rotation story.
