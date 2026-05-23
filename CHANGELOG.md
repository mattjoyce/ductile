# Changelog

## 2026-05-23
- Add hook lifecycle controls. Hook jobs now carry `parent_job_id` linking them to the source job that fired the hook, making chains walkable in the queue. New `service.hook_max_depth` (default `4`) caps the on-hook chain at the configured depth; enqueue is refused when the next hook would exceed the cap. `ductile doctor` reports a `hook_cycles` warning when a reciprocal or self-firing `notify_on_complete` configuration exists (respecting `from_plugin` filters to avoid false positives). Phase 2 CIA finding P2-11.
- **BREAKING (strict mode + tick floor):** When `service.strict_mode: true`, operational config drift (e.g. `config.yaml`, `routes.yaml`) — previously a warning at startup and silent at reload — now hard-fails both the startup pre-flight and `/system/reload`. High-security files (`tokens.yaml`, `webhooks.yaml`) and plugin entrypoint fingerprints continue to reject unconditionally. Reload sources the `strict_mode` policy from the running config so an attacker cannot relax admission via the reload they are pushing. Also: `service.tick_interval` now has a hard floor of `100ms` (rejected by config load) and a soft floor of `1s` (`ductile doctor` warning). Phase 2 CIA finding P2-10.
- **BREAKING (auth scopes):** `plugin:ro` is now catalog-only. Tokens that previously invoked read-class plugin commands via `plugin:ro` (e.g. `stress.cpu`) must add `plugin:invoke:ro` to retain invocation. `plugin:rw` and wildcard `*` are unaffected — they continue to grant both catalog read and read-class invocation. New scopes: `plugin:catalog:ro` (read manifests, list commands) and `plugin:invoke:ro` (invoke read-class commands). Phase 2 CIA finding P2-07 — was over-broad least-privilege boundary.
- Bundled `fetch` plugin: hard-blocks egress to loopback / link-local / private (RFC1918, ULA) / cloud-metadata IPs by default. Resolves the URL hostname and re-validates each redirect hop; pins the resolved IP between admission and connect to defeat DNS rebinding. Caps response bodies at 1 MiB (configurable). Default `follow_redirects` flipped from true to false. New optional config keys `allowed_hosts` / `denied_hosts` / `max_response_bytes`. Phase 2 CIA finding P2-06.
- Add narrower OPTIONAL `jobs:*:ro` / `events:*:ro` scopes (`jobs:status:ro`, `jobs:result:ro`, `jobs:logs:ro`, `jobs:tree:ro`, `events:meta:ro`, `events:payload:ro`) with per-principal response shaping: principals without `jobs:result:ro` no longer see `result`, `last_error`, or `stderr` fields in `/job/{id}`, `/job/{id}/tree`, or `/job-logs?include_result=true`. Back-compat: `jobs:ro` is a super-scope that implies all four narrower scopes — existing tokens see identical response shape. SSE `/events` payload shaping is reserved for a follow-up bead. Phase 2 CIA design note D1.

## 2026-05-05
- Add `tcc_paths` top-level config field (macOS-only): paths stat()-ed once on cold-start to surface Files-and-Folders TCC popups synchronously after a binary redeploy. Adhoc-signed binary cdhash changes per build invalidate existing TCC grants; pre-warming under operator presence avoids unattended popup deadlocks where launchd-spawned plugin children block on first protected-path access. No-op on non-darwin and when paths empty. Fail-soft: missing paths log Warn and continue.

## 2026-04-21
- Expose the core boundary used to decide when to retry operations.

## 2026-04-20
- Implement explicit durability for Hickey Sprint 3.
- Fix Docker fixture state isolation to prevent cross-test leakage.

## 2026-04-18
- Stabilize the reload lifecycle and crash-recovery behavior to reliably mark jobs dead and trigger job-failed hooks.
- Strengthen plugin fingerprinting and config locking across config, CLI, and snapshots, including wiring into lock/boot/reload and improving migration checks.
- Add job lineage facts and ensure unavailable plugin fingerprints are recorded in snapshots.
- Add reload lifecycle Docker fixture and fix webhook fixture checksum preflight, while expanding CLI plugin fingerprint wiring tests.
- Apply Go formatting cleanup, fix gosec findings, add developer deck, and simplify agent guidance.

## 2026-04-10
- Tests now explicitly set janitor_interval in the workspace-lifecycle fixture to make test behavior predictable.
- Removed the obsolete workspace migrate command from the CLI to reduce confusion.
- Add .beads-credential-key to gitignore to prevent committing credential files.

## 2026-04-09
- Fix: workspace janitor now runs even if the config omits the workspace section.
- Cleanup: remove unused logging wrappers (package-level Info/Debug/Warn/Error) and helper functions.
- Cleanup: remove a set of unused config/lock/plugin helpers and accessors (VerifyScopeFiles, ConfigFiles.OperationalFiles, PIDLock.Path, GenerateChecksums, Discover and DiscoverMany wrappers).
- Chore: add deadcode as a project tool and bump Go to 1.25.
- Docs: add workspace cleanup directives to CONFIG_REFERENCE and workspace cleanup fields to the config JSON schema.

## 2026-04-08
- Rewrite CLAUDE.md to reflect rc1 state and update the skill CLI reference.

## 2026-04-01
- Add PLUGIN_DIAGNOSTICS.md: structured runbook for plugin health investigation.
- Fix two failing tests in the dispatch hook_test.go to improve test reliability.

## 2026-03-31
- Add MACOS_INSTALLATION.md with the first Mac (darwin/arm64) installation guide.

