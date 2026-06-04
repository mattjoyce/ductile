---
id: 35
status: done
priority: Normal
blocked_by: [8]
tags: [docs, vault, attestation, branch-sweep]
---

# Docs · `./docs/` coverage for the vault + attestation subsystem

**From the branch doc-sweep (2026-06-03).** The vault stack (25 commits) is operational but largely
undocumented in `./docs/`. Per-file gaps below; this is a content card, splittable during triage if too big.

**`docs/SECRETS.md` is the canonical home (operator-cleared 2026-06-03 — rewrite, no coordination needed).**
It already covers age-at-rest, the `ductile secrets` CLI (keygen/encrypt/rotate), and spawn hygiene; keep and
refresh those sections and **add the vault subsystem** there (principal/secret model, set/roll/revoke/purge,
`vault init` genesis, `rotate-key`, compose delivery, attestation, resident-admin-token API). Cross-link the
reference docs below rather than duplicating.

**Per-file (HIGH unless noted):**
- **ARCHITECTURE.md:** no vault subsystem (age blob, principal registry, sole-writer, genesis, Compose
  fail-closed, dispatch secret delivery over stdin, vault_audit) and no attestation model (keyed-BLAKE3 +
  nonce, compose-time re-verify, mismatch escalation, config-lock vs plugin-lock decoupling).
- **CONFIG_REFERENCE.md:** `secrets.age_key_file`, `secrets.vault_file` (paths/perms/resolution incl.
  `DUCTILE_AGE_KEY_FILE`), `service.plugin_env_passthrough` (spawn allowlist); tokens.yaml→vault migration.
- **API_REFERENCE.md:** the `/vault/secret`, `/vault/secret/roll|revoke`, `/vault/principal`,
  `/vault/principal/revoke|purge|roll` endpoints + resident-admin-token auth + request/response schemas.
- **GLOSSARY.md:** principal, authorized_principals, secret_ref, vault, Compose, attestation, fingerprint,
  nonce, plugin lock, config lock, reserved entity.
- **OPERATOR_GUIDE.md:** has "Rotating the vault key" (done in #22) — still needs `vault init` genesis,
  principal register/revoke/purge, secret set/roll/revoke lifecycle, manual vs auto patterns, audit query.
- **for-agents/operator-handbook.md:** add the `vault` noun + `plugin lock` to the agent-facing CLI ref.
- **(LOW)** COOKBOOK (credential-rotation pattern), GETTING_STARTED (`vault init` step), 8_IDIOMS, index.md /
  mkdocs.yml nav entry for vault/attestation.

**Acceptance:** ARCHITECTURE describes the vault + attestation subsystem; CONFIG_REFERENCE/API_REFERENCE/
GLOSSARY cover the new config/endpoints/terms; OPERATOR_GUIDE + for-agents handbook give operators/agents a
full vault workflow; SECRETS.md boundary agreed with its author.

### PROGRESS (2026-06-03)
- **SECRETS.md — DONE** (`8779fe2`): added the Vault section (principal/secret/compose model, sole-writer
  keyless-vs-key-touching split, genesis + lifecycle, rotate-key, attestation gate, backup pairing); kept
  the existing age-at-rest / secrets-CLI / spawn sections; cross-linked rather than duplicated. H1 retitled
  "Secrets, Vault & Spawn Hygiene".
- **Remaining:** ARCHITECTURE.md (vault + attestation subsystem), CONFIG_REFERENCE.md (secrets.* +
  plugin_env_passthrough), API_REFERENCE.md (/vault/* + admin-token auth), GLOSSARY.md (terms),
  OPERATOR_GUIDE.md (init/principal/lifecycle walkthroughs beyond rotate-key), for-agents/operator-handbook,
  and the LOW items (COOKBOOK/GETTING_STARTED/8_IDIOMS/index). mkdocs.yml nav is not-ours — leave it.

## Narrative
- **Source:** branch doc-sweep, docs explorer (2026-06-03).
- **Relates to:** [[33-skills-ductile-operate-vault-coverage]] (share api/config wording),
  [[28-vault-backup-includes-blob-key-out-of-band]], [[15-xcut-recovery-backup-story]],
  [[08-arch-daemon-sole-writer-api]].

### Done — docs/ remainder (2026-06-04, commits `b883024` ARCHITECTURE + `3421b5c` OPERATOR fix + `659e477` rest)
- **ARCHITECTURE.md** (b883024): §5.5 secret-delivery + attestation theory; §6.1 `secrets` envelope; §9.4
  rotation + the two-credential-domain auth; §11.4/§17 redaction reframed.
- **API_REFERENCE.md:** "Vault Management" section (7 endpoints + value-free responses) + admin-token domain.
- **CONFIG_REFERENCE.md:** `secrets.age_key_file/vault_file` (resolution/defaults) + `plugin_env_passthrough`.
- **GLOSSARY.md:** 11 terms (vault, principal, authorized_principals, Compose, secret_ref, attestation,
  fingerprint, nonce, plugin lock, config lock, reserved entity).
- **OPERATOR_GUIDE.md:** "Vault operations" how-to (genesis + lifecycle + local/keyless split + audit);
  fixed the stale backup blockquote (3421b5c).
- **for-agents/operator-handbook.md:** vault/secrets command family, `plugin lock`, config-lock fix.
- **GETTING_STARTED.md:** optional vault-init step; **index.md:** a Secrets card linking SECRETS.md.
- **Audit query** resolved by shipping `ductile system vault-audit` (#37), referenced instead of raw SQL.
- **Remaining LOW (deferred, splittable):** COOKBOOK credential-rotation recipe, an 8_IDIOMS spawn-hygiene
  idiom, for-agents/index.md affordance. mkdocs nav left as "not ours" per the card.
