# Ductile — Contributor Contract

This is the single document any contributor — human or agent — should read
before changing code. It defines what Ductile *is*, the lenses we use to keep
it small enough to reason about, the vocabulary that carries the design, and
the Go quality bar for merge-grade work.

Reference material (architecture, API, config, pipelines, plugin authoring)
lives under `docs/`. This file governs *how* we change the code; `docs/`
explains *what* the code is.

---

## 1. Project Identity

Ductile is an automation runtime AI agents can run, debug, and build for. Its
audience is LLM operators driving the full lifecycle — operating the gateway,
diagnosing failures, RCA'ing incidents, testing plugins, and authoring new
ones — with humans as auditors. See [`CONSTITUTION.md`](CONSTITUTION.md) for
the alignment paragraph and the five lifecycle pillars every change must
serve.

Technically: a Go core orchestrates polyglot plugins via a subprocess
protocol (JSON over stdin/stdout). Sized for personal-scale automation
(~50–500 jobs/day), with emphasis on simplicity, reliability, and predictable
behaviour under crash, retry, and timeout conditions.

It is research-grade in scope, production-disciplined in execution. Code
should be readable, composable, and explicit about what it does.

**Core philosophy:** Plugins stay dumb; the core controls flow. Simple enough
for a human to understand in an afternoon; structured enough for an agent to
drive without supervision.

---

## 2. Design Grounding

These are the lenses we use when reviewing changes. They are not style
preferences; they are how we keep Ductile small enough to reason about.

- **Simple is the goal, not easy.** A change that makes something easier to
  use today but harder to reason about tomorrow is the wrong trade. Prefer
  fewer concepts over more affordances.
- **Decomplect concerns.** If two things vary independently in the real world
  (e.g. *what a plugin observed* vs *what the latest value is*), they get
  separate names and separate storage. *Plugins stay dumb; the core controls
  flow* is this rule applied to processes.
- **Distinguish value, state, and identity.** A *value* is immutable (a fact,
  a config snapshot, a payload). *State* is a place that changes (a queue row,
  a plugin-owned filesystem path). *Identity* is a stable name for a series of
  values over time (a pipeline, a plugin alias). Code that conflates these is
  the code that breaks under retry and crash.
- **Names are part of the contract.** `plugin_facts`, `compatibility_view`,
  `baggage`, and `fact_outputs` were chosen carefully. New names get the same
  care; renames are a real change, not a cleanup.
- **Design before typing.** For anything touching the queue, durability, or
  the plugin protocol, write the proposal first (in an issue or working note)
  and get it reviewed before code. The cost of a wrong abstraction here is
  paid by every plugin author downstream.

---

## 3. Idiom Grounding

### 3a. Central Abstractions

1. **Work Queue (SQLite-backed).** All producers submit to a single FIFO
   queue. Producers: Scheduler (heartbeat ticks), Webhook receiver, Router
   (plugin output), CLI/API. Dispatch uses a bounded worker pool by default
   (`service.max_workers` defaults to `max(1, CPU-1)`). Plugin concurrency is
   controlled by each plugin's `parallelism` config and manifest
   `concurrency_safe` hint. Operators can force whole-system serial dispatch
   with `service.max_workers: 1`.

2. **Plugin Lifecycle: Spawn-Per-Command.** No long-lived plugin processes.
   Fork entrypoint → write JSON to stdin → read JSON from stdout → kill
   process. The wire protocol carries `context` (baggage). The core does not
   provision or pass `workspace_dir`; plugins own any filesystem scratch or
   cache paths they require. Timeouts: SIGTERM → 5s grace → SIGKILL.

3. **State Model.** Three distinct concerns, three distinct names:
   - `config` — static, from config files, env-interpolated.
   - `plugin_facts` — append-only durable record of plugin observations.
     The durable place a plugin remembers things.
   - `plugin_state` — compatibility/cache view of the latest fact, rebuilt
     automatically by the core via the manifest's `compatibility_view`
     declaration.

4. **Pipeline Engine.** YAML DSL for event-driven orchestration. Steps can
   `uses` a plugin, `call` another pipeline, `split` for parallel fan-out,
   gate with `if` conditions, and remap payload with `with`.

Full technical reference: `docs/ARCHITECTURE.md`,
`docs/PIPELINES.md`, `docs/CONFIG_REFERENCE.md`.

### 3b. Vocabulary

