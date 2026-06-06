---
id: 34
status: done
priority: Normal
blocked_by: [8]
tags: [skills, plugin-dev, rca, attestation, branch-sweep]
---

# Skills · plugin-developer + RCA skills miss attestation, secret delivery, and vault failure modes

**From the branch doc-sweep (2026-06-03).** The author-facing and incident-facing skills don't cover the
attestation/secret-delivery model the branch shipped.

**`skills/ductile-plugin-developer/SKILL.md` (MED):**
- No plugin fingerprint / attestation story; a plugin must be `plugin lock`-ed before it receives secrets —
  unmentioned.
- The plugin protocol section doesn't say secrets are delivered by the core in the request envelope
  (over stdin) at compose time — plugins consume, never manage them.
- Spawn hygiene / env allowlist (`plugin_env_passthrough`) constraint on the plugin's environment is absent.
- The "config lock handoff" step predates `plugin lock`; manifest/entrypoint byte changes break the
  fingerprint and must be re-attested.

**`skills/ductile-rca/SKILL.md` (MED):**
- Evidence ladder omits the `vault_audit` fact table (registrations/rolls/revokes).
- No failure modes for: fingerprint mismatch / failed attestation, "principal registered but no secret
  delivered", secret delivery gated/refused at compose time.
- The "forgot to config lock" root-cause section needs a sibling: "forgot to `plugin lock` after a manifest
  change" (post-decoupling).

**Acceptance:** plugin-developer skill explains attestation (`plugin lock`), the secret-delivery envelope,
and spawn hygiene; the RCA skill adds vault_audit to the evidence ladder and the new attestation/secret-
delivery failure modes + the plugin-lock root cause.

## Narrative
- **Source:** branch doc-sweep, skills explorer (2026-06-03).
- **Relates to:** [[33-skills-ductile-operate-vault-coverage]], the attestation commits (plugin lock,
  compose re-verify, fingerprint-mismatch escalation), [[35-docs-vault-attestation-coverage]].

### Done (2026-06-04, commits `b883024` fossils + `dd0f056` gaps)
- **plugin-developer SKILL.md:** protocol "one screen" gains the `secrets` envelope field with the
  consume-not-manage + attest-before-secrets + don't-echo rules; Step 10 handoff fixed to `plugin lock`
  (not `config lock`) with the fail-closed consequence; spawn-hygiene note (minimal allowlisted env,
  extras via `service.plugin_env_passthrough`); links `docs/SECRETS.md`.
- **rca SKILL.md:** evidence ladder gains a `vault_audit` rung (rung 7, via `ductile system vault-audit`);
  three secret-delivery failure modes added (silent empty secrets / revoked fail-closed / fingerprint-
  mismatch as a SECURITY event); a validation-table row; the "forgot to lock" root cause split into the
  decoupled config-lock vs plugin-lock siblings; links `docs/SECRETS.md`.
- Used the new `system vault-audit` (#37) as the evidence command rather than raw SQL.
