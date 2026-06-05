---
id: 62
status: doing
priority: Normal
blocked_by: [60]
tags: [vault, deploy, thinkpad, monitor, soak]
---

# R12 — Soak + monitor

Epic: [[49-epic-thinkpad-vault-field-trial]]. A field trial is only proven by running real traffic.
Watch for the spawn-hygiene regression (the dominant risk) above all.

## Steps
1. **Tail logs** for a soak window: `journalctl --user -u ductile-local -f` — watch for plugin spawn
   failures, missing-env errors, fingerprint-mismatch SECURITY events, audit-write warnings.
2. **Run real plugins that need API keys** (the spawn-hygiene canaries): trigger a fabric job, a
   jina-reader fetch, a youtube_transcript, a claude_harvest — confirm they get their keys
   (via plugin_env_passthrough or vault) and succeed. This validates [[55-rung5-plugin-env-passthrough-audit]].
3. **Health over time**: periodic `curl localhost:8081/healthz` (plugins_loaded steady, no circuits open).
4. **Attestation latency**: check the per-spawn attestation debug log (see [[46-attestation-per-spawn-latency-debuglog]])
   to confirm compose-time verification isn't adding pathological latency.
5. **Audit growth**: `ductile system vault-audit` over time — ops recorded, no soft-fail warnings.

## Acceptance
- Over the soak window: previously-working plugins still work (esp. API-key plugins), no crash-loop,
  no fingerprint SECURITY events, no audit-write soft-fails, attestation latency acceptable.
