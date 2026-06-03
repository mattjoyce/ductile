---
name: ductile-rca
description: >
  Root cause analysis for ductile incidents. Extends the global `rca` skill's
  discipline with ductile's specific evidence taxonomy — job_logs, transitions,
  plugin_facts, baggage, selfcheck, circuit breaker state, queue state — and
  applies them in non-faulty-entity-first order (Armstrong). Use when the user
  reports a symptomatic ductile failure: "ductile is stuck/hanging", "plugin
  broken in ductile", "pipeline stuck", "job won't complete", "circuit breaker
  tripped", "queue not draining", "reload hanging", "baggage missing", "fact
  wrong", "config rejected by lock", "gateway won't start", "selfcheck red",
  "events not routing". Step 1 is literally "what is your problem?" — the skill
  refuses to prescribe tools until the problem statement is named (Hickey).
  Pairs with `ductile` (CLI invocations and live state) and
  `ductile-plugin-developer` (when the fix is in plugin code). Do NOT load for
  routine job inspection ("show me job X") — that is `ductile` alone, not an
  incident.
---

# Ductile RCA

This skill is **Pillar 3: RCA** of the Ductile Constitution (see
[`../../CONSTITUTION.md`](../../CONSTITUTION.md)) — the thinking discipline
for incidents. It is not a recipe book. The recipe is in `ductile` (Pillar 1)
and the fix is in `ductile-plugin-developer` (Pillar 5). What this skill
insists on is *what you think before you type*.

## Seam: relationship to other skills

| Skill | Role | When |
|---|---|---|
| Global `rca` | Generic RCA discipline | Loaded by router on the word "RCA"; this skill **extends** it with ductile evidence taxonomy. Both can co-load. |
| Global `diagnose` | Bug-reproduction loop | For local bugs without a running gateway. This skill is for **incidents on a running ductile**. |
| `ductile` | CLI + instance facts + deploy | You will invoke its commands constantly. Load both. |
| `ductile-plugin-developer` | Plugin code, manifest, protocol | Load once the hypothesis points at plugin internals. |

**Co-invocation matrix:**

| Scenario | Load |
|---|---|
| "Job X is in `dead` state, why?" | `ductile-rca` + `ductile` |
| "Withings plugin returns 0 facts since Tuesday" | `ductile-rca` + `ductile-plugin-developer` + `ductile` |
| "Gateway won't start after migration" | `ductile-rca` + `ductile` |
| "Show me job X" (routine, no incident) | `ductile` alone — do not load this skill |
| Full incident: diagnose → fix → redeploy | All three ductile skills |

## Out of scope

- Writing the fix (→ `ductile-plugin-developer`)
- Deploying the fix (→ `ductile`)
- Routine inspection of a healthy system (→ `ductile`)

## Step 1 — What is your problem?

> *"Programmers know the benefits of everything and the tradeoffs of nothing."*
> The faster you reach for a tool, the longer the incident.

Before any command runs, fill this in. If you cannot fill it in, you are not yet
investigating — you are flailing.

```text
SYMPTOM (what is observably wrong):
  …

EXPECTATION (what should be happening instead):
  …

WHEN DID IT START (timestamp, last known good):
  …

WHO IS AFFECTED (which instance, which plugin, which pipeline, which user):
  …

WHAT CHANGED RECENTLY (deploy, config lock, plugin update, host event):
  …

WHAT YOU HAVE ALREADY VERIFIED (and the evidence for it):
  …
```

Refuse to proceed until this is named. The shape of the problem statement tells
you which evidence to pull next; the wrong shape leads to noise.

## Step 2 — Hammock first. Do not instrument yet.

Hickey's hammock-driven development applies precisely here. *Think for ten
minutes before you type ten commands.* List the candidate hypotheses **before**
collecting evidence — otherwise you confirm the first one you stumble into.

Write down 2-5 hypotheses ranked by prior probability:

```text
H1: <most likely cause given the symptom shape>
H2: <next most likely>
H3: <wildcard — what would I not want to be true?>
```

For each hypothesis, write the **discriminating observation**: the single piece
of evidence that, once collected, would distinguish this hypothesis from the
others. *That* is what you collect first.

## Step 3 — Evidence in non-faulty-entity order

> *"Errors must be detected by some non-faulty entity."* — Armstrong

The plugin is the faulty entity. Its self-report is the **last** thing you
consult, not the first. Walk this ladder in order; do not skip rungs:

