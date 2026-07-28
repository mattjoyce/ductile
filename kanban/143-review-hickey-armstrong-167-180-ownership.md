---
id: 143
status: doing
priority: High
blocked_by: []
tags: [review, hickey, armstrong, privsep, ownership, lens-pass]
---

# Review: Hickey × Armstrong pass on #167–#180 (service-read artifact ownership)

**Lens pass, 2026-07-29.** Structure-at-rest (Hickey) × behaviour-under-fault (Armstrong) audit of
branch `test/card180-enforce-posture`: new `internal/fsown` ownership package, integrity/admission
cause-preservation (#169–#174), SQLite state-file hardening (#171), doctor alias-following (#168),
privileged CI gates (#175), and the live privsep posture fixture (#167, #180). Eleven commits,
~1970 added lines across 21 files. "After — code audit" variant. Branch was unpushed and uncarded
at review time — this is the first substantial privsep work since the lens practice started.

## Throughline

The branch fixes six ownership bugs at six write sites and **never states the policy once**.
`ApplyToTemp` aborts if it is root and the chown fails; `Apply` never aborts; `secureSQLiteFiles`
discards the result entirely. Three different fault policies for one class of fault, each defended
only by its own comment. Both lenses arrive at the same fix from opposite directions — Hickey sees
*authority braided into failure policy* (`os.Geteuid()` sampled inside the function that decides
whether to abort), Armstrong sees *defensive handling standing in for a supervisor*. The deepest
single change is to lift the policy out of `fsown` and name a supervisor that re-asserts the
invariant, rather than trusting six write paths to each get it right forever.

## Findings → cards

- **H1** (Med) — `fsown` complects three contracts under one prefix: `Desired` is a pure query,
  `ApplyToTemp` is a mutation that is *fatal when root*, `Apply` is a mutation that is *never* fatal
  and silently chmods too. `Apply`/`ApplyToTemp` read as variants of one operation; they have
  opposite failure contracts. Split decision / enforcement / policy.
- **H2** (High) — `os.Geteuid()` inside `ApplyToTemp` makes the error contract depend on ambient
  process state. Identical inputs behave differently by launch method, and the function cannot be
  tested without manipulating process identity — which is exactly why #175 needed a user-namespace
  CI job. **The awkward test is the design smell surfacing.** Pass the policy in.
- **H3** (Low) — `Apply` returns one `bool` for two operations and three distinct failure causes
  (can't stat / no platform opinion / chown refused); the chmod result is dropped. Every caller
  discards it.
- **A1** (High) — no `error kernel` boundary is drawn. The branch's implicit answer ("the .checksums
  write must not fail; the state DB may") is correct but is embedded in two functions rather than
  stated once and made configurable.
- **A2** (High) — **no supervisor for the ownership invariant.** Ownership is fixed at write time on
  ~6 paths and never re-checked. A missed path — or a future one added without routing through
  `fsown` — resurfaces exactly as #167 did: at the next boot, far from the cause. `doctor` is the
  obvious supervisor and is already agent-drivable (`GET /system/doctor`, idiom 8). One check —
  *every service-read artifact is readable by the service account* — would have caught #167, #169,
  #170 and #171 as a class instead of one at a time.
- **A3** (Med) — `secureSQLiteFiles` documents its own unsupervised gap: a sidecar recreated later
  in the process's life inherits the umask, not the call. Invariant holds at open, may lapse after,
  nothing detects it. Closed by A2's doctor check.
- **A4** (Low) — `writeFileAtomicWithBackup` publishes `path.bak` and *then* chowns it, so the
  atomicity `ApplyToTemp` documents does not hold at that call site; a refused chown as root aborts
  the write leaving a root-owned `.bak`. The rollback path becomes the thing that recreates the bug.

## Fixed in this pass — 2026-07-29

- **`internal/fsown/owner_other.go` did not compile.** Unused `os` import, and `Apply` missing while
  the untagged `internal/storage/sqlite.go` calls it. Added `Apply` (enforces the mode ceiling,
  reports no ownership opinion) and a scoped CI gate that builds the package for windows/plan9/js so
  the build-tag siblings cannot drift again. **Scoped deliberately** — ductile as a whole does not
  cross-compile (`internal/lock`, `cmd/ductile/system_state.go`, `system_status.go` use
  `syscall.Flock`/`Kill` with no portable sibling), so a repo-wide GOOS matrix would fail on
  pre-existing code and train everyone to ignore it.
- **`loadPluginFingerprintRecords` claimed a fix it had not made** (#173). Both before and after the
  reorder it returned `nil` on an unreadable manifest, so the snapshot still showed a clean plugin
  table for a box whose attestation state was unknowable. Resolved per A1 — absent and unusable are
  now different verdicts: absent returns nil (integrity never enabled), unusable emits one
  `Available: false` record per configured plugin carrying the cause. Table-driven test added
  covering unsupported-version, unparseable and absent; **verified non-vacuous** by reverting the
  fix and watching it fail.
- **Two of the branch's four new syscall-using test files were missing the build tag** its siblings
  carry (`cmd/ductile/ownership_and_causes_test.go`, `internal/storage/sqlite_perms_test.go`).
- **`sqlite_perms_test.go` skipped absent sidecars**, so it would have stayed green if the WAL/SHM
  half of #171 regressed. Now fatal on absence — the sidecars are verified present at that point.

## Noted, not carded

- `cmd/ductile/config.go:830` — `else if err != nil` after an `err == nil` branch; nilness reports it
  tautological. Pre-existing (2026-05-08), unrelated to this branch.
- §6 file length: this branch extends three files already far over the ~400-line guidance
  (`config_manage.go` 1665, `runtime.go` 1010, `config.go` 946). `appendIntegrityFindings` is the
  natural extraction candidate.
- `gosec` is named a merge gate in `AGENTS.md` §7 and is run by nothing in the repo — no Makefile
  target, no `scripts/` entry, no CI step, no `.golangci.yml`. Live `#nosec G304` annotations go
  unverified. Separate concern from this branch; worth its own card.

## Credit (clean under the lens)

- Collapsing four independent tmp+chmod+rename helpers into one — of which only one was correct — is
  a textbook decomplect, and the commit says so honestly rather than claiming novelty.
- `Desired()` taking intent from the **directory** rather than the artifact being replaced is a real
  value/identity separation: the directory is the stable identity, the file is a succession of
  values. Getting this backwards was the original bug, and the comment records the refutation.
- `fsown.Apply` reconciles via stat/chmod/chown and never opens a descriptor — which matters, since
  it runs against the live state DB inside `OpenSQLite`. That is #179's lesson correctly applied.
- Cause preservation across #169–#173 (`%w`, `errors.Is`, no string matching) is Armstrong's
  don't-hide-corruption applied to diagnostics rather than to state.
- The posture fixture asserts the **refusal messages**, not just exit codes, and `run_as_service`
  refuses to let an exec failure (rc 126/127) masquerade as a refusal. That is testing the
  supervisor's contract instead of its implementation.

## Open

- A2 (doctor supervisor) is the highest-value follow-up and is not yet carded.
- Scenarios 7–9 of the posture fixture have never executed anywhere. Dell run required before push;
  file capabilities are silently stripped on a `nosuid` mount and the fixture's `getcap` check reads
  the xattr, not the runtime set.
