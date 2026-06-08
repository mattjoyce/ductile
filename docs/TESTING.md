# Testing Guide

This document defines the target-state testing strategy for Ductile. The goal is to preserve developer velocity during normal branch work while adding stronger runtime/system confidence gates before merge and after changes land on `main`.

---

## 1. Testing Model

Ductile uses a **staged testing strategy**:

1. **Fast tests for branch development**
   - Used constantly during normal implementation and remediation work.
   - Optimized for feedback speed and iteration velocity.
2. **Docker-backed tests for complex/system-level assurance**
   - Used during complex development when realism matters.
   - Required before merge to `main`.
3. **Full validation on `main` after merge**
   - Ensures trunk health on the actual merged state.

This guide is intentionally about **assurance workflow**, not product/runtime commands. Testing orchestration belongs to repository tooling rather than the `ductile` binary.

---

## 2. Branch Development Policy

For day-to-day branch development, the default loop is the fast existing test suite.

### Default fast inner loop
```bash
go test ./...
```

This is the baseline command for:
- feature branches
- remediation branches
- refactors
- fast local iteration

### Why the fast loop stays fast
The default branch-development loop should optimize for:
- quick feedback
- frequent execution
- low friction during implementation

To preserve velocity, the fast inner loop should **not require Docker** and should **not be overloaded with every gate**.

### Environment requirements for the fast suite

`go test ./...` is mostly hermetic, but a few packages couple to the host environment.
Run the fast suite as a **non-root user** with **`bash` and `python3` on `PATH`**:

- Some `internal/vault` tests force a persist failure with `chmod 0500` on a directory
  and assert the write is rejected. **root bypasses file permissions**, so the injected
  failure never fires under root. This precondition is now enforced *in code*:
  `requireWritablePermsEnforced` (`internal/vault/preconditions_test.go`) `t.Skip`s these
  tests under euid 0 with the assumption named — so root sees an explicit skip, not a
  false "expected a save failure". (A skip is never a pass; run as non-root for coverage.)
- The `internal/e2e` and `internal/dispatch` plugin-spawn tests exec real plugin
  entrypoints, which need `bash` (and `python3` for the `sys_exec`/python plugins).
  Without them you get `env: can't execute 'bash'` and spurious failures.

If you must run the suite in a container, base it on an image that has `bash`/`python3`
and run as a non-root user — mirror the Dockerfile's **runtime** stage, not its builder
stage. `scripts/test-fast` prints a warning when these preconditions are not met.

---

## 3. Docker-Backed Testing Policy

Docker-backed tests are used for **complex or environment-sensitive behavior** where standard tests provide less confidence.

### Docker tests are for
- webhook ingress
- scheduler persistence and restart recovery
- authenticated API end-to-end flows
- plugin runtime/process behavior
- append-only fact persistence and compatibility-state derivation
- realistic service startup/configuration behavior

### Docker tests are not for
- duplicating every Go test in containers
- replacing fast local tests
- being mandatory on every small branch iteration

### Development usage
Docker-backed tests are **selective during development**:
- use them for system-level work
- use them when reproducing runtime-sensitive issues
- use them before merge as part of the required gate

---

## 4. Repository Test Commands

Testing orchestration should live in repository tooling under `scripts/`, not in the `ductile` CLI.

### Canonical script surface
- `scripts/test-fast`
- `scripts/test-docker`
- `scripts/test-premerge`
- `scripts/test-main`

### Optional convenience wrappers
A `Makefile` may provide wrappers such as:
- `make test`
- `make test-docker`
- `make test-premerge`
- `make test-main`

If Make targets exist, they should wrap the canonical scripts rather than duplicating logic.

---

## 5. Intended Meaning of Each Script

### `scripts/test-fast`
Purpose:
- normal branch-development assurance
- fastest frequent local validation

Expected scope:
- standard fast repo tests

Recommended initial behavior:
```bash
go test ./...
```

### `scripts/test-docker`
Purpose:
- Docker-backed runtime/system validation
- selective during development
- required in pre-merge and main validation flows

Expected scope:
- fixture-driven Docker scenarios
- black-box/high-value runtime validation
- teardown and artifact capture on failure

### `scripts/test-premerge`
Purpose:
- merge-grade assurance before landing to `main`

