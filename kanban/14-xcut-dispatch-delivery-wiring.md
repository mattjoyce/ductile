---
id: 14
status: done
priority: Normal
blocked_by: [5]
tags: [vault, dispatch, delivery]
---

# Cross-cutting · dispatch delivery wiring (Compose → plugin)

Deliver composed secrets to a plugin at spawn, over stdin (never env, never argv).

**Scope:**
- Decide the delivery slot: add a **`Request.Secrets` field (recommended)** vs merge into `Config` — `internal/protocol/types.go`.
- Wire `Compose(principal)` into the spawn path so a plugin receives exactly its authorized secrets.
- Composes with the existing spawn-hygiene env allowlist (`internal/dispatch/env.go`).

**Acceptance:** a registered plugin receives its composed secrets over stdin at spawn; an unauthorized secret is absent; nothing leaks via env/argv.

**Graft tightening split to #18.** #9 grafts *all* active vault secrets into the load-time `cfg.Tokens` map. Now that per-plugin stdin delivery exists, plugin-scoped secrets should be removed from the global graft — but that change is ADR-adjacent and was deferred to its own card (**#18**) per the 2026-06-02 grill.

## Narrative
- **Source:** handoff §"Open micro-decisions" #1 (dispatch delivery slot) — "needed when wiring end-to-end delivery, not before."
- Depends on Compose (#5); follows Rung 2 references (#9) for the operator-facing `secret_ref:` story.

### Done (2026-06-02, uncommitted)
Slice 1 — `Compose`→stdin delivery — built TDD; acceptance met.
- **Delivery slot:** `protocol.Request.Secrets map[string]string` (`json:"secrets,omitempty"`) — distinct from `Config` (secrets are per-principal authorization, not configuration). Wire round-trip tested.
- **Decomplected policy** (`internal/dispatch/secret_delivery.go`): `SecretComposer` interface defined at the point of use + pure `composePluginSecrets`. Contract (settled by grill — *registration = authorization only; identity stays with the registry/.checksums*): composer nil → no secrets; unknown principal → no secrets, no error (vault is an opt-in overlay); revoked principal (or any non-opt-out Compose error) → **fail closed**; active → deliver, denials logged. 6 unit tests.
- **Wiring:** `dispatch.WithSecretComposer` option (nil → no delivery, back-compat); populated at the `Request` build in `dispatcher.go` with fail-closed `completeJob`. Delivery stays stdin-only (env/argv already withheld by spawn hygiene).
- **Runtime read-path:** `config.LoadVaultStore(configDir, cfg)` — single entry for the in-memory vault model; `vaultStore` helper now shared with #9's graft (load/degradation logic has one home). `buildRuntime` loads it and passes the composer, guarding the typed-nil-interface trap. 3 loader tests.
- Gate green: gofmt/goimports/vet/golangci-lint(0)/gosec(0)/`go test -race -shuffle=on` across dispatch, protocol, config, cmd/ductile.
