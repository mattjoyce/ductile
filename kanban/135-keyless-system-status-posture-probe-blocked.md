---
id: 135
status: done
priority: Medium
tags: [cli, system-status, posture, privsep, observability]
---

# Keyless `system status` reports DEGRADED and never reaches the posture probe

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Lamport×Thomas/Hunt F3, empirically confirmed against a live management-posture daemon).

## The concern
`system_status.go:107-129` early-returns when `config.Load` fails; the `checkBootPosture` probe is
appended only after a successful load (`:150-152`). A zero-token bootstrap config only validates
when the **caller** can decrypt the vault (`loader.go:166-168, 780-783`). On the #112
credentialed/hybrid posture (age.key 0600 service-owned, operator CLI keyless), keyed status shows
`boot_posture: management-only (live)` but keyless status shows `DEGRADED / config_load: FAIL` and
no posture line — the #130 anti-strand signal is invisible to the operator it was built for.
Keyless `system reload` (`system_state.go:674-688`) refuses for the same reason, though it only
needs a PID and SIGHUP. `checkBootPosture` itself needs nothing from the vault — just the socket
path and listen address.

## Fix
Run `checkBootPosture` independently of the validate-gated load (derive socket/listen via a raw
load, or also emit it in the load-failure branch). Alternatively: have `validate` treat "vault blob
exists on disk but caller is keyless" as vault-present for the api-tokens rule — the blob's
existence, not its decryptability, is what legitimises the posture.

## Done when
Keyless `system status` against a live management-posture daemon shows `boot_posture` instead of a
misleading `config_load: FAIL`.

## Narrative
- 2026-06-10: Took the independent-probe option (Matt's call; the blob-counts-as-present alternative
  would have relaxed validation for every keyless caller). New config.LoadLenient (no verification,
  no validation — observability only) feeds checkBootPosture in the config_load failure branch, so a
  keyless operator on a #112 credentialed host sees the live #130 anti-strand signal. Strict-load
  semantics untouched; keyless `system reload` remains follow-up if wanted. (by @assistant)
