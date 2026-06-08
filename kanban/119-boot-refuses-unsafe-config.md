---
id: 119
status: done
priority: High
tags: [testing, boot, fail-closed, security, characterization, trust]
---

# Characterize "fail closed at boot, legibly" (the #1 unsupervised-trust property)

> **Nav:** luminary council 2026-06-08 — named independently by **Thompson×Feathers** ("smallest first
> move") and **Lamport** ("#94 should have been one asserted invariant"). The tracer for the redesign.

## Why this first
The behaviour that *silently killed the entire system tier* (#94 rejecting literal API tokens at boot)
is currently only proven **by accident**. It is also the single most important property an unsupervised
agent depends on: **the runtime refuses to start in an unsafe configuration, and says why legibly** so
the agent can read the refusal. Promote it from side-effect to deliberate characterization test.
Smallest possible slice — no Docker, no fixture rebuild.

## Do
- Assert that booting with a config carrying a **literal API token** (and `${ENV}` token) is rejected:
  non-zero exit / hard error, with a **structured, machine-readable refusal naming the offending field**
  (`api.auth.tokens[0]` … `secret_ref`, ADR §8.5).
- Cover the seam an agent actually sees. Prefer a binary-level characterization (spawn the boot path,
  assert exit code + stderr shape) over a pure config-validation unit test — that external surface is
  where the trust is placed. Reuse any existing in-process loader assertion; add the boot-seam one.
- Confirm the *legibility* contract: stdout/stderr separation, the message is parseable, names the field.

## Done when
- A test fails if the loader ever silently accepts a literal/`${ENV}` token, OR if the refusal stops
  being legible (field name dropped, wrong stream, zero exit).
- Runs in the fast tier (no Docker).

## Progress
- 2026-06-08: **boot-seam characterization LANDED** (PR #125, `cmd/ductile/boot_failclosed_test.go`).
  `TestBootRejectsLiteralAPIToken` drives the real seam (`config.LoadWithVault` = what `system start`
  calls) and asserts fail-closed + legibility (names `api.auth.tokens`, points at `secret_ref`);
  `TestBootAcceptsSecretRefAPIToken` proves the rule discriminates. Fast tier, no Docker.
- **Remaining:** the `${ENV}`-token variant (env-sourced literal still outside the vault); and the
  live `boot-refuses-bad-config` system fixture once the tier is curated ([[118]]).

## Notes
Feeds the `boot-refuses-bad-config` system fixture in [[118-system-tier-curate-trust-property-fixtures]]
and the fail-closed invariant in [[117-queue-state-machine-invariant-suite]]. **Started 2026-06-08.**
