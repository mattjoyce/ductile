---
id: 154
status: todo
priority: Normal
tags: [logging, observability, operator, reload, deploy]
depends_on: []
---

# Logging is too verbose and partly broken

> Do not tag, do not merge, do not push. The principal merges.

Ideal state: INFO is a record an operator (mostly an LLM agent, `docs/AUDIENCES.md` cell 8) can
read for a day without drowning. One line per job terminal transition, detail at DEBUG or in the
database. Plugin log levels honoured. Request logging at DEBUG for reads. The level changes on
reload. `log_format` works. Growth is bounded on every sink.

## Problem now

- **The level is fixed for the process life.** `internal/log/logger.go:17-38`: `Setup` runs under
  `sync.Once`; `slog.NewJSONHandler(os.Stdout, …)` is hard-coded at `34`. `buildRuntime` calls
  `log.Setup(cfg.Service.LogLevel)` at `cmd/ductile/runtime.go:411` on boot and on every reload;
  every call after the first is a no-op. The `reload-lifecycle` fixture edits `log_level: info` to
  `debug` and reloads twice (`test/fixtures/docker/reload-lifecycle/run.sh`) and never asserts the
  level changed; it would fail if it did.
- **`service.log_format` is dead.** Field `types.go:57`, default `"json"` `types.go:847`, merged
  `loader.go:506`, defaulted `loader.go:673`, recorded in config snapshots
  (`internal/configsnapshot/snapshot.go:374`), documented (`docs/CONFIG_REFERENCE.md:134`,
  `docs/MACOS_INSTALLATION.md:443`). Nothing reads it (`rg LogFormat` over non-test code: only
  those sites). It is not validated either; `log_level` is (`loader.go:792-794`).
- **Volume, Mac** (`~/Library/Logs/ductile-local.log`, measured 2026-11-01): 1,287,261,459 bytes,
  5,760,297 lines, never rotated (launchd `StandardOutPath` and `StandardErrorPath` both point at
  it; plist `com.mattjoyce.ductile-local`, `ThrottleInterval` 5, `KeepAlive` true). Lines by month:
  Apr 79,173; May 104,289; **Jun 5,293,191**; Jul 52,453; Aug 40,671; Sep 36,225; Oct 40,133.
- **June was a KeepAlive crash loop on a boot refusal, not a restart loop of a running gateway.**
  June has 79,759 `ERROR integrity check failed (admission.verify_integrity_on_boot)`
  (`runtime.go:548`), 79,772 `config loaded`, 4,307,613 `loaded plugin`, 6 `reloading config`, and
  only 13 `ductile starting`, because that line (`runtime.go:591`) comes after admission. Peak days
  2026-06-13 (1,083,745 lines) and 06-16 (1,082,640). Each refused boot logged plugin discovery
  (`internal/plugin/discovery.go:140` through the logger adapter at `runtime.go:441`: one
  `loaded plugin` per plugin, 54 here), five `config file` lines (`477`), `config loaded` (`463`)
  and `config summary` (`487`) before the gate said no. The boot preamble is about 60 INFO lines
  ahead of the admission decision, and a supervisor loop multiplies it.
- **Steady state, Mac, last 200,000 lines:** `plugin log` 40,613; `plugin executing` 36,841;
  `job started` 36,841; `job completed` 36,744; `Enqueued scheduled job` 36,521; `http request`
  8,002; `loaded plugin` 1,295; `Skipped scheduled job` 1,098. Levels: INFO 199,594, WARN 334,
  ERROR 63. About 5.1 lines per scheduled job; jobs are 93% of the stream. The 8,002 requests are
  four paths at equal cadence (`/topology` 1,930, `/config/view` 1,929, `/jobs` 1,928,
  `/scheduler/jobs` 1,926): a dashboard poller, ductile-webui by inference (its repo is not on this
  Mac). `/healthz` is 33.
