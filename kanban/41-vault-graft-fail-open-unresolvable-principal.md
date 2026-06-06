---
id: 41
status: done
priority: Normal
blocked_by: [18]
tags: [vault, dispatch, config, security, branch-review]
---

### DONE (2026-06-05) — resolved as WARN-ONLY (operator decision)
Fail-closed would have reversed an intentional, test-pinned decision
(`vault_secrets_test.go`: "a grant to an unregistered principal must not hide the secret
from the graft") and could break a legitimate consumer on a typo. Chosen middle path
(the reviewer's "or at least log the widening"):
- `activeVaultSecrets` now returns warnings alongside the secret map; `graftVaultSecrets`
  propagates them through the existing `logGraftWarnings` → `slog.Warn` channel.
- New `unregisteredGrantee` helper: a secret whose grant names an unregistered principal
  stays load-time visible (unchanged) **but emits a loud warning** naming the secret and
  the dangling principal.
- Test extended to assert both visibility AND the warning. Full suite green.

# Vault · load-time graft fails open on an unresolvable principal

**From the 2026-06-04 branch review (Hickey-Armstrong Rev2 §1.2).** `pluginScopedSecret`
(`internal/config/vault_secrets.go:160-178`) fails **open** for secrecy: when a grant names a
principal that isn't registered or isn't a plugin (an operator typo), the secret is treated as
*not* plugin-scoped and grafts to **all** load-time consumers gateway-wide, rather than being
withheld.

A misspelled `authorized_principals` entry silently widens a secret's blast radius instead of
denying it. Should fail **closed** — or at minimum log the widening loudly so it's auditable.

**Scope:**
- Decide policy: fail-closed (withhold from the global graft when a named principal can't be
  resolved) vs. fail-open-with-warning.
- Implement at `pluginScopedSecret` / `graftVaultSecrets`.
- Test: a grant to an unregistered/non-plugin principal does not leak the secret globally.
