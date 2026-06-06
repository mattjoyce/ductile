---
id: 16
status: done
priority: High
tags: [vault, prereq, housekeeping]
---

# Prerequisite · land the age-at-rest + spawn-hygiene batches

The vault builds on prerequisites that are **already implemented but uncommitted** on
`feat/age-secrets-and-spawn-hygiene`.

**Scope:**
- Review + commit the two finished, green batches: (a) age-at-rest + spawn-hygiene + `ductile secrets` CLI; (b) embedded schema dump + static `config validate`.
- Suggested: `/code-review` then two logical commits (`<component>: <action> <what>`, no AI attribution).
- Verified present: `internal/secrets/age.go`, `internal/secrets/keyring.go`, `internal/config/secrets.go`, `internal/dispatch/env.go`.

**Acceptance:** branch has the prerequisites committed; `go test ./...` green.

## Narrative
- **Source:** handoff §"State in one line" + §"Constraints — Verified-present prerequisites".
- Their own PRDs (complete): `~/.claude/MEMORY/WORK/20260531-110251_age-secrets-and-spawn-hygiene/PRD.md` and `~/.claude/MEMORY/WORK/20260531-135616_embedded-schema-access-and-validate/PRD.md`.
- Not strictly blocking Rung-1 *coding* (the code is present on-branch), but should land before vault commits stack on top.

### Done (2026-06-02)
- **Already landed** — the premise was stale. `git ls-files` confirms all four prereq files are
  tracked and the working tree is clean: `internal/secrets/age.go`, `internal/secrets/keyring.go`,
  `internal/config/secrets.go`, `internal/dispatch/env.go`. They were committed during the Rung 1–3 arc.
- `go test ./...` green on `feat/age-secrets-and-spawn-hygiene` except the documented flake
  `TestSpawnPluginTimeoutKillsProcessGroup` (heavy-parallel only); it **passes isolated**
  (`go test ./internal/dispatch -run … -count=1` → ok). Not a regression.
- No commit needed; card closed as verify-only.
