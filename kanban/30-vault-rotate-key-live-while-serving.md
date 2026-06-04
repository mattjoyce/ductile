---
id: 30
status: todo
priority: Low
blocked_by: [22, 13]
tags: [vault, daemon, rotation, deferred, yagni]
---

# Vault · live `rotate-key` while the daemon is serving (no downtime)

**Deferred from #22 (slimming pass, 2026-06-03).** #22 ships `vault rotate-key` as a LOCAL,
daemon-down, PID-guarded command (consistent with #8: key-touching ops are local, like `vault init`).
This card is the no-downtime upgrade: rotate the daemon's key *while it serves*, via the authenticated
management API (keyless CLI → POST), doing the bridge+verify+keyring-swap under the vault write lock so
concurrent ops block for the ~ms it takes.

Only worth building if stop→rotate→start downtime becomes unacceptable on a single host (unlikely
pre-vaultd). Closely tied to the #13 vaultd go/no-go.

**Scope:**
- `VaultManager.RotateKey` + `POST /vault/rotate-key` in the authenticated group + runtime wiring.
- Reuse #22's bridge+verify core; run it under `v.mu.Lock()`; hot-swap the in-memory keyring.
- Daemon mints; new key written to the boot key path; never transmit the private key over the API.

**Acceptance:** an operator can rotate the daemon's key with the daemon up and serving; no silent
revert; concurrent vault ops are correctly serialized; tested.

## Narrative
- **Source:** #22 grilling (2026-06-03). Operator chose the slim local op; this is the live variant, YAGNI for now.
- **Relates to:** [[22-vault-recipient-rotation-coherence]], [[13-rung5-vaultd-daemon]], [[08-arch-daemon-sole-writer-api]].
