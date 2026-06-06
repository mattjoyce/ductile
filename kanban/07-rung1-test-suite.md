---
id: 7
status: done
priority: Normal
blocked_by: [2, 3, 4, 5, 6]
tags: [vault, rung1, tests]
---

# Rung 1 · test suite / acceptance (self-contained)

Rung 1 must be **testable without dispatch**. TDD encouraged (write alongside #2–#6).

**Scope:**
- Store: encrypt/decrypt roundtrip; atomic write leaves no partial/`.bak`; no-write-when-unchanged.
- Principals: register/list, duplicate rejection, revoked flagging.
- Secrets: `set`/`get` roundtrip; `check` healthy + orphan detection.
- Compose: authorized active secrets returned; typed denials for revoked/unauthorized/fingerprint-mismatch; **fail-closed** on unregistered/inactive principal.
- `vault init`: genesis produces valid blob with `core` + nonce; refuses to clobber.

**Acceptance:** `go test ./internal/vault/...` green; `go vet` / `golangci-lint` / `gofmt` clean.

## Narrative
- **Source:** handoff §"Build sequence — Rung 1" ("Self-contained, testable without dispatch").

### Done 2026-06-01
- Per-component coverage landed with #2–#6 (28 tests). #7 adds the missing tie-it-together piece: `integration_test.go` — two end-to-end tests through the **real persistence boundary**.
  - `TestRung1EndToEnd`: genesis on disk → register plugin → grant secret → Save → **fresh Load** → core/nonce survived, Compose delivers exactly the granted secret (no denials), admin token never delivered + intact, Check healthy.
  - `TestRung1RevocationSurvivesReload`: status change persists; Compose's fail-closed denial holds after round-trip.
- Acceptance: **30 vault tests green**; `gofmt`/`go vet`/`golangci-lint` clean; full `go test ./...` green. All Rung-1 self-contained (no dispatch).
