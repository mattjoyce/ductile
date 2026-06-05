---
id: 61
status: done
priority: High
blocked_by: [60]
tags: [vault, deploy, thinkpad, test, secret-delivery]
---

# R11 — Functional secret-delivery test (end-to-end)

Epic: [[49-epic-thinkpad-vault-field-trial]]. Prove a vault secret actually reaches a plugin over
stdin and is audited. Mirror the in-repo fixture `test/fixtures/docker/vault-secret-delivery/run.sh`.

## Steps
1. Register a principal + grant a test secret (daemon running, keyless API clients):
   - `ductile vault register-principal --api-url http://localhost:8081 --name <plugin> --kind plugin`
   - `echo -n 'testvalue' | ductile vault set --api-url http://localhost:8081 --name trial-secret --principal <plugin>`
2. Dispatch a job to that plugin (a probe, or an existing plugin that logs `request.secrets` names/lengths):
   `ductile api /plugin/<plugin>/poll -X POST -f ...` and inspect the job.
3. Confirm the plugin received the secret over stdin (name + length, never the value — redaction by design).
4. **Audit**: `ductile system vault-audit --config ~/.config/ductile/` shows the register + set ops
   (and any compose-denial if a fingerprint mismatch occurred).
5. Confirm the reserved-entity guard: `ductile vault get --name core-admin-token` is REFUSED.

## Acceptance
- A vault secret is delivered to a plugin over stdin and the op appears in `vault-audit`;
  reserved-name read is refused. End-to-end vault path proven on the Thinkpad.
