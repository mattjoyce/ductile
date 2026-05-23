# Plugin Diagnostics

A structured process for diagnosing plugin health in a running Ductile instance. Covers triage, job history analysis, failure inspection, manual testing, and remediation.

---

## Quick Triage (3 commands)

Run these first. They answer "is anything broken right now?"

```bash
# 1. Gateway and overall health
ductile system status

# 2. Recent failures across all plugins (last 24h)
ductile job logs --from $(date -u -d '24 hours ago' --rfc-3339=seconds | tr ' ' 'T') \
  --limit 200 --json | \
  python3 -c "
import json,sys
d=json.load(sys.stdin)
logs=d['logs'] or []
fails=[l for l in logs if l['Status']=='failed']
print(f'Total jobs: {d[\"total\"]}  Failures: {len(fails)}')
for f in fails:
    print(f'  {f[\"Plugin\"]:25} {f[\"CreatedAt\"][:16]}  {f[\"LastError\"]}')
"

# 3. Run a specific plugin's health check
ductile plugin run <plugin-name> health
```

If step 2 shows failures, move to **Per-Plugin Investigation** below. If step 3 fails, move to **Configuration Issues**.

---

## 1. Per-Plugin Job History

Get a summary of a plugin's recent activity:

```bash
FROM=$(date -u -d '24 hours ago' --rfc-3339=seconds | tr ' ' 'T')

ductile job logs --from $FROM --plugin <plugin-name> --limit 200 --json | python3 -c "
import json,sys
from collections import Counter
d=json.load(sys.stdin)
logs=d['logs'] or []
statuses=Counter(l['Status'] for l in logs)
print(f'Total: {d[\"total\"]}  Statuses: {dict(statuses)}')
if logs:
    print(f'Oldest: {logs[-1][\"CreatedAt\"][:16]}')
    print(f'Newest: {logs[0][\"CreatedAt\"][:16]}')
for l in logs:
    if l['Status'] == 'failed':
        print(f'  FAIL {l[\"CreatedAt\"][:16]}  {l[\"LastError\"]}')
"
```

**Status meanings:**

| Status | Meaning |
|--------|---------|
| `succeeded` | Plugin ran and returned `status: ok` |
| `failed` | Plugin returned `status: error` or timed out |
| `skipped` | A job was explicitly skipped by orchestration logic; uncommon for Sprint 6 `if:` pipelines because they now branch through `core.switch` instead |
| `retrying` | Core retry policy queued another attempt after a retryable failure |

A high `succeeded` count for `core.switch` is normal for conditional pipeline steps. Only `failed` warrants investigation.

---

## 2. Inspect a Failed Job

Get the full result payload and pipeline lineage for a specific job:

```bash
# Get job IDs for failed runs
ductile job logs --from $FROM --plugin <plugin-name> --limit 50 --json | python3 -c "
import json,sys
d=json.load(sys.stdin)
for l in (d['logs'] or []):
    if l['Status'] == 'failed':
        print(l['JobID'], l['CreatedAt'][:16], l.get('LastError',''))
"

# Inspect the full result (including plugin stdout, error detail)
ductile job logs --from $FROM --plugin <plugin-name> --limit 50 --json --include-result | python3 -c "
import json,sys
d=json.load(sys.stdin)
for l in (d['logs'] or []):
    if l['Status'] == 'failed':
        print('=== FAILED JOB', l['JobID'][:8], l['CreatedAt'][:16], '===')
        print(json.dumps(l.get('Result'), indent=2))
        if l.get('Stderr'):
            print('STDERR:', l['Stderr'])
"

# Follow the pipeline lineage (what triggered this job, what did it trigger)
ductile job inspect <job-id>
```

**What to look for in `job inspect`:**
- **Hops** — which pipeline step triggered this job and what baggage it carried
- **Baggage** — the payload passed down the chain; missing keys here often explain `missing field` errors

---

## 3. Manual Plugin Invocation

Test a plugin end-to-end without waiting for a trigger:

```bash
# Run with default/no payload
ductile plugin run <plugin-name> handle

# Run with a payload (useful for handle commands that need input)
ductile api /plugin/<plugin-name>/handle -X POST \
  -b '{"payload": {"message": "test message"}}'

# Run the health command to verify config
ductile plugin run <plugin-name> health
```