## 2026-03-28
- Use 'dev' as the default version instead of a hardcoded fallback.
- Remove all protocol v1 references from docs and tests.

## 2026-03-26
- Dedupe window shortened from 24h to 2h to keep deduping more current.
- Reverts undo earlier dispatch changes: routing of job.completed and job.failed through the pipeline engine and the added message field for job.completed.
- Added on-hook DSL support: new on-hook DSL keyword and notify_on_complete config, plus test fixtures for NextHook routing and fixes to hook envelopes and signals, with docs/spec updates.
- Improve plugin and webhook tests: warn when plugin directories lack manifest.yaml and update webhook-ingress fixture checksums path.
- Harden lifecycle hook routing and docs: update routing spec for lifecycle hooks, update JSON schemas for on-hook DSL and notify_on_complete config, and fix pipeline remap validation with accompanying docs.

## 2026-03-25
- Dispatch now treats DedupeDropError as a skip rather than a routing failure, and pipeline steps with no events now emit a synthetic ductile.step.succeeded.
- sys_exec now supports configurable success_exit_codes and emits events when exit codes match emit_event_on_exit_codes.
- Documentation and schemas are aligned with the implementation, and CLI help output and LLM skill manifests are synchronized with the code.
- The dispatcher now includes the plugin name and the message in the job.failed payload.
- API adds /config/view endpoint with sensitive value redaction and updates test port to 9001, and config tests are updated to match the WebhookEndpoint struct.

## 2026-03-07
- Discord notify plugin now supports an incoming webhook, adds a poll command with configurable poll_message, and includes a default_message fallback plus a User-Agent fix.
- JSON Schemas for config, plugins, and pipelines YAML are added, with multi-file-merge support and validation for max_workers and plugin parallelism.
- Repo sync and discovery are hardened to prefer SSH remotes, use per-repo dedupe keys for concurrency, and exclude forks, with RO_GITHUB_TOKEN used for repo sync.
- Weekly changelog generation and auto-derived versioning are added via scripts/version.sh and Makefile.
- Testing and reliability improvements include stress-test suites, API and webhook mocks, and fixes to lint and webhook fixtures.

## [Unreleased] — Weekly Development Summary (Last 7 Days)

This entry captures the most significant changes from the past week of development. It is intentionally expansive so that it remains useful as older Git history scrolls out of view.

### Highlights
- Introduced **YouTube playlist ingestion** with an end-to-end transcript → Fabric → markdown workflow for Astro summaries.
- Implemented **CLI support for plugin list/run**, enabling direct API-backed invocation.
- Delivered **skills/capability discovery improvements** for AI operators and external tooling.
- Shipped **scheduler upgrades** (command-based schedules, dedupe cadence fixes, next-run persistence).
- Completed **RC1 cleanup** with removal of legacy config and auth surfaces.

### Added
- **String operators for if conditions**: `contains`, `startswith`, `endswith`, and `regex` (full-string match) are now supported in pipeline step predicates.
- **youtube_playlist plugin** for polling YouTube playlists and emitting dedupe-safe events.
- **Plugin CLI actions**:
  - `ductile plugin list` (API-backed `/plugins`)
  - `ductile plugin run` (API-backed `/plugin/{name}/{command}`)
- **sys_exec plugin** for safe shell execution with env-only payload propagation.
- **file_watch / folder_watch plugins** with optional per-file events and dedupe propagation.
- **Skills registry enhancements**:
  - Expanded `/skills` index (pipelines + plugins)
  - AI-native skills manifest format improvements
- **Plugin instance aliasing** to allow per-instance configuration and reuse.
- **TUI watch improvements** with metadata header and richer detail panels.

### Changed
- **Scheduler**:
  - Command-based schedules (beyond implicit `poll` only).
  - Persisted `next_run` timestamps and improved countdown visibility in TUI.
  - Dedupe cadence fixes for frequent schedules.
  - Startup validation for scheduled commands.
- **Config and discovery**:
  - Plugin discovery moved earlier in startup for better preflight diagnostics.
  - Config logging now includes discovered/configured/enabled plugin counts.
- **Docs/identity**:
  - README/identity refreshed and aligned with integration-engine focus.
  - Host-local deployment guide added.
  - Webhooks and cookbook documentation expanded.

### Fixed
- YouTube playlist prompt formatting crash (metadata braces in prompt templates).
- Token loading and webhook token resolution in include mode.
- Multiple manifest command metadata omissions (command type fields added).

### Security / Hardening
- Symlink policy enforcement for config and plugins.
- Queue list hardening with safer where-clause builder.
- Secret redaction and reduced permissions for sensitive paths.
- gosec cleanup: suppressions and fixes consolidated.

### Breaking / RC1 Cleanup
- **Removed legacy schedule field** (`schedule:`) in favor of `schedules:`.
- **Removed legacy API auth key** (`api.auth.api_key`), enforcing scoped tokens.
- **Removed legacy `/trigger` endpoint**.
- **Removed legacy config discovery fallback**.
- **Webhook secrets now require `secret_ref`** (inline `secret` removed).
- **Manifest command objects required**; missing command types now hard-fail.
- **Checksums v1 dropped**.
- **CLI backward-compat aliases removed**.

### Additional Notables
- Added default output directory support in `file_handler`.
- Auto-detect JS runtime for `yt-dlp` in `youtube_transcript`.
- Documentation cleanup: removed RFC/agent scaffolding and deprecated artifacts.

---

If you want this entry split into versioned releases (e.g., `1.0.0-rc.1`), say the word and I’ll restructure it.