Expected scope:
- fast tests
- lint/static checks
- Docker-backed validation

### `scripts/test-main`
Purpose:
- full post-merge validation of trunk health

Expected scope initially:
- same coverage as pre-merge

This may later expand to include broader smoke/regression suites.

---

## 6. Lint Placement Policy

To preserve branch-development velocity:
- **lint is not required in the default fast inner loop**
- **lint is required in pre-merge and main validation**

This gives the desired balance:
- velocity during development
- stronger gates before merge and on trunk

---

## 7. Pre-Merge Policy

Before merging to `main`, the branch must pass:
- the fast standard suite
- lint/static checks
- the Docker-backed validation suite

Conceptually, pre-merge validation is:
```text
scripts/test-fast
+ golangci-lint run ./...
+ scripts/test-docker
```

This is the required assurance level for merge readiness.

---

## 8. Main Branch Policy

`main` must receive a **full validation pass after merge**.

### Initial definition of full validation on `main`
- `scripts/test-fast`
- `golangci-lint run ./...`
- `scripts/test-docker`

This means `main` validation is initially **at least as strong as pre-merge validation**.

### Why `main` validation is distinct
Pre-merge validation protects the merge boundary.
Post-merge validation protects the actual merged state of trunk.

This matters because:
- nearby merges may interact unexpectedly
- rebases and branch timing can hide integration issues
- trunk health affects all contributors

### Main failures
Failures on `main` should be treated as **trunk-health issues** and triaged promptly.
Docker-backed failures on `main` should retain and surface their artifacts.

### Initial definition of `scripts/test-main`
Initially, `scripts/test-main` should mean:
- `scripts/test-fast`
- `golangci-lint run ./...`
- `scripts/test-docker`

This keeps `main` validation at least as strong as pre-merge validation while leaving room to grow later.

### Future expansion
Heavier smoke/regression or stress-oriented suites may be added later as `main`-only or scheduled validation, but they are not required for the initial phase of this testing strategy.

---

## 9. Docker Harness Design Direction

The Docker-backed harness should follow these principles:
- **fixture-driven**
- **Docker Compose based**
- **explicit readiness checks** (not just sleeps)
- **black-box/high-value scenarios**
- **automatic artifact capture on failure**

### First-wave Docker scenarios
The first high-value Docker scenarios are:
- `webhook-ingress`
- `scheduler-recovery`
- `api-e2e`

These are intentionally narrow and complement the fast test suite rather than replacing it.

#### `webhook-ingress`
Goal:
- validate end-to-end inbound webhook handling in a real service/container environment

Should verify:
- service boots with webhook configuration
- valid signed request is accepted
- invalid signature is rejected
- oversized request is rejected when configured
- queued work exists after successful ingress

#### `scheduler-recovery`
Goal:
- validate restart/crash recovery behavior using persisted runtime state

Should verify:
- service starts with scheduler enabled
- a running/orphaned job exists before restart
- service restart occurs
- orphan recovery transitions work correctly after restart

#### `api-e2e`
Goal:
- validate authenticated API and pipeline behavior with real config and runtime setup

Should verify:
- service boots with real config
- authenticated API requests succeed
- unauthorized requests fail appropriately
- pipeline/API triggers create expected queued work
- status/readback behavior works from the running service

#### `hook-route-compilation`
Goal:
- validate hook runtime behavior against a real running service

Should verify:
- a root job with `notify_on_complete: true` fires a hook pipeline
- hook-entry `call:` expands into the called pipeline entry
- exactly one hook job is enqueued for the scenario
- hook dispatch remains root-level rather than creating pipeline step context
- the hook job payload is the expected lifecycle event envelope

#### `sync-terminal-route`
Goal:
- validate synchronous API result selection against compiled terminal routes

Should verify:
- a synchronous pipeline returns `200 OK`
- the compiled `if:` root appears as a `core.switch` job rather than a skipped user step
- a skipped earlier step does not become the returned result
- the returned result comes from the actual terminal routed step
- the runtime still records the expected job completion story in the database

#### `conditional-with-route`
Goal:
- validate compiled `if:` routing and `with:` remapping against a real running service