The `health` command validates the plugin's configuration (e.g. required API keys, webhook URLs) without performing any side effects. Use it after changing config.

---

## 4. Configuration Issues

### Check plugin is registered

```bash
ductile config show | grep -A 10 'plugins:'
ductile config get plugins.<plugin-name>.enabled
```

### Validate full config integrity

```bash
ductile config check
```

This catches: missing fields, integrity hash mismatches, unreachable entrypoints.

### Verify the manifest

Each plugin directory must contain a valid `manifest.yaml`. If a plugin is silently absent from scheduling, check:

```bash
ls <plugin-dir>/manifest.yaml
cat <plugin-dir>/manifest.yaml
```

The manifest declares supported `commands`, required `config_keys`, and the `entrypoint`. A missing or malformed manifest causes the plugin to be skipped at startup with no error.

### After any config change

```bash
ductile config lock    # update integrity hashes
ductile config check   # verify
ductile system reload  # apply without restart
```

---

## 5. Scheduled Plugin Not Firing

If a plugin is scheduled but no jobs appear in the logs:

1. **Confirm the schedule is configured:**
   ```bash
   ductile config get plugins.<plugin-name>.schedules
   ```

2. **Check cron expression and timezone** — Ductile cron runs in the system timezone unless overridden. A schedule of `0 7 * * * Australia/Sydney` fires at 07:00 AEST, which is 20:00 or 21:00 UTC depending on DST.

3. **Check the plugin is enabled:**
   ```bash
   ductile config get plugins.<plugin-name>.enabled
   ```

4. **Look for startup errors in the journal:**
   ```bash
   journalctl --user -u ductile-local --no-pager -n 100 | grep -i 'error\|plugin'
   ```

---

## 6. Pipeline-Triggered Plugin Not Firing

If a plugin is supposed to run when an upstream job completes but doesn't:

1. **Confirm the upstream job actually ran and succeeded:**
   ```bash
   ductile job logs --from $FROM --plugin <upstream-plugin> --limit 10 --json | \
     python3 -c "import json,sys; d=json.load(sys.stdin); [print(l['Status'], l['CreatedAt'][:16]) for l in (d['logs'] or [])]"
   ```

2. **Check the pipeline `if:` condition** — Sprint 6 compiles `if:` into an internal `core.switch` hop. If the condition evaluates false, Ductile bypasses the gated step and routes the false branch onward. Inspect the upstream payload and the `core.switch` result to confirm what matched.

3. **Check event routing:**
   ```bash
   ductile config show | grep -B2 -A15 'on: <upstream-plugin>'
   ```

4. **Inspect the upstream job for baggage** — the downstream plugin receives the upstream job's baggage as its payload. A `missing field` error downstream usually means the upstream didn't emit that field.
   ```bash
   ductile job inspect <upstream-job-id>
   ```

---

## 7. Circuit Breaker

Ductile tracks consecutive plugin failures and can open a circuit breaker to stop retrying a broken plugin. Signs:

- Plugin stopped firing entirely after a run of failures
- `system status` shows plugin in `open` circuit state

```bash
# Check circuit state
ductile system breaker <plugin-name>

# Machine-readable breaker state and recent transition facts
ductile system breaker <plugin-name> --json

# Reset after fixing the underlying issue
ductile system reset <plugin-name>
```

Do not reset without first understanding why the circuit opened.

---

## 8. Reconciliation Check

To verify that a plugin's fired jobs match expected outputs (e.g. confirming notifications landed):

```bash
FROM=$(date -u -d '12 hours ago' --rfc-3339=seconds | tr ' ' 'T')

ductile job logs --from $FROM --plugin <plugin-name> --limit 200 --json | python3 -c "
import json,sys
from collections import Counter
d=json.load(sys.stdin)
logs=d['logs'] or []
statuses=Counter(l['Status'] for l in logs)
print(f'Window: last 12h  Total: {d[\"total\"]}')
print('Breakdown:', dict(statuses))
"
```

Cross-reference the `total` count against expected frequency:
- A `poll` plugin on a 15-minute schedule should produce ~48 jobs per 12h
- An event-driven plugin should have jobs proportional to the events that triggered it
- Gaps (fewer jobs than expected) can indicate scheduler drift, missed events, or a silent failure in an upstream trigger