- **Steady state, Thinkpad, 24 h of `journalctl -u ductile`:** 2,332 lines; `plugin log` 634 of
  which 88 carry `"level":"debug"`; `plugin executing`, `job started`, `job completed` 391 each;
  `Enqueued scheduled job` 389; `http request` 14. Two WARNs, once each per boot:
  `privsep: account is CREDENTIALED (trusted) …` (`runtime.go:770`) and `privsep SECURITY:
  credentialed (trusted) account has a host root side-door …`
  (`internal/dispatch/sidedoor_audit.go:159`, emitted at `235`).
- **Plugin log levels are flattened.** `internal/dispatch/dispatcher.go:596-598`:
  `jobLogger.Info("plugin log", "level", entry.Level, …)` for every entry regardless of
  `entry.Level` (`internal/protocol/types.go:87-90`: debug | info | warn | error). On the Mac the
  last 200k lines hold 3,842 plugin `warn` and 113 plugin `error` entries printed at INFO.
- **Every request is INFO.** `internal/api/server.go:424-437` `loggingMiddleware`, installed
  globally at `281`, so `/healthz` is logged too; `internal/webhook/server.go:130-145` same shape.
- **Call sites (non-test):** Debug 28, Info 74, Warn 54, Error 180. Info by file:
  `cmd/ductile/runtime.go` 25, `internal/dispatch/dispatcher.go` 13,
  `internal/scheduler/scheduler.go` 12, `internal/webhook/server.go` 4, `internal/router/engine.go`
  4, `internal/api/server.go` 3.
- **What is already durable** (so the cut lines lose nothing): `job_queue` (status, attempt,
  submitted_by, created/started/completed_at; `internal/storage/schema.sql:5`), `job_transitions`
  (from, to, reason; `schema.sql:48`, written at `internal/queue/queue.go:78`), `job_attempts`
  (`schema.sql:60`), `job_log` (status, `result` = the raw plugin stdout JSON including `logs[]`,
  `last_error`, `stderr`, snapshot ids; `schema.sql:141`, `queue.go:1711-1792`). So every `plugin
  log` line is already in `job_log.result.logs`, and every lifecycle line has a row. Read back with
  `ductile job inspect` and `job logs` (`cmd/ductile/job.go:33,141`), `GET /job/{id}`,
  `/job-logs`. Not durable: the events hub is an in-memory ring of 100 (`internal/events/hub.go`),
  so `scheduler.skipped` leaves no trace except its log line.
- **Sinks** (verified this session): Mac, launchd file, unbounded. Thinkpad, journald with
  `SystemMaxUse=500M` (`/etc/systemd/journald.conf`; 472.1M in use), bounded. Unraid, Docker
  logging driver; the repo `docker-compose.yml:31-35` sets `json-file` `max-size: 10m`,
  `max-file: 3`; the live compose at `/mnt/user/appdata/ductile/` was not reachable from the Mac and
  is unverified.

## Slices

### Slice 1: the level is live, and `log_format` is real

- `internal/log`: a package-level `slog.LevelVar` and an atomically swapped handler. `Setup(level,
  format string)` installs on first call and on later calls sets the level and, if the format
  changed, swaps the handler. `format` is `json` or `text` (`slog.NewTextHandler`); validate it in
  `validate()` beside `log_level` (`loader.go:792`). `runtime.go:411` passes both.
- Why implement rather than remove: the key is in two docs and in example configs; removing it
  makes `log_format: json` an ignored-key warning today and, after card 153's strict decode, a boot
  error. `text` is what a human tailing a file wants.
- Tests: unit test that a logger obtained by `WithComponent` before the change emits DEBUG after
  `Setup("debug", …)`; invalid format rejected by `config check`; `reload-lifecycle` gains the
  assertion it lacks: after the `info → debug` reload, `ductile.log` contains a `"level":"DEBUG"`
  record (`Scheduler tick`, `scheduler.go:142`, fires every tick). That assertion is the live-only
  proof and fails on today's binary.
- Exit: the fixture assertion passes; `log_format: text` renders text; CONFIG_REFERENCE documents
  `json | text`; CHANGELOG. Version: minor.

