---
id: 93
status: backlog
priority: Normal
blocked_by: [85]
tags: [privsep, attestation, authority]
---

# Privsep · bind the worker grant to the plugin fingerprint

> **Nav:** [[83-privsep-epic]] · after [[85-privsep-per-plugin-worker-grant]] · feeds [[90-privsep-negative-test-suite]]

Split out of the original #85. The *which-worker* decision (#85) and the *is-the-binary-still-
the-one-I-granted* decision are **different problems** — this card is the second one, and it
defends against the **secondary** threat (supply-chain swap, ADR §2), so it rides *after* the
grant mechanism works, not bundled into it.

**Scope:**
- Tie each per-plugin worker grant (#85) to the plugin's recorded fingerprint, reusing the
  existing keyed-nonce attestation (#12, nonce held in the vault).
- On fingerprint mismatch the grant is **invalidated** → fall back to the most-restricted
  worker (or refuse), consistent with #12's fail-closed path.
- A substituted plugin therefore does **not** inherit the old plugin's worker identity.
- **Blast-radius reduction, NOT a code-execution gate (Armstrong, B5):** downgrading a mismatched
  binary to the most-restricted worker bounds the *damage* — but the swapped bytes still execute, at
  a lower uid. The gate that stops *unverified code from running* lives in the plugin registry /
  `.checksums` path, not here. State this so #93 isn't read as "swapped binaries don't run."

**Acceptance:** a plugin whose bytes changed since its grant no longer runs as the granted
worker (downgrade or deny); an unchanged plugin runs as granted; behaviour matches #12's
fail-closed attestation path.

## Narrative
- **Source:** Brooks×Beck review — unbundled from #85 so each card carries one decision (if
  something breaks we know whether it was the grant or the binding).
- Secondary threat (ADR §2 names supply-chain swap as secondary to your-own-popped-plugin),
  so deliberately sequenced after the isolation wall (#86/#87) is real.
- Builds on already-shipped #12 keyed-nonce machinery — small increment, not new crypto.
