---
id: 57
status: done
priority: High
blocked_by: [56]
tags: [vault, deploy, thinkpad, genesis, age-key, security]
---

# R7 — Age key + vault genesis

Epic: [[49-epic-thinkpad-vault-field-trial]]. First-time vault bootstrap. **Service must be STOPPED**
(these key-touching ops take the PID lock and refuse if the daemon holds it).

## Steps
1. **Generate the age identity** (operator-minted, NOT auto-created by genesis):
   `ductile secrets keygen --out <oob-path>` → writes private key (mode 0600) + prints the public
   recipient to stderr. **Custody:** store the private key OUT-OF-BAND (e.g. `~/.config/secrets/ductile/age.key`,
   never in the repo, never in a `system backup` — restore needs both blob + key). Record the public recipient.
2. Point config at it (done in [[56-rung6-config-reconciliation]]: `secrets.age_key_file`).
3. **Genesis** (service stopped): `ductile vault init --vault ~/.config/ductile/vault.age --key <key>`
   - Creates the `core` gateway principal (32-byte nonce), the reserved `core-admin-token` secret, and
     the encrypted `vault.age` blob (0600).
   - **The admin token is printed to stdout ONCE and is never recoverable in plaintext** — capture it
     immediately into a credential manager. It is the canonical vault management-API credential
     (distinct from api.yaml tokens).
4. Confirm `vault.age` exists (0600) and the key decrypts it (a read op, e.g. `vault get` of a known name later).

## Acceptance
- Age key exists out-of-band (0600), public recipient recorded; `vault.age` created and decryptable;
  admin token captured to a secure store. No secret material written to repo/backups.