### Slice 2: one INFO line per job terminal transition

- To DEBUG: `job started` (`dispatcher.go:293`), `plugin executing`
  (`internal/dispatch/subprocess_executor.go:71`), `Enqueued scheduled job` (`scheduler.go:777`),
  `Skipped enqueue due to dedupe hit` (`scheduler.go:758`), `enqueued hook job`
  (`dispatcher.go:1982`), `enqueued routed job` and `routing event resulting in child jobs`
  (`dispatcher.go:1017-1094` region). Each has a `job_queue`/`job_transitions` row.
- Stays INFO: `job completed` (`dispatcher.go:1833`), which gains `attempt` (the `jobLogger`
  already carries `job_id`, `plugin`, `command`); `Skipped scheduled job` (`scheduler.go:607`), the
  only durable trace of a skip; circuit-breaker transitions (`scheduler.go:894, 964`); every WARN
  and ERROR path untouched.
- Tests: a dispatcher test with a capturing `slog.Handler` asserts exactly one INFO record per job
  and that the rest are DEBUG with the same `job_id`; a scheduler test asserts enqueue at DEBUG and
  skip at INFO.
- Exit: from the measured windows, Thinkpad 24 h drops from 2,332 to about 1,150 lines and the Mac
  200k window to about 80k (computed from the counts above; re-measure after deploy and record).
  Lane: `go test`; no fixture change.

### Slice 3: plugin log levels honoured

- `dispatcher.go:596-598`: map `entry.Level` to the slog level; unknown strings log at INFO with
  `level_raw`. Do not emit a custom `level` attribute: it duplicates slog’s built-in JSON key and
  causes ordinary JSON parsers to overwrite the actual record severity with the plugin severity. The entries stay in `job_log.result.logs` unchanged, so a debug line that leaves the
  stream is still in `job inspect`.
- Tests: table over `debug|info|warn|error|bogus`; a test that `job inspect` output for a completed
  job includes the plugin's log entries (if `internal/inspect/report.go` does not surface them
  today, add that in this slice; it is the "detail in the DB" half of the ideal state).
- Effect: Thinkpad loses 88 of 634 plugin lines at INFO; the Mac loses none (0 debug entries in the
  last 200k) but 3,842 plugin `warn` and 113 `error` entries become visible at their own level. The
  remaining Mac volume after slices 2-3 is plugin INFO lines (about half): that is the plugin
  authors' choice and is durable; a per-plugin log floor is a possible follow-up, not this card.
- Exit: `go test`; PLUGIN_DEVELOPMENT states that `logs[].level` is honoured. Lane: `go test`.

### Slice 4: the boot preamble is a summary

- To DEBUG: `loaded plugin` (`discovery.go:140`, via the adapter at `runtime.go:441`),
  `pipeline registered` (`runtime.go:627`), `plugin alias registered` (`458`), the per-file
  `config file` (`477`). Keep and enrich: `config loaded` (`463`) gains `files` (the list),
  `plugin discovery complete` (`601`) gains `roots`, `pipeline discovery complete` (`625`) stays,
  `config summary` (`487`) gains `aliases`. `failed to load plugin` stays WARN.
- Effect: a refused boot logs about 8 lines, not about 60. The June loop would have been 640k
  lines, not 5.3M; the ERROR line that named the cause would have been one in eight, not one in
  sixty.
- Tests: a boot test with a capturing handler over a fixture with 3 plugins and 2 includes asserts
  the INFO count is at most N (pick N from the enriched summary lines). Native fixture:
  `boot-refuses-bad-config` asserts each refusal output is under 20 lines.
- Exit: `go test`; the fixture assertion passes. Lane: native.

### Slice 5: request logging at DEBUG for reads

- `api/server.go:429`: GET, HEAD and OPTIONS with status < 400 log at DEBUG; mutating methods and
  any status >= 400 stay INFO. `/healthz` falls under the rule. No new config key: the rule is
  the policy, and an opt-in flag would be one more dead setting of the `log_format` kind.
