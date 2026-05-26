# Ductile Constitution

This document defines **why** Ductile exists and the alignment target every
change must serve. For **what** the system is technically, see `SPEC.md` and
the references under `docs/`.

---

## Alignment

Ductile is an automation runtime AI agents can run, debug, and build for.

Every surface is shaped to be driven by an LLM: a NOUN ACTION CLI, a REST API
with OpenAPI discovery, structured JSON I/O on every plugin invocation, and a
queryable execution ledger that records what happened and why. Agents don't
just operate Ductile — they **diagnose** it (doctor checks, stopwatch
subspans, structured incident evidence), they **test** plugins against it
(spawn-per-command isolation, contract-shaped manifests, deterministic JSON
in / JSON out), and they **author** new plugins for it in whatever language
the integration needs.

Sized for personal-scale automation (~50–500 jobs/day), where the operators
are agents and the auditors are humans.

---

## The Five Pillars

Every feature, doc, and skill in Ductile exists to serve one of these. When
proposing a change, name the pillar it serves.

### 1. Run

Agents drive Ductile end-to-end without a human in the loop.

- NOUN ACTION CLI — every command is `ductile <noun> <action>`
- REST API with OpenAPI discovery (`/openapi.json`)
- Structured exit codes; no interactive prompts
- `GET /topology` — plugin↔signal↔plugin graph for orchestration
- Skill: [`skills/ductile/`](skills/ductile/)

### 2. Debug / Diagnose

Agents inspect live state and surface anomalies.

- `ductile system doctor` — startup and runtime health checks
- `GET /system/doctor` — HTTP equivalent for remote agents
- `GET /system/selfcheck` — end-to-end validation
- Stopwatch subspans — per-step timing in the execution ledger
- `GET /stopwatch/{plugin}` — p50/p95/p99 latency aggregation
- Structured logs, retention controls
- Skill: `skills/ductile-doctor/` (planned)

### 3. RCA

Agents reconstruct what happened after an incident.

- Execution ledger is queryable from the API and SQLite
- Structured incident evidence taxonomy
- Skill: [`skills/ductile-rca/`](skills/ductile-rca/)

### 4. Test

Agents verify plugin behaviour against the contract.

- Spawn-per-command — invoke a plugin with one JSON blob, assert on the
  response; no harness, no DB, no global state
- Manifest contract is small and explicit
- `fact_outputs` schema is declarative
- Skill: `skills/ductile-plugin-tester/` (planned)

### 5. Author

Agents create new plugins in whatever language the integration needs.

- Subprocess protocol — JSON over stdin/stdout, any executable
- Manifest contract + `fact_outputs` schema
- The 8 idioms ([`docs/8_IDIOMS_OF_DUCTILE.md`](docs/8_IDIOMS_OF_DUCTILE.md))
- Skill: [`skills/ductile-plugin-developer/`](skills/ductile-plugin-developer/)

---

## What Ductile Is Not

These are load-bearing refusals. Each is defensible from at least one pillar.

- **Not a platform with a GUI.** No drag-and-drop canvas. The orchestration
  map is YAML because an agent can read and write YAML; an agent cannot read
  a canvas. *(Violates Run, Debug, Author.)*

- **Not a SaaS.** Runs locally on personal hardware. State lives in files an
  agent can `ls` and a human can `git diff`. *(Violates RCA, Debug.)*

- **Not opinionated about your workflow.** Provides primitives, not
  templates. Agents compose; Ductile orchestrates. *(Violates Author.)*

- **Not sized for enterprise scale.** ~50–500 jobs/day is the design centre.
  Above that, you want different trade-offs than Ductile makes. *(Violates
  Run reliability assumptions.)*

---

## Alignment Check

When adding or removing anything, ask:

1. Which pillar does this serve?
2. Does it make any pillar harder to live up to?
3. Does it violate any "is not"?

If a feature doesn't map to a pillar, or weakens one, it doesn't belong.