---

## Common Failure Patterns

| Error | Likely Cause | Fix |
|-------|-------------|-----|
| `missing repo_path/path` | Upstream step didn't emit the required baggage field | Check upstream plugin result and pipeline config mapping |
| `missing webhook_url` | Plugin config lacks required key | Add key to plugin config, `config lock`, `system reload` |
| `timeout` | Plugin exceeded deadline | Increase `timeout:` in plugin config or fix slow external call |
| `invalid JSON input` | Plugin received malformed stdin | Check upstream payload construction; look at `Stderr` in job log |
| `HTTP 4xx` from external API | Auth or request format issue | Check plugin config (tokens, endpoint URLs); run `health` command |
| `HTTP 5xx` from external API | Upstream service down | Transient — check plugin error facts and core retry events; check external service |
| `exit code 1` (sys_exec) | Shell command failed | Check `Stderr` in job log for command output |

---

## Reference: Key Commands

```bash
# Gateway health
ductile system status
ductile system watch                          # live TUI

# Plugin testing
ductile plugin run <name> health
ductile plugin run <name> handle
ductile api /plugin/<name>/handle -X POST -b '{"payload": {...}}'

# Job history
ductile job logs --plugin <name> --from <RFC3339> --limit 200 --json
ductile job logs --plugin <name> --from <RFC3339> --limit 200 --json --include-result
ductile job inspect <job-id>

# Config
ductile config check
ductile config show
ductile config get plugins.<name>.<key>
ductile config lock && ductile system reload

# Circuit breaker
ductile system breaker <plugin-name>
ductile system reset <plugin-name>

# Logs (systemd)
journalctl --user -u ductile-local --no-pager -n 50 | grep ERROR
```

---

## Stopwatch — answering "is ductile slow, or is my plugin slow?"

The dispatcher captures per-invocation timing automatically. Plugins do not
instrument themselves; the supervisor measures them. Each plugin invocation
emits one immutable `stopwatch.Record` into the request context under the
reserved key `ductile_stopwatch`.

A Record carries everything needed to attribute time:

| Field             | Meaning                                                       |
|-------------------|---------------------------------------------------------------|
| `plugin_id`       | Plugin name                                                   |
| `step_name`       | Pipeline step ID, when known                                  |
| `attempt`         | 1-based retry counter                                         |
| `enter_wall_ns`   | Wall-clock entry timestamp (correlation only)                 |
| `exit_wall_ns`    | Wall-clock exit timestamp (correlation only)                  |
| `dur_ns`          | Monotonic spawn duration — the number to compare              |
| `runtime_pre_ns`  | Dispatcher work between request build and spawn               |
| `runtime_post_ns` | Dispatcher work between spawn return and record write         |
| `status`          | `ok`, `err`, `timeout`, or `capture_error`                    |
| `subs`            | Optional plugin-emitted sub-spans (capped at 32 per Record)   |

### Attributing the bottleneck

For one job, durations are local. For a pipeline of `N` steps:

```
plugin_time  = Σ dur_ns          (across all step records)
wall_time    = max(exit_wall) − min(enter_wall)
gateway_time = wall_time − plugin_time
```

- If `gateway_time` is large compared to `plugin_time`, the bottleneck is
  **inside ductile** — dispatch, routing, or the queue.
- If a single `plugin_id` dominates `plugin_time`, that plugin is the
  bottleneck.
- If `runtime_pre_ns` or `runtime_post_ns` grows without `dur_ns` growing,
  the cost is in the dispatcher's pre/post work, not the plugin spawn.

### Optional sub-spans

Plugins may emit internal phases (`db_query`, `http_call`) in their response
under `ductile_stopwatch_subs` (see PLUGIN_DEVELOPMENT.md). The dispatcher
caps at 32 entries per Record and drops the rest with a single warn-log;
malformed shapes are dropped silently. Sub-spans are advisory; the Record
itself is always present regardless.

### Status semantics

`status` is a closed set. `capture_error` indicates a defect in the
supervisor itself and should never appear in production — it exists so
that timing data is still emitted in the worst case rather than silently
disappearing.