- `webhook/server.go:137` stays INFO: ingress is an audit event (who signed what, when) and is
  low volume (0 in the Thinkpad day; the Mac shows none in the window).
- Tests: `httptest` with a capturing handler: `GET /healthz` 200 → DEBUG; `GET /jobs` 200 → DEBUG;
  `GET /jobs` 401 → INFO; `POST /plugin/x/poll` 202 → INFO; `POST /system/reload` → INFO.
- Effect: the Mac's 8,002 request lines per 200k window become about 200 (the `stopwatch` POSTs
  and errors).
- Exit: `go test`; OPERATOR_GUIDE §Logging (`docs/OPERATOR_GUIDE.md:170`) gets a table of what
  INFO means after this card. Lane: `go test`.

### Slice 6: bounded growth, per sink (decision, docs, operator verification)

Decision: ductile keeps stdout as its only sink and does not add in-process rotation.

- Seq / Vector (Unraid): Ductile stdout is also shipped to Seq, so Docker rotation does not bound
  this downstream store. Verify and record effective Seq retention separately. Check that Vector
  maps the corrected unique slog `level` to Seq severity and preserves plugin messages; verify new
  events arrive after slices 1–5. The old “healthz every 30 s” freshness heuristic becomes invalid
  when read requests move to DEBUG: update that operator guidance. This is sink verification, not
  a new logging integration.
- journald (Thinkpad): bounded already, `SystemMaxUse=500M`, verified. Nothing to do; document the
  check (`journalctl --disk-usage`).
- Docker (Unraid): bounded when the compose sets `logging.options.max-size`/`max-file`, as the repo
  compose does. Add to `docs/runbooks/unraid-docker-upgrade.md` a step: confirm the live compose
  sets it, and the `docker inspect` line that shows it. The live file was not verified.
- launchd (Mac): `StandardOutPath` is a file launchd opens and hands to the process; a
  rename-based rotation leaves the process writing to the renamed inode until it restarts. That is
  the usual Unix fd behaviour; it was not tested on this Mac and the slice tests it. Ship
  `deploy/newsyslog.d/ductile-local.conf` (size-trigger, keep a few, the `R` flag running
  `launchctl kickstart -k gui/$UID/com.mattjoyce.ductile-local`) and document it in
  MACOS_INSTALLATION and DEPLOYMENT with a one-time `: > ~/Library/Logs/ductile-local.log` for the
  1.29 GB file.
- Why not ductile's job: a file sink means a log path per posture with its own owner and mode
  (under privsep the gateway writes it as `ductile`; on the Mac as the login user), either a
  dependency or owned rotation code while card 148 is removing dependencies, and a second place the
  level and format must be plumbed. After slices 2-5 the Mac steady state is roughly 10k lines a
  month; the one real blow-up was a crash loop, which slice 4 shrinks about eightfold. Rotation is
  insurance, not the fix. Revisit if card #95 (macOS enforce) changes the Mac launch shape.
- One more line at boot, INFO: `logging configured` with `level`, `format`, `sink=stdout`, so the
  posture of the log itself is in the log.
- Exit: the sample conf and docs are in the PR; the principal runs `newsyslog -F` once on the Mac
  and pastes the result (rotated file, gateway restarted, new file receiving lines) into this card.
  No `go test` can prove this one.

