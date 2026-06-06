---
id: 90
status: backlog
priority: Normal
blocked_by: [86, 87]
tags: [privsep, testing, acceptance]
---

# Privsep · negative-test suite (aggregate + CI gate)

> **Nav:** [[83-privsep-epic]] · after [[86-privsep-spawn-uid-drop]] + [[87-privsep-filesystem-permissions]] · CI gate over the whole epic

"Privsep is only real if tested" (ADR §9). But the tests are **not born here** — each negative
test lands **with the mechanism it proves** (Beck: a test batched at the end hides which wall
fell). This card is the *aggregation* + the CI gate that keeps them all green.

**Where each test is written (with its mechanism):**
- can't read age key → **#92** (tracer) then generalized in **#87**
- can't read config / state DB → **#87**
- can't read another worker's `state_dir` → **#87**
- can't `ptrace` a sibling plugin (cross-uid) → **#86** — **must use two plugins on DIFFERENT
  workers, never two on shared `default`** (Armstrong, B4): same-uid siblings aren't cross-uid, so a
  `default`/`default` pair passes trivially and reads green over the known residual. The suite must
  also carry an explicit note that **sibling isolation *within* `default` is NOT covered** (it's the
  accepted shared-uid residual — see [[83-privsep-epic]]), so a reader doesn't mistake green for it.
- only allowlisted env (regression-lock of shipped 1a) → **#86**
- only granted tokens, never the full set → **#85**
- fingerprint mismatch downgrades/denies → **#93**

**Scope of THIS card:**
- Collect the above into one suite in the end-to-end harness; wire it as a **CI gate**.
- Tests **skip cleanly (not silently pass)** on hosts without a `CAP_SETUID` gateway (dev Mac)
  so a missing-privilege env can't mask a real regression.

**Acceptance:** every §9 assertion has an executable test that fails if breached; the suite is
a CI gate; on a non-privileged host the privsep tests report *skipped*, never *passed*.

## Narrative
- **Source:** PrivSec ADR §9, **re-sliced by the Brooks×Beck review** so tests ship with their
  mechanism and this card is the aggregator, not a terminal all-or-nothing gate.
- Distinct from the known sub-ms verify→exec TOCTOU on the secret path (#12 §3.3) — don't conflate.
