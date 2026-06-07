---
id: 108
status: todo
priority: High
blocked_by: []
tags: [vault, security, fail-closed, privsep, bug]
---

# Vault compose is fail-OPEN on an unknown principal (should fail-closed / warn loud)

> **Nav:** [[83-privsep-epic]] · [[01-vault-epic]] · security finding from the 2026-06-07 Phase-2 work.

**Job story:** *When* a plugin's principal is unknown/misnamed at compose time, *I want* the gateway to
fail closed (or at minimum warn loudly), *so* a typo can't silently leave a plugin running with **no
secret and no error** — consistent with privsep's fail-closed spine.

## The finding (ductile-admin, 2026-06-07)

`Compose(job.Plugin)` resolves the plugin name to a vault principal with **no normalization**. An
unknown principal (typo, snake_case name that was never registered, missing registration) yields
**silently zero secrets** — the plugin spawns, gets an empty `secrets` map, and runs. No error, no
warning. That is **fail-OPEN**.

This is inconsistent with the rest of the privsep design, which is deliberately fail-closed: the boot
gate refuses on capability/accounts mismatch; a `run_as` to an undefined account fails at config load;
a fingerprint mismatch with no downgrade tier is terminal. The secret-delivery path should not be the
one fail-open seam — a misconfigured principal silently disabling a secret is exactly the kind of quiet
failure that ships a broken-but-green gateway.

## Options (decide on pickup)
- **Fail-closed:** if a plugin is configured/known to require secrets and its principal is unknown,
  refuse the spawn (terminal), like the undefined-`run_as` case.
- **Loud warn (floor):** at minimum, log a clear boot/spawn warning naming the unknown principal and
  the plugin that got no secrets — so it's never silent.
- Distinguish "plugin legitimately needs no secrets" from "principal unknown" so the check doesn't
  false-positive on keyless plugins.

## Acceptance
An unknown/misnamed principal for a secret-needing plugin produces a loud, attributable failure (or
refusal), never a silent empty-secrets spawn. A keyless plugin is unaffected.

## Narrative
- 2026-06-07: Found while diligencing the Phase-2 secret scheme — the compose path's silent
  no-secret-on-unknown-principal is a fail-open footgun out of step with privsep's fail-closed spine.
  Worth fixing before the plugin-vault-native workstream ([[107-plugins-vault-native-conformance]])
  multiplies the number of principal names in play. (by @assistant)
