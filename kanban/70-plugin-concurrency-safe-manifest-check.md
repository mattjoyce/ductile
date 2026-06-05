---
id: 70
status: done
priority: Normal
blocked_by: []
tags: [plugin, manifest, concurrency, correctness, unraid]
---

# Verify `concurrency_safe` lives in the manifest for serial-required plugins

**RESOLVED 2026-06-05 (Thinkpad.unraid_admin verified on the NAS, over walkie-talkie):** both
`birda/manifest.yaml` and `health_data_summary/manifest.yaml` **already declare `concurrency_safe: false`**
— serial execution is enforced at the manifest (the single honored home), birda's single-GPU 300s budget
is safe. The dropped `plugins.yaml` copies were redundant/inert; dropping them changed nothing. No manifest
edit or re-attest required. Card kept as a confirmation record.

**Origin ([[68-deploy-vault-branch-unraid]] strict-decode cleanup, 2026-06-05):** Unraid's `plugins.yaml`
carried `concurrency_safe: false` on **birda** and **health_data_summary**. That key is a plugin
**manifest** field (`internal/plugin/manifest.go:159`, `ConcurrencySafe *bool`), **not** a valid config
field — the lenient loader had been dropping it silently, so it was inert. We dropped it from config to
reach strict-decode parity.

**The risk it leaves:** if either plugin actually needs to run **serial** (birda has a single-GPU 300s
budget — concurrent GPU jobs would contend), the only honored declaration is in its **manifest**. With the
config key gone and no manifest declaration, the dispatcher defaults to `concurrency_safe: true`
(`discovery.go:191`) → parallel. Inert-in-config means it may *already* have been running parallel.

## Do
- Check birda's and health_data_summary's manifests for `concurrency_safe`.
- If serial execution is intended (confirm with the plugin's resource model — GPU/contention), set
  `concurrency_safe: false` **in the manifest**, then `plugin lock` to re-attest.
- If parallel is actually fine, no change — just record the decision so the dropped config key isn't
  re-added by reflex.

**Acceptance:** birda + health_data_summary run with the intended concurrency, declared in the manifest
(the single honored home), verified against the dispatcher's serialization path (`dispatcher.go:259`).
