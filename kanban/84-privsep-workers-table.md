---
id: 84
status: done
priority: Normal
blocked_by: [92]
tags: [privsep, config, schema]
---

# Privsep · two worker tiers in config (generalize the tracer)

> **Nav:** [[83-privsep-epic]] · after [[92-privsep-tracer-wall-off-sys-exec]] · before [[85-privsep-per-plugin-worker-grant]]

Generalize the tracer's one hardcoded worker into config. The map is an **open map** (the loader
accepts any number of rows — parsing an Nth entry is free), but we **ship and document exactly two
tiers** as the starting posture: `default` (trusted first-party) + `untrusted` (arbitrary command /
third-party). What's deferred is *ergonomics/validation* for many, **not arity** — a third row works
immediately (this is what makes the §2 sibling-residual mitigation real: move a secret-holder onto
its own worker, config-only, no code change; ADR §5 "one uid + one table row").

**Scope:**
- A `workers` map in **core config**: name → `{uid, gid, state_dir}`. Open map; `default` +
  `untrusted` shipped/documented as the default posture. **Absent / empty `workers` map → engages
  the boot gate (#86), not a phantom `default`:** with no capability → `unconfined` (today's
  gateway-uid spawn); *with* CAP_SETUID → **refuse to start** (privileged daemon, nothing to drop
  to). `default` is a *configured* unprivileged uid; `unconfined` is the named no-drop state — the
  two must never share a name (vocabulary note, [[83-privsep-epic]] / ADR §5).
- Minimal validation: uid/gid positive, `state_dir` absolute, **and no two workers share a uid**
  (a duplicate uid is false isolation — #87 chowns both `state_dir`s to one owner and the wall is
  painted on; this is correctness, not ergonomics, so it ships now). Defer only kebab-case rules
  and arbitrary-N-worker ergonomics (named-deferred, not built).
- Go structs stay the runtime authority; on-disk JSON schema stays the authoring aid (ADR §11).

**Acceptance:** core config parses the `workers` map (a third row loads fine — open map, not capped
at two); invalid uid/gid/state_dir **and any duplicate uid** fail load loudly; an absent/empty map
resolves via the #86 boot gate (no capability → `unconfined`; CAP_SETUID → refuse), never a
synthesized `default`.

## Done (2026-06-06)
- `validateWorkers` (`internal/config/loader.go`) wired into `validate()`: uid/gid > 0 (so a worker
  can never be root), `state_dir` absolute, no two workers share a uid (false-isolation guard).
  Deterministic iteration so multi-problem configs fail identically. 9 cases in `worker_validation_test.go`.
- Open map confirmed by test (a third `isolated` row loads); absent/empty map is valid here (the
  capability/refuse boot gate is [[86-privsep-spawn-uid-drop]], not this card).
- `workers` + `WorkerConf` added to `schemas/config.schema.json` (authoring aid, ADR §11).
- **Next:** [[85-privsep-per-plugin-worker-grant]] — the *which-worker* grant: no-grant → shared
  `default` tier (currently resolves to unconfined), and the manifest-hint-ignored authority split.

## Narrative
- **Source:** PrivSec ADR §5 ("capability for many, two tiers by default"). The map stays open
  (arity is free); the Brooks×Beck scope-down defers *ergonomics/validation* for many workers, not
  the ability to add a row.
- **DEFERRED (named):** arbitrary-many workers + *ergonomic* validation (kebab-case, naming) —
  adopt only when a real third worker appears (the sizing rule, ADR §5: "would you accept one
  reading the other's memory?"). **Not deferred:** duplicate-uid rejection — that's correctness,
  pulled into scope above (Hickey×Armstrong review, A4).
- **Q4 resolved (2026-06-06):** **stable uids** (postgres/sshd/nginx pattern — stateful plugins own
  a persistent `state_dir`). The map references the **uid number**; the OS account is created by the
  *deploy layer* (#88 sysusers.d / #89 image), never by the daemon at runtime. Dynamic users deferred,
  earmarked stateless-`untrusted`-only (ADR §5 provisioning note + §6 earmark).