These terms have specific meanings. Use them precisely; do not introduce
synonyms.

| Term | Kind | Meaning |
|---|---|---|
| `config` | value | Static, parsed from YAML, env-interpolated. Immutable for the life of a run. |
| `plugin_fact` | value | An append-only durable record of something a plugin observed. The thing a plugin remembers. |
| `plugin_state` | view | A compatibility/cache projection of the latest fact. Derived, not authoritative. |
| `baggage` | value | Metadata propagated along a pipeline. Travels with the payload; does not mutate upstream. |
| `pipeline` | identity | A named series of steps. Stable across runs; its executions are values. |
| `job` | identity | A queued unit of work. Its status is state; its inputs are values. |
| `event` | value | An immutable trigger record. Routes consume events; events do not change. |

### 3c. Project Structure

```
ductile/
├── cmd/ductile/        CLI entrypoint (thin; delegates to Run(ctx) error)
├── internal/           Core packages (not importable externally)
│   ├── api/            REST API server
│   ├── auth/           Token auth, scopes
│   ├── config/         YAML parser, ${ENV} interpolation, integrity
│   ├── dispatch/       Plugin spawn, preflight, pipeline routing
│   ├── doctor/         Startup preflight checks
│   ├── e2e/            End-to-end test harness
│   ├── events/         Event hub
│   ├── inspect/        Job/baggage inspection
│   ├── lock/           PID lock
│   ├── log/            Structured logging
│   ├── plugin/         Discovery, manifest validation, registry
│   ├── protocol/       v2 codec (request/response envelopes)
│   ├── queue/          SQLite work queue, state machine
│   ├── router/         Config-declared event routing, fan-out
│   ├── scheduler/      Heartbeat tick, interval/cron, poll guard
│   ├── scheduleexpr/   Schedule expression parser
│   ├── state/          plugin_facts (append-only) + plugin_state (view)
│   ├── storage/        SQLite helpers
│   ├── tui/            Terminal UI (system watch)
│   ├── webhook/        HTTP listener, HMAC verification
├── plugins/            Bundled reference plugins
├── schemas/            JSON schemas for config files
├── scripts/            Test orchestration, version, migrations
├── test/               Fixtures (test/fixtures/) and stress harness (test/stress/)
└── docs/               Canonical documentation
```

Group packages by concern, not by layer. Define interfaces where they are
used; keep them small. Accept interfaces, return concrete types from
constructors.

### 3d. Key Design Constraints

These are non-negotiable without explicit redesign:

1. **Bounded dispatch by default; serial is an operator cap** —
   `service.max_workers` defaults to `max(1, CPU-1)` for bounded global
   concurrency. Operators who need whole-system serial execution set
   `service.max_workers: 1`. Per-plugin concurrency remains controlled by
   `parallelism` and the plugin manifest's `concurrency_safe` hint.
2. **No streaming** — no WebSockets, no persistent plugin connections.
3. **Exact-match routing** — no wildcards. Conditional logic belongs in
   plugins or in `if` predicates.
4. **Spawn-per-command** — no daemon plugin management. Fork overhead is
   acceptable at this scale.
5. **HMAC mandatory** — all webhook endpoints require HMAC-SHA256.
6. **Integrity on high-security files** — `tokens.yaml`, `webhooks.yaml`,
   `scopes/*.json` must pass BLAKE3 check or the system refuses to start.
   Always run `ductile config lock` after editing these files.

---

## 4. Workflow

- **Branching:** `<component>/card<id>-<short-description>`.
- **Commits:** `<component>: <action> <what>`. Never attribute AI tooling in
  commit messages or PR descriptions.
- **Cadence:** one card → one branch → one PR → merge → next card.
- **Push discipline:** do not push without explicit human permission in the
  current session.
- **Scope discipline:** do not expand scope silently. Report unrelated
  problems instead of fixing them opportunistically. If an abstraction is
  making the work difficult or misleading, report that rather than papering
  over it.

---

## 5. Code Quality — Go Defaults

- Use established standard-library tools first: `testing`, `net/http`,
  `httptest`, `errors`, `fmt.Errorf(... %w ...)`, `database/sql`, and `go mod`.
- Prefer project-appropriate common libraries when needed: `cobra` or `flag`
  for CLI, `viper` plus YAML or `envconfig` for config, `log/slog` for
  logging, `huh` for prompts, and `bubbletea` for full TUI flows.
