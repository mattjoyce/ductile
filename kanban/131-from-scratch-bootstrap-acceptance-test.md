---
id: 131
status: todo
priority: High
tags: [vault, bootstrap, testing, docs-drift, acceptance]
---

# From-scratch bootstrap acceptance test + docs reconcile

> **Nav:** child of [[128-vault-native-bootstrap-no-offline-seed]]; design root
> [[../docs/adr/vault-credential-ladder]]. Depends on [[129-vault-operable-boot-posture]] +
> [[130-activate-on-reload-observable-posture]]. This is #128's original "Done when", made real.

## Do
1. **Acceptance test** (fast, in-process or a docker fixture) for the full ladder walk:
   genesis (`vault init`) → boot in management posture ([[129]]) → admin token mints the api token via
   `/vault/*` → `system reload` ([[130]]) → `GET /topology` returns **200 with** the api token and
   **401 without**. No literal tokens, no manual DB surgery.
2. **Docs reconcile to the implemented flow:**
   - `docs/DEPLOYMENT.md §11` — rewrite the first-time deploy to the two-posture ladder.
   - `docs/SECRETS.md:193` — drop the phantom `import` from the "local, key-touching ops" list
     (the command does not exist; see #128).
   - `cmd/ductile/templates/skills-cli-commands.md` — remove the phantom `vault.import` advert.
   - Confirm the `ductile` operate skill describes the management posture, not the dead offline seed.
3. Once green, this unblocks [[118-system-tier-curate-trust-property-fixtures]] (fixtures can boot
   vault-native against a real template) and [[116-testing-gate-green-only-governance]] (gate the green set).

## Done when
A documented, tested, from-scratch sequence brings up a vault-native gateway with the API enabled, via
the credential ladder, and every doc that describes the bootstrap matches the implemented commands
(no phantom `import`/`reconcile`).
