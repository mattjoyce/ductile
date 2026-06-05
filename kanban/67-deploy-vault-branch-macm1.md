---
id: 67
status: backlog
priority: Normal
blocked_by: [66, 64]
tags: [vault, deploy, mac-m1, rollout]
---

# Deploy the vault branch to the Mac (m1) instance

Rollout step 2 of 3. Apply the proven Thinkpad playbook ([[49-epic-thinkpad-vault-field-trial]]) to the
Mac dev instance, after the corrected deploy doc ([[64-rung14-closeout-docs]]) exists.

## Mac specifics (differ from Thinkpad)
- `~/.local/bin/ductile`, config `~/.config/ductile/`, DB `~/.config/ductile/ductile.db`,
  `127.0.0.1:8082`, service **launchctl** `com.mattjoyce.ductile-local` (NOT systemd).
- Build: `make build` then **codesign** (Darwin) — see deploy memory; cutover is
  bootout/swap/bootstrap launchctl, not `systemctl restart`.
- Watch the strict-mode / config-lock gotcha from the Mac deploy notes.

## Playbook (same rungs, per-instance)
Backup + rollback baseline → build → offline gate → vault_audit migration → env-passthrough audit
(Mac's own plugin set + .env) → config reconcile → **its own** age key + genesis (separate vault) →
import its tokens.yaml → plugin lock --all → cutover → secret-delivery test → soak.

## Acceptance
- Mac instance runs the branch with its own vault; functional secret-delivery test passes; rollback
  artifacts captured. Note: each instance gets a SEPARATE age key + vault (no shared key).
