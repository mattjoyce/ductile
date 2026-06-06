---
id: 33
status: done
priority: Normal
blocked_by: [8]
tags: [skills, docs, vault, branch-sweep]
---

# Skills · `ductile` operate skill + generated skill templates miss the vault surface

**From the branch doc-sweep (2026-06-03).** The operate-the-CLI skill and — critically — the templates that
`ductile system skills` emits do not mention the vault/secrets/attestation commands the branch shipped, so
both human operators and LLMs driving the gateway can't discover them.

**Gaps:**
- **`cmd/ductile/templates/skills-cli-commands.md` (CRITICAL):** hand-maintained static template printed by
  `system skills`; has no Vault / Secrets / `plugin lock` sections. An LLM reading the manifest cannot learn
  vault ops exist. (Decide: keep hand-maintained but update, or generate from the command table.)
- **`cmd/ductile/templates/skills-core-mode.md` (MED):** bootstrap manifest lists only config check / system
  status / job inspect; no path to `vault init` for standing up secrets.
- **`skills/ductile/SKILL.md` (HIGH):** CLI reference omits the `vault` and `secrets` families and `plugin
  lock`; no sole-writer / keyless-CLI-vs-local-key-touching model; the "config lock ritual" predates the
  `config lock` ↔ `plugin lock` decoupling.
- **`skills/ductile/references/api.md` (HIGH):** none of the `/vault/secret`, `/vault/secret/roll|revoke`,
  `/vault/principal`, `/vault/principal/revoke|purge|roll` endpoints; no resident-admin-token auth model.
- **`skills/ductile/references/config.md` (HIGH):** no `secrets.age_key_file` / `secrets.vault_file`,
  no `service.plugin_env_passthrough` (spawn allowlist), no age-at-rest example.

**Acceptance:** `system skills` output (both templates) enumerates the vault/secrets/plugin-lock commands;
the ductile skill + api/config references document the vault command family, the sole-writer/keyless model,
the `/vault/*` endpoints + admin-token auth, and the secrets/spawn-allowlist config.

### PROGRESS (2026-06-03)
- **Generated templates — DONE** (`a785717`): `skills-cli-commands.md` gained Secrets + Vault sections
  (verified flags, keyless-vs-key-touching note) and `plugin.lock`; `skills-core-mode.md` gained the local
  secrets/vault genesis bootstrap note. Verified by rendering `ductile system skills`.
- **Remaining (prose):** `skills/ductile/SKILL.md` (vault/secrets CLI reference + sole-writer model +
  config-lock↔plugin-lock decoupling), `references/api.md` (/vault/* endpoints + admin-token auth),
  `references/config.md` (secrets.age_key_file/vault_file, plugin_env_passthrough). Cross-link
  docs/SECRETS.md (now the canonical secrets+vault doc) rather than duplicating.

## Narrative
- **Source:** branch doc-sweep, skills explorer (2026-06-03).
- Keep `secrets`/vault config wording in sync with [[35-docs-vault-attestation-coverage]] /
  docs/SECRETS.md (the canonical secrets+vault doc) — one source of truth.
- **Relates to:** [[34-skills-plugin-dev-and-rca-vault-coverage]], [[08-arch-daemon-sole-writer-api]].

### Done — prose remainder (2026-06-04, commits `b883024` fossil + `2714e36` gaps)
- **SKILL.md:** the "config lock ritual" fossil split into the decoupled config-lock/plugin-lock acts;
  added a "Vault & Secrets" CLI block (vault + secrets families, sole-writer/keyless split, `plugin lock`),
  the separate `DUCTILE_VAULT_TOKEN` admin-credential note, and a `docs/SECRETS.md` reference.
- **references/api.md:** the seven `/vault/*` routes + the admin-token auth note.
- **references/config.md:** the `secrets.age_key_file/vault_file` block, `plugin_env_passthrough`, an
  age-at-rest note, the tokens.yaml->vault `import` note, and the integrity-workflow `plugin lock` fix.
- Generated templates (`skills-cli-commands.md`, `skills-core-mode.md`) were already correct (a785717) — untouched.