- Put binaries in `cmd/<name>/main.go`; keep `main` thin and delegate to
  `Run(ctx) error`.
- Group packages by concern rather than MVC layers; put non-public code under
  `internal/`.
- Define interfaces where they are used, keep them small, accept interfaces,
  and return concrete types from constructors.

---

## 6. Style

- `gofmt` and `goimports` are mandatory.
- `golangci-lint` must pass for merge-grade work.
- Refactor functions longer than roughly 60 lines and files longer than
  roughly 400 lines.
- Exported names need doc comments.
- Avoid package stutter, vague names, and clever abstractions that hide
  behaviour.

---

## 7. Safety

### Errors and concurrency

- Wrap propagated errors with useful context.
- Use `errors.Is` and `errors.As`; do not inspect errors by string matching.
- Do not `panic` except for unrecoverable startup failures.
- Do not use `log.Fatal` outside `main`.
- Do not log and return the same error.
- Put `context.Context` first for blocking or cancellable work.
- Use context cancellation for shutdown.
- Use either mutexes or channels for a datum, not both.
- Use `defer mu.Unlock()` immediately after `mu.Lock()`.

### General safety

- Handle every error or explicitly document why it is safe to ignore.
- Respect context cancellation in long-running operations.

### Security

- `gosec` must pass for merge-grade work.
- `#nosec` requires explicit human approval, the rule ID, and a concrete
  justification.
- Never log secrets.
- Validate external input.
- Use `html/template` for HTML output.
- Use `//go:embed` only for small, static, versioned assets; prefer `embed.FS`
  for file sets.

---

## 8. Testing

Detailed workflows live in `docs/TESTING.md`. The contract:

- Prefer real components over mocks. Mock only truly uncontrollable external
  systems. Do not mock what can be exercised with a real local implementation.
- Use real SQLite or a real test database where practical.
- Use `httptest.NewServer` for HTTP behaviour.
- Use table-driven tests when there are multiple meaningful cases.
- Test observable behaviour, not implementation details.
- Keep tests beside the code they exercise.
- Race test failures and flaky tests block merge readiness.

Staged validation:

| Stage | Script | When |
|---|---|---|
| Branch work | `scripts/test-fast` (`go test ./...`) | Every change |
| System confidence | `scripts/test-docker` | Before PR |
| Merge readiness | `scripts/test-premerge` | Before merge request |
| Main validation | `scripts/test-main` | Post-merge gate |

Testing orchestration belongs in repository scripts, not in the `ductile`
CLI. Docker-backed tests should be fixture-driven, readiness-driven,
black-box, and should capture useful failure artefacts.

---

## 9. Quality Gates

Before considering code work complete, run the relevant quality gates for the
scope changed. For Go changes the expected gates are formatting, lint/static
checks, security review, tests, and vet. If a gate cannot be run or fails for
an unrelated reason, report that clearly.

Treat `gosec` findings as real findings. Fix them where possible. Do not add
`#nosec` without explicit human authorisation, and include the rule ID and
justification when authorisation is given.

For merge-grade Go work, the expected checks are:

```bash
gofmt -l .
goimports -l .
go vet ./...
golangci-lint run ./...
gosec -severity medium ./...
go test -race -count=1 -shuffle=on -vet=all ./...
```

---

## 10. Session Handoff

At handoff, summarise:

- what changed,
- which files changed and why,
- what was verified,
- any mocks used,
- any out-of-scope issues found,
- anything that needs human review.

---

## References

- `docs/ARCHITECTURE.md` — single source of truth for the system model.
- `docs/API_REFERENCE.md` — REST API endpoints and schemas.
- `docs/CONFIG_REFERENCE.md` — configuration spec.
- `docs/PIPELINES.md` — pipeline DSL reference.
- `docs/PLUGIN_DEVELOPMENT.md` — plugin authoring guide.
- `docs/OPERATOR_GUIDE.md` — day-to-day operations.
- `docs/TESTING.md` — testing workflows in detail.
- `docs/AUDIENCES.md` — the eight reader profiles this project serves; cite cells when proposing or reviewing doc and affordance changes.
- `CONTRIBUTING.md` — build, test, and PR mechanics for new contributors.

A Claude Code skill for operating Ductile lives in `skills/ductile/` and can
be installed with `cp -r skills/ductile/ ~/.claude/skills/ductile/`. The
skill is a tooling convenience; this document is the contract.
