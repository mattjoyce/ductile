---
id: 13
status: wontfix
priority: Normal
blocked_by: [5, 8]
tags: [vault, rung5, daemon, wontfix]
---

### WON'T FIX (2026-06-05) — operator: "#13 is a hard no"
The standalone `vaultd` form is explicitly not happening. The single in-memory owner behind a
`sync.RWMutex` (`internal/vault/vault.go`) is the final concurrency model — a separate
servicing process/goroutine buys nothing on a single host and is pure operational surface.
Closing rather than leaving as a perpetual "maybe never." (Knock-on: closes
[[44-vault-compose-denial-serializable-enum]], which existed only to survive the RPC hop this
daemon would have introduced.) Kept on disk for the rationale below.

# Rung 5 · `vaultd` daemon + multi-host propagation (maybe never)

Optional standalone form.

**Scope:**
- Split the vault module into a `vaultd` daemon (form C): a process that alone holds the key + in-memory model and serves `Compose` / management over its API.
- Automated multi-host secret propagation.

**Acceptance:** N/A until pulled forward — explicitly "maybe never" for a single-host homelab.

## Narrative
- **Source:** handoff §"Build sequence — Rung 5 (maybe never)".
- Enabled cheaply because #8 makes B→C a deployment change, not a re-contract. Single-host homelab → likely never needed; kept as the named future rung.

### DECISION (2026-06-03) — deferred, no compelling rationale now
Reviewed with operator. No compelling reason to build now:
- **Standalone `vaultd` (form C):** #8 already made B→C a deployment change, not a re-contract, so deferring
  costs nothing architecturally. On a single host a separate process is pure operational surface (another
  service to supervise/secure/back up) for ≈nil payoff.
- **Multi-host propagation:** explicitly designed *against* — the store is the daemon's private blob with a
  single reader ("no other entity knows the store exists"; see [[22-vault-recipient-rotation-coherence]]).
- **Actual genesis (operator):** the original idea was to **tease out the vault as a reusable module for other
  projects**, not multi-host. That — a second project genuinely needing the vault module — is the real
  pull-forward trigger, not a second host. Even then, weigh "one vault per host/project" before "one vaultd
  serving many" to preserve the single-reader blast radius.

Status: stays `backlog` as the named future rung; **not for now.**