| Rung | Source | What it answers | CLI / location |
|---|---|---|---|
| 1 | `system selfcheck` | Are the invariants intact? | `ductile system selfcheck --json` (offline against a quiesced binary preferred — see `ductile` skill's "real-green pattern" for why) |
| 2 | `system status` | Is the gateway alive, PID, version, plugin count | `ductile system status --json` |
| 3 | `job_logs` | What did the gateway *observe*? | `ductile job logs --plugin <name> --status failed --json` |
| 4 | `job inspect <id>` | Full lineage + baggage for one job | `ductile job inspect <id> -v --json` |
| 5 | `transitions` | State machine path for the queue row | `ductile job inspect <id>` shows the chain |
| 6 | `plugin_facts` | What did the plugin durably record? | DB: `SELECT * FROM plugin_facts WHERE plugin = '<name>' ORDER BY id DESC LIMIT 10;` |
| 7 | `vault_audit` | Was a secret delivered, withheld, or refused — and why? | `ductile system vault-audit --principal <name> --json` |
| 8 | `plugin_state` (view) | Compatibility projection of latest fact | `ductile config show plugin:<name>` for config; DB view for state |
| 9 | `baggage` / `event_context` | Lineage that travelled with the job | inside `job inspect -v` |
| 10 | circuit breaker state | Is the plugin currently quarantined? | `ductile plugin list --json` shows breaker state |
| 11 | container/host logs | systemd / docker stderr | `journalctl --user -u ductile-local`, `docker logs ductile` |
| 12 | **plugin stderr** | Faulty entity's view — useful, but biased | bottom of job_logs, container logs |

The rule: **never quote plugin stderr as root cause before you have confirmed
the gateway's view (rungs 1-5).** A plugin that reports success to a job the
gateway recorded as failed is more interesting than either fact alone.

## Step 4 — Hypothesis testing: why-this-but-not-that

For each surviving hypothesis, force two questions:

1. **Why this?** — what is the observation that *positively* implies this
   hypothesis (not merely is consistent with it)?
2. **Why not that?** — name the sibling hypotheses you are now rejecting and
   the evidence that rules them out.

If you cannot answer "why not that" for the alternatives, you have a guess, not
a diagnosis. Keep collecting until you can.

## Step 5 — Blast radius: what-could-be-impacted-but-isn't

Hickey: *values vs places.* A failed `plugin_fact` is a value at a point in
time — it does not retroactively corrupt the past. But the **places** that
project from that fact (compatibility view, downstream pipeline state,
sibling-container DB) can drift.

For every confirmed failure, ask:
- Which pipelines `uses:` this plugin? (`ductile config show pipeline:*`)
- Which webhooks subscribe to events this plugin emits?
- Which sibling systems read from a DB this plugin writes to *via another writer*
  (e.g., the `garmin` container writes `garmin.db`; the ductile plugin is only a
  mtime watcher)?
- Is the circuit breaker open, masking downstream symptoms?

If the blast radius is wider than the symptom, you may be looking at the
*effect*, not the cause.

## Step 6 — Validate before you fix

For each candidate fix, design one cheap test that would confirm it.

| Hypothesis class | Cheap validation |
|---|---|
| Config drift | `ductile config check --strict` (does it still parse and verify?) |
| Lock mismatch | `ductile config lock` then re-run the failing job |
| Plugin regression | `ductile plugin run <name>` manually with known-good input |
| Schema drift | `ductile system selfcheck` against an offline copy of the DB |
| Reload deadlock | Trigger `ductile system reload`; observe whether it returns |
| Queue stuck | `ductile job logs --status retrying` and inspect the oldest |
| Webhook silent | Hit the gateway's `/healthz` and `/webhooks/...` with curl |
| Secret withheld / fingerprint mismatch | `ductile system vault-audit --principal <name>`; if `fingerprint_mismatch`, treat as security (possible swap) then `ductile plugin lock <name>`; if `compose_denial`+revoked, it's fail-closed-by-design — re-grant |

If the cheap test cannot distinguish your hypothesis from the alternatives,
you do not yet have the test. Refine before acting.

## Common ductile failure modes (with evidence pattern)

| Symptom | Likely cause | First evidence | Often missed |
|---|---|---|---|
| Pipeline shows green but nothing happens downstream | `if:` gate evaluating false unexpectedly; or webhook send failing silently | `job inspect` baggage + `events: []` in response | Check the **event name** at the consumer end — idiom 4 (events are the contract). |
| Plugin runs but no fact recorded | Missing `fact_outputs` in manifest; or `concurrency_safe: false` blocking | `plugin_facts` table empty for plugin; `state_updates: {}` | Manifest bytes changed but `plugin lock` not run (re-attest). |
| `system reload` hangs | Reload deadlock; or port handoff race | API server still listening on old socket | See `docs/reload_rca.md` — canonical worked example. |
| Circuit breaker open, plugin not even attempted | Earlier failures crossed threshold | `plugin list --json` shows breaker state | `ductile system reset <plugin>` is the explicit override. |
| Gateway refuses to start after config edit | High-tier integrity mismatch (`tokens.yaml`, `webhooks.yaml`, `scopes/*.json`) | startup log: "checksum mismatch" | Lock tier is *hard fail* for high-security, *warn* for operational. |
| Selfcheck shows red rows 4-6 with live gateway | Not an error — WAL safety design | `detail: "skipped: active gateway holds PID lock"` | Run selfcheck offline against the new binary BEFORE installing. |
| Fact is "wrong" — value disagrees with reality | Compatibility view stale; or two writers (ductile plugin + sibling container) | Compare `plugin_facts` newest row to source-of-truth API | Often the ductile plugin is a **watcher** not a writer; the sibling container is the writer. |
| Plugin runs fine but acts unauthenticated — its `secrets` are empty | Never `plugin lock`-ed (no fingerprint → secrets withheld), or the principal is registered with no grant | `ductile system vault-audit --principal <name>` shows no `ok` delivery; plugin ran with empty `secrets` | This is **silent** — the plugin runs and the job is "green". The secret path is gated; the run path is not. |
| Job fails with "secret composition failed" | Principal **revoked** — delivery fails closed rather than run secret-less | `vault_audit` row with outcome `denied` / op `compose_denial` | A revoked principal is fail-closed *by design*, not a bug. Re-grant or re-register. |
| Job fails right after a plugin edit; log says "fingerprint mismatch" | Plugin bytes changed without re-`plugin lock` — OR an actual binary swap | log "SECURITY: plugin fingerprint mismatch at spawn" + hub event `plugin.fingerprint_mismatch`; `vault_audit` denial | Treat as a **security event** first. Confirm it was *your* edit, then `ductile plugin lock <name>` and re-run. |

## The recurring root cause(s): forgot to lock — `config lock` *or* `plugin lock`

> Half of "why isn't my change live?" is a lock not run. Since the two locks
> decoupled, there are **two** H1s — pick by what changed.

After a **config file** edit (`config.yaml`, `api.yaml`, `pipelines/*.yaml`,
`webhooks.yaml`):

```bash
ductile config check          # validate
ductile config lock           # authorize the file state
ductile system reload         # apply
```

After a **plugin manifest/entrypoint** edit — the bytes are *attested*, so this
is the sibling H1, easy to miss post-decoupling:

```bash
ductile plugin lock <name>    # re-attest the changed bytes
ductile system reload
```

`config lock` does **not** re-attest plugin bytes. The tell for the
`plugin-lock` H1: a secret-consuming plugin started failing or lost its `secrets`
right after an edit, and the operator ran `config lock` but not `plugin lock` —
the spawn-time fingerprint check then refuses its secrets (fail closed). If the
user describes a change followed by no apparent effect, one of these two is the
hypothesis every time.

## Handoff back to the other skills

- Fix is in plugin code → `ductile-plugin-developer`
- Fix is in config / lock / deploy → `ductile`
- Fix is "operator forgot a step" — close the incident with a runbook update in
  `docs/OPERATOR_GUIDE.md` (idiom 10: observability is a feature).

## References

- Global `rca` skill — generic discipline this skill extends
- Global `diagnose` skill — when there is no running gateway
- `docs/PLUGIN_DIAGNOSTICS.md` — structured triage commands
- `docs/reload_rca.md` — worked RCA example, canonical pattern
- `docs/HEALTH_CHECK.md` — invariants checked by `selfcheck`
- `docs/SQL_TIGHTENING_LOG.md` — schema-change audit trail (when "what changed?" is "the schema")
- `docs/SECRETS.md` — the vault + attestation model (for secret-delivery and fingerprint-mismatch incidents)
