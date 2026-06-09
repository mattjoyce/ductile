---
id: 142
status: backlog
priority: Low
tags: [tests, fixtures, healthz, posture, hygiene]
---

# Review residue: two one-line fixes (silent-skip docker probe; healthz "closed" on a live relay-only listener)

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Lamport×Thomas/Hunt F7 + Hickey×Armstrong F9 — judged below card-weight individually; batched).

## 1. Docker-tier "public listener closed" assertion can silently vanish
`scripts/test-docker-vault-lib:54-58`: `fixture_bootstrap_vault` awk-parses `listen` out of
api.yaml (stripping only double quotes) and guards the negative curl probe with
`[[ -n "$listen" ]]` — a failed parse (single quotes, renamed file, indent change) skips the
management-posture "gateway listener is NOT open" assertion without failing the fixture.
**Fix:** `[[ -n "$listen" ]] || fixture_fail "could not parse api listen address from api.yaml"`.

## 2. `/healthz` reports posture "closed" from a listener that is open
With `api.enabled: false` + a relay configured, the server block still runs
(`cmd/ductile/runtime.go:739`) and unauthenticated `/healthz` reports `"posture": "closed"`
(`internal/config/boot_posture.go:19-22,73-74`) from a live TCP listener — the one spot where
reported posture and the live listener set disagree.
**Fix:** suppress the posture field when `!cfg.API.Enabled`, or add a "relay-only" value if that
deployment shape should be observable.

## Done when
A listen-address parse failure fails the fixture loudly; `/healthz` never reports "closed" from a
listener it is served by.