Should verify:
- a compiled `if:` step becomes a real `core.switch` hop at runtime
- the false branch bypasses the gated plugin and still reaches the downstream step
- the true branch runs the gated plugin and preserves the expected parent/child route shape
- the gated plugin receives the `with:`-remapped payload values on the true branch
- route depth and max-depth control-plane state persist in `event_context`

#### `from-plugin-scoping`
Goal:
- validate the `from_plugin:` selector against a real running service

Should verify:
- a hook pipeline with `from_plugin:` matches only when the upstream
  plugin equals the selector
- the same hook signal from a different upstream plugin does not fire
  the scoped pipeline
- a co-resident hook pipeline without `from_plugin:` continues to fire
  for every matching lifecycle signal (regression for today's behaviour)
- the compiled-route inspection (`GET /config/view`) surfaces
  `source_plugin` on the scoped route

#### `context-aware-trigger-if`
Goal:
- validate pipeline-level `if:` evaluating against the upstream
  job's accumulated durable context

Should verify:
- a baggage value claimed by an upstream pipeline step is visible to a
  downstream pipeline's `if:` predicate as `context.*`
- predicate true → dispatch fires
- predicate false → dispatch is suppressed (no `core.switch`)
- a route fired without upstream context (e.g. from webhook ingress)
  with a `context.*` predicate is suppressed (absent context evaluates
  to false, no error)

### Deferred wave-2 concerns
These are valuable, but should not be in the first Docker wave:
- reload/restart nuance beyond initial recovery scenarios
- plugin runtime matrix testing
- multi-hop expansion suites
- load/stress validation
- broad config matrix coverage

Wave 1 should stay small, high-value, and stable.

---

## 10. Docker Harness Architecture

The Docker-backed test harness should be **fixture-driven** and orchestrated through repository tooling rather than product commands.

### Design principles
- use Docker only where runtime/system realism adds meaningful confidence
- keep scenarios black-box and high-value
- avoid duplicating the entire Go test suite in containers
- make local and CI execution use the same harness entry points

### Recommended structure
Use named fixtures representing focused system scenarios. For example:

```text
test/fixtures/docker/webhook-ingress/
test/fixtures/docker/scheduler-recovery/
test/fixtures/docker/api-e2e/
```

Each fixture should define the inputs needed for that scenario, such as:
- config files
- fixture data
- environment variables
- compose overrides if required
- scenario-specific assertion inputs

### Orchestration mechanism
Use **Docker Compose** for harness orchestration.

Recommended behavior:
- shared Compose base where possible
- fixture-specific overrides where needed
- explicit startup and teardown lifecycle
- one harness runner orchestrating one or more fixtures

### Readiness policy
Readiness must be explicit.

The harness should:
- wait for service health or known-ready endpoints
- use retries/timeouts
- fail clearly if readiness is not achieved

Avoid relying on arbitrary sleeps as the primary readiness mechanism.

### Fixture execution model
The harness should follow a consistent lifecycle:
1. select fixture(s)
2. start services
3. wait for readiness
4. run black-box assertions
5. collect artifacts on failure
6. tear down by default

### Local targeting
The design should support:
- running all Docker fixtures
- running a single named fixture for focused development work

This keeps Docker usable during complex development without forcing full-suite runtime on every change.

## 11. Failure Artifact Policy for Docker Tests

On Docker-backed test failure, the harness should automatically capture artifacts under a predictable path such as:

```text
test-artifacts/docker/<timestamp>/<fixture>/
```

Recommended minimum artifact set:
- container/service logs
- fixture/config inputs
- scenario/assertion log
- failed HTTP responses where applicable
- DB snapshot where applicable

### Recommended artifact structure
```text
test-artifacts/docker/<timestamp>/<fixture>/
  run.log
  scenario.log
  compose.log
  service-*.log
  responses/
  config/
  db/
```

### Behavior
- keep artifacts on local failure
- upload artifacts on CI failure
- tear down containers after artifact capture by default
- optional debug/preserve mode may be added later

### Design intent
Docker failures should be diagnosable without immediately rerunning in manual debug mode. Artifact capture should therefore be automatic rather than opt-in.

---

## 12. CI Policy

CI should mirror the local staged testing model rather than inventing separate workflows.

### Branch / PR CI
Always run fast validation:
- `go test ./...`
- `golangci-lint run ./...`

This keeps standard branch feedback fast and useful.

### Pre-merge gate
Before merge, require merge-grade validation:
- `scripts/test-premerge`

Conceptually this means:
- fast tests
- lint/static checks
- Docker-backed validation

### `main` CI
Run full validation on merged trunk:
- `scripts/test-main`

Initially, `scripts/test-main` should provide at least the same coverage as pre-merge validation.

### CI job visibility
CI should expose at least separate visible checks for:
- fast validation
- Docker validation

This makes failures easier to diagnose and rerun.

### CI design principles
- CI should mirror the local staged-testing model
- CI should invoke the same canonical scripts developers use locally
- Docker validation should be a required pre-merge gate
- `main` should always receive a full post-merge validation pass

---

## 13. Scope Boundaries

### Fast tests should own
- pure logic
- deterministic unit behavior
- parser/validator correctness
- config integrity and plugin fingerprint policy, using temporary config/plugin fixtures
- router/state/queue integration using real SQLite where helpful
- low-friction day-to-day confidence

### Docker tests should own
- runtime system behavior
- service boot with real config
- restart and recovery flows
- network ingress behavior
- realistic end-to-end operator-facing scenarios

### Config integrity / plugin fingerprint tests

Plugin fingerprinting belongs in the fast suite by default. Use temporary
config directories and temporary plugin directories in Go tests rather than
committed fixture trees unless the scenario needs multi-process runtime
realism.

Fast tests should cover:
- `ductile config lock` always writes fingerprints for configured plugins
- lock records both configured absolute paths and symlink-resolved paths
- manifest and entrypoint bytes are hashed
- configured enabled plugin mismatches fail verification
- configured disabled plugin mismatches warn only
- configured but undiscovered enabled plugins fail verification
- stale lock entries for plugins removed from config warn only
- missing `plugin_fingerprints` fails when plugins are configured

Move a scenario into Docker only when the behavior depends on real service
startup/reload, container filesystem mounts, or operator-facing black-box
assertions that cannot be represented by temporary local fixtures.

---

## 14. Implementation Order

Once the design is accepted, implementation should proceed in sensible phases:

1. document the testing policy
2. scaffold canonical repo scripts
3. build the Docker harness base
4. implement the first Docker fixtures
5. wire CI stages to the canonical scripts
6. polish artifact capture and `main` validation behavior

---

## 15. Script and Target Design

The canonical testing interface should live in `scripts/`, with optional convenience wrappers in a `Makefile`.

### Canonical scripts
- `scripts/test-fast`
- `scripts/test-docker`
- `scripts/test-premerge`
- `scripts/test-main`

These scripts are the source of truth for local and CI usage.

### Intended behavior

#### `scripts/test-fast`
Purpose:
- fast branch-development assurance
- frequent local execution during implementation

Recommended initial behavior:
```bash
go test ./...
```

Lint is intentionally excluded from the default fast loop to preserve iteration speed.

#### `scripts/test-docker`
Purpose:
- Docker-backed system/runtime validation
- selective during development
- required in pre-merge and main validation flows

Expected behavior:
- boot the Docker-backed harness
- run the selected fixture scenarios
- perform readiness checks
- capture artifacts on failure
- tear down by default

#### `scripts/test-premerge`
Purpose:
- required merge-grade validation before landing on `main`

Expected behavior:
- run `scripts/test-fast`
- run `golangci-lint run ./...`
- run `scripts/test-docker`

#### `scripts/test-main`
Purpose:
- full post-merge validation on `main`

Expected initial behavior:
- same coverage as `scripts/test-premerge`

This may expand later to include broader smoke/regression suites.

### Composition rules
- `scripts/test-premerge` should compose lower-level scripts instead of duplicating logic
- `scripts/test-main` should compose lower-level scripts instead of duplicating logic
- CI should invoke the same canonical scripts developers use locally

### Optional Make wrappers
Optional Make targets may wrap the scripts for ergonomics:
- `make test`
- `make test-docker`
- `make test-premerge`
- `make test-main`

If present, these should delegate to `scripts/` rather than reimplementing behavior.

## 16. Summary

The target state is:
- **velocity during branch development**
- **strong gates before merge**
- **explicit trunk protection after merge**
- **clear separation between product commands and assurance tooling**

In short:
- fast tests are for iteration
- Docker tests are for system confidence
- pre-merge is a gate
- `main` is protected by full validation