Not changed: the privsep boot WARNs stay WARN. They are the per-boot posture record the side-door
audit was designed to give (#111), two lines per boot.

## Done

- Reload changes the level (fixture-proven); `log_format: text` works and is validated.
- Thinkpad: a 24 h window at INFO is about 1,000 lines for the same job count; no plugin `debug`
  entry appears at INFO; `job inspect` shows a plugin's log entries.
- Mac: the 200k-line window has one lifecycle line per job and under 300 request lines.
- Unraid: inspect the running container’s effective logging driver and rotation limits (not just
  compose source), measure a 24 h baseline by message and true record severity, then repeat after
  deployment. Verify plugin levels, read-request logging, and successful jobs. Record container
  uptime/restarts, log size and the effective retention settings. No host is signed off from another
  host’s result; retain the existing Mac and Thinkpad checks too.
- A refused boot prints under 20 lines.
- The newsyslog sample is in the repo and was run once on the Mac with the result recorded here.
- Merge gates (AGENTS.md §9) pass.

## History

- 2026-11-01: carded. Measured both hosts; found the June volume was a launchd KeepAlive loop on
  `integrity check failed` (79,759 refusals), each logging the full plugin list before the gate.

## Narrative

- 2026-11-01: Principal requested that this card explicitly cover Thinkpad and Unraid alongside
  the Mac. Read-only Thinkpad recheck over `ssh matt@thinkpad`: v1.1.0, healthy, 40 plugins,
  system service active, NRestarts=0 for the current activation. Previous 24 h: 2,342 lines,
  391 each of job started/plugin executing/job completed, 389 scheduled enqueues, 634 plugin
  logs, 22 HTTP requests and 18 scheduled skips. journald stdout/stderr, SystemMaxUse=500M,
  472.1M total journal use (host-wide, not Ductile alone). Config log_level is info.
- 2026-11-01: Confirmed a severity-field collision in dispatcher.go: plugin records are emitted
  via Info with a second `level` attribute. Standard JSON decoding reports the plugin level
  (546 info and 88 debug entries in the Thinkpad window), hiding the original INFO severity.
  Slice 3 must remove this duplicate key as well as honouring levels.
- 2026-11-01: Unraid 192.168.20.4 is reachable, but batch SSH as root rejects available keys
  from both the Mac and Thinkpad. Live log driver, limits, volume and restart behavior remain
  unverified; repo compose settings are not evidence of running container settings. Requested
  the existing access method. No host configuration, restart, rotation or runtime code changed.

### Unraid live baseline — 2026-11-01

Access succeeded via Thinkpad using `/mnt/Projects/unraid_admin/unraid_cmd.sh`, as documented in
that repo’s CLAUDE.md. Credentials remain in the existing secret file; no credentials were printed.

- Gateway v1.1.0, healthy, 80 plugins, no open circuits, restart count 0; container started
  2026-10-28T11:49:57Z.
- Effective `docker inspect` logging config: json-file, max-size=10m, max-file=3. Rotation is real:
  current log about 5.5M, two rotated logs about 9.6M each at inspection. This supersedes the
  earlier unverified-live-config statement; no Docker retention change is required for Ductile.
- Previous 24 h `docker logs`: 33,063 lines / 8,561,511 application-text bytes. Actual slog severity
  (first level key, before plugin attribute): INFO 33,031; WARN 26; ERROR 6.
- Messages: plugin log 7,818; job started 5,136; job completed 5,134; plugin executing 5,133;
  scheduled enqueue 4,254; HTTP requests 2,871, all /healthz; triggering pipeline 880;
  enqueued routed job 880; routing event resulting in child jobs 877. These are a time window,
  not a reconciled job cohort, so starts and completions need not match.
- Plugin severities: info 7,792; error 24; debug 2. All currently emitted through slog.Info.
- Existing WARN/ERROR records include plugin failures and stdout capture-limit failures; retain
  these signals. Investigating their causes is outside this logging card.
- Additional live sink: seq-vector ships Ductile Docker logs to Seq. Both containers also have
  json-file 10m × 3 logs. Seq health returned 200, data store currently 92M on disk2. Its event
  retention is not established: the existing Read API key gets 403 on /api/retentionpolicies.
  Do not infer a retention cap from store size, Docker limits or licence limits.
- Existing `Seq Log Server Container.md` documents the duplicate-level workaround and a freshness
  check relying on INFO /healthz logs. Slice 3/5 deployment must check severity parsing and replace
  that stale heuristic. No live settings or services were changed during this inspection.
