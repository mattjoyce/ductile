# Contributing to Ductile

Welcome. This file covers the practical mechanics of building, testing, and
submitting changes. The design lenses, code-quality bar, and project
vocabulary are defined in [`AGENTS.md`](AGENTS.md) — please read that first.
It is the contract; this file is the on-ramp.

---

## Prerequisites

- Go 1.25+ (see `go.mod` for the exact toolchain version)
- SQLite (linked via `database/sql`; no separate install needed for tests)
- Docker (for `scripts/test-docker` and the fixture-driven system tests)
- Optional: `golangci-lint`, `gosec`, `goimports` for the merge-grade gates

---

## Build

```bash
make build          # builds ./ductile with version stamping (and codesigns on macOS)
make install        # copies to ~/.local/bin/ductile (Linux: also restarts user service)
```

Or directly:

```bash
go build -o ductile ./cmd/ductile
```

---

## Run

```bash
./ductile system start --config ~/.config/ductile/config.yaml
./ductile system watch          # TUI for live diagnostics
ductile config check            # validate config files
ductile config lock             # update integrity manifest after editing high-security files
```

---

## Test

Ductile uses staged validation. Pick the gate that matches your scope:

| Stage | Command | When to run |
|---|---|---|
| Fast | `make test` (or `scripts/test-fast`) | While iterating on a branch |
| Docker | `scripts/test-docker` | Before opening a PR |
| Pre-merge | `scripts/test-premerge` | Before requesting merge |
| Main | `scripts/test-main` | Post-merge validation |

Test fixtures for system-level scenarios live in `test/fixtures/docker/` (see the
[fixture index](test/fixtures/docker/README.md)).

---

## Quality gates for merge-grade work

```bash
gofmt -l .
goimports -l .
go vet ./...
golangci-lint run ./...
gosec -severity medium ./...
go test -race -count=1 -shuffle=on -vet=all ./...
```

If a gate cannot run in your environment or fails for an unrelated reason,
say so explicitly in the PR. Do not silence findings.

---

## Branching, commits, PRs

- Branch name: `<component>/card<id>-<short-description>`.
- Commit message: `<component>: <action> <what>`. Do not attribute AI tooling.
- One card → one branch → one PR.
- Do not push without explicit permission for the current session.

---

## Where things live

- **Code:** `cmd/`, `internal/`, `plugins/`
- **Schemas:** `schemas/` (JSON schemas for config files)
- **Tests:** beside the code they exercise; system fixtures in `test/`
- **Docs:** `docs/` — see [`docs/`](docs/) for architecture, API,
  configuration, pipelines, plugin authoring, and operator guides.
- **Contract:** [`AGENTS.md`](AGENTS.md) — read before changing code.

---

## Reporting problems vs fixing them

If you find a problem outside the scope of your current change — a stale
doc, a broken script, a leaky abstraction — report it (open an issue with
`bd`, or note it in your PR description). Do not silently expand scope to
fix it. This keeps PRs reviewable and protects against accidental
regressions.

---

## Who is this project for?

Ductile's documentation and software surfaces are designed against eight
reader profiles defined in [`docs/AUDIENCES.md`](docs/AUDIENCES.md). If
you're contributing here you're most likely cell #1 (first-time contributor)
or cell #2 (maintainer). When proposing or reviewing changes to docs or
affordances, cite the cells you are serving — it makes scope explicit and
review precise.

## Getting help

- Architecture and design questions: `docs/ARCHITECTURE.md`.
- "How do I write a plugin?": `docs/PLUGIN_DEVELOPMENT.md`.
- "How do I run / operate Ductile?": `docs/OPERATOR_GUIDE.md`.
- "Which doc is meant for me?": `docs/AUDIENCES.md`.
- Anything else: open an issue with `bd`.
