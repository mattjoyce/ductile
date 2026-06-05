---
id: 46
status: done
priority: Low
blocked_by: [12]
tags: [vault, attestation, observability, performance, branch-review]
---

# Attestation · per-spawn fingerprint-verify latency debug log

**From the 2026-06-04 branch review (Hickey-Armstrong Rev2 §2.1).** Compose-time re-hashing
(`config.LoadChecksums` + two keyed-BLAKE3 file hashes per spawn) reintroduces a per-spawn cost
on the hot dispatch path (`internal/.../plugin_verifier.go:38-62`). The cost is currently only
inferable, not measured.

**Scope:**
- [x] Add a `log.Debug` timing line around the per-spawn verify so the cost is observable rather than
  guessed (duration + plugin name).
- [x] Keep it debug-level (no hot-path overhead at normal verbosity).

**Done 2026-06-05.** `pluginIdentityVerifier.VerifyIdentity` (cmd/ductile/plugin_verifier.go) now
times the whole verify (LoadChecksums + keyed-BLAKE3 hashes) via a deferred `logger.Debug` line —
`plugin`, `outcome` (ok/denied), `duration` — on every path, success or deny. Threaded a `*slog.Logger`
through `newPluginIdentityVerifier` (nil → `slog.Default()`); runtime passes its logger.
