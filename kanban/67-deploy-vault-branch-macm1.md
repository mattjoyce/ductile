---
id: 67
status: doing
priority: High
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

## Progress (2026-06-05)
**Vault deploy: DONE.** Mac runs **v0.843-04463d8** (launchctl), all four admission gates on
(`validate_config_on_boot: true` boots clean), "compose-time attestation on", own age key
(`~/.config/secrets/ductile/age.key`, pub `age194td07...`) + vault, admin token in 0600
`~/.config/secrets/ductile/genesis.out`. No tokens.yaml → genesis only, no import. Vault
register/set/get parity verified. Config cleanup: log_level→service, dropped discord_notify dead
flat timeout, **removed dangling `beads/.env` include** (file gone — would've blocked boot),
strict_mode→admission, `plugin_env_passthrough: [ANTHROPIC_API_KEY, OPENAI_API_KEY]` for the
scribe-*/email_pipeline_veto plugins (they read those from env). 27 plugins attested.

**REGRESSION I caused — gmail/email broke on cutover (NOT vault/spawn-hygiene):** restarting the
launchd agent from a headless (Claude Code) context lost the GUI login-session **Keychain** access
that `gws` needs (keyring_backend=keyring, auth_method=none, no env token, no file creds). gmail_poller
+ email_pipeline_fetch worked at 17:55, fail since the 18:06 cutover. `launchctl kickstart` from this
context did NOT restore it.

**FIX (needs Matt's GUI session):** from **Terminal.app while logged in** (NOT via Claude `!`, which is
the same headless context):
```
launchctl bootout   gui/$(id -u)/com.mattjoyce.ductile-local
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.mattjoyce.ductile-local.plist
gws auth status          # should show authenticated in the GUI session
```
Or log out/in (plist is RunAtLoad → loginwindow loads it in the Aqua session). Confirm gmail_poller
succeeds, then this card → done. Rollback to v0.787 would NOT fix gmail (same restart-context issue)
and would discard the good vault deploy — so don't.

**Rollout note for #68 (unraid):** the Mac's `beads/.env` dangling-include + the GUI-session keychain
gotcha are Mac-specific; but DO check unraid for dangling includes and any keychain/session-bound creds.

## Acceptance
- Mac instance runs the branch with its own vault; functional secret-delivery test passes; rollback
  artifacts captured. Note: each instance gets a SEPARATE age key + vault (no shared key).
