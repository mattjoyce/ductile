---
id: 144
status: doing
priority: High
blocked_by: []
tags: [plugin, attestation, vault, operability, security]
---

# Plugin · clearpass: relax attestation for one plugin, for a bounded period

## Problem

Attestation is an operator act that can only be computed in place: `plugin lock`
resolves a plugin through the live config's plugin roots and keys the hash with the
vault nonce. There is no way to bless bytes before they are live, so every plugin-tree
release on a scheduled gateway becomes a window of hand steps: stage a copy beside the
live tree, attest the changed plugins against the staged tree with a helper that lives
in `/tmp`, lock, swap, restart, health-check. Get the order wrong and the heartbeat
spawns into a mismatch, the job fails closed, and nothing leaves the box to say so.

The strict rule is right. What is missing is a way for the operator to say, before the
change, "I am about to change this plugin; accept what you find for the next two hours,
and tell me what you accepted."

## Design

Clearpass delegates the attestation act to the gateway under an explicit, per-plugin,
time-boxed pre-authorisation recorded beside the fingerprint it relaxes.

- **Open:** `ductile plugin clearpass <name> --for <duration> [--note <text>]`. Needs
  the vault and a recorded fingerprint for the plugin (clearpass relaxes an
  attestation; it does not stand in for one). Duration is required, capped at 24h.
  Written into `.checksums` as `plugin_fingerprints[].clearpass{opened_at, until, note, seal}`.
  `seal` is keyed-BLAKE3 under the vault nonce over the plugin name, the recorded hashes
  and the window's fields, so opening needs the vault (like `lock`), a `.checksums` write
  without the vault cannot open a window, an edited window is dead, and a `plugin lock`
  invalidates any stale one. The verifier ignores an unverified record and names it in
  the denial; the closer never commits under one. `config lock` carries the record
  forward verbatim like the rest of the fingerprint. Each window also has a random
  `id` inside the seal, and opening writes a `clearpass_open` audit row; the closer
  commits only a window the audit log saw opened and not yet closed, and only within
  24h of its deadline, so a sealed record replayed from a backup stays inert. Residual:
  an attacker who can write both `.checksums` and the state database, and holds a
  sealed record less than two days old, is the same attacker who can read the age key
  beside them; attestation notices change, it is not a wall against a local writer.
- **Inside the window, at spawn:** the verifier compares as today. On a mismatch with an
  open window it tolerates the spawn, returns no error to either gate (secret delivery
  and the privsep downgrade both consult the same verifier), and reports through a
  callback: a WARN log with recorded and live hashes and the window, a
  `plugin.clearpass_spawn` hub event, and a `clearpass_spawn` vault-audit row. Not a
  SECURITY line: the operator asked for it. The secret path is unchanged on a match.
- **Close:** the gateway closes it. A closer loop checks at boot and once a minute; when
  `until` has passed it recomputes the fingerprint from the live bytes, commits it as
  the attestation, clears the window, and reports "committed X (was Y), observed in N
  spawns" or "unchanged, never spawned" via INFO log, `plugin.clearpass_closed` event
  and a `clearpass_close` audit row. A window with no spawn still commits, so a plugin
  that runs weekly does not hit the strict wall after close.
- **Manual close:** `ductile plugin clearpass <name> --close` commits now from the CLI
  (needs the vault for the nonce, like `lock`). `plugin lock <name>` also clears any
  window, since lock is the explicit commit. `--list` shows open windows; the boot log
  names each open window.
- **After `until`, before the closer runs:** strict. The mismatch error names the expired
  window so the operator sees why.

Out of scope, reported separately: the fingerprint covers manifest and entrypoint only;
a route from core events to a notifier plugin; a `plugin lock --from <dir>` for the case
where the bytes are known in advance.

## Acceptance

1. `.checksums` round-trips a clearpass record; `config lock` preserves it;
   `plugin lock <name>` clears it.
2. Verifier: an unsealed or edited window is ignored and named in the denial, and the
   closer never commits under one; mismatch inside an open sealed window passes and fires the callback once per
   spawn with both hashes; mismatch outside the window denies and names the window
   when it has expired; a match inside the window passes without the callback.
3. Closer: an expired window is committed to the live bytes, cleared, and reported with
   the observed count; an open window is left alone; the closer runs at boot.
4. CLI: open refuses an unattested plugin, a missing duration, and a duration over 24h;
   `--close` commits and clears; `--list` prints open windows; help lists the action.
5. Docs: GLOSSARY, SECRETS.md, OPERATOR_GUIDE.md and the ductile skill name the act.
6. Gate green: gofmt, vet, golangci-lint, gosec, `go test -race -count=1 -shuffle=on`.
7. Exercised on the local Mac instance: open, change a plugin byte, spawn passes with the
   WARN line and audit row, close commits, strict rule resumes.

## Done (2026-10-23, branch plugin/card144-clearpass, 33ff6a1..b09a461)

- Built and gated (gofmt, goimports, vet, golangci-lint 0 issues, gosec clean,
  `go test -race -shuffle=on` green on cmd/ductile and internal/config).
- Exercised on a scratch local gateway with its own vault: accepted spawns reported with
  both hashes, gateway closed the window on the clock ("committed X (was Y), observed in
  N spawns"), strict spawn passed on the new bytes, a swap with no window was refused with
  the SECURITY alert; CLI `--close` and `plugin lock` paths run too.
- Installed on the Mac dev instance (v1.0.1-23-gb09a461) and verified with a no-change
  window on echo: opened by CLI, closed by the gateway "unchanged, never spawned".
- Review hardening after the first cut: window sealed under the vault nonce; per-window
  id plus open/close audit trail checked before commit, 24h commit grace; close rows
  written before the record changes, fail closed.
- Out of scope, seen on the way: `internal/api` management-posture tests and
  `internal/dispatch` TestSpawnPluginTimeoutKillsProcessGroup flake under -race -shuffle
  on main (the latter is fixed on fix/ownership-supervisor-and-gates); pre-existing
  errcheck and gosec G204 hits on untouched files; the fingerprint still covers manifest
  and entrypoint only; no route from core events to a notifier plugin.

## Field test on unraid (2026-10-26, v1.0.1-25-g957b5e4)

Exercised on the unraid gateway with a real byte change on `owntracks` (a vault principal:
on an unconfined gateway only the secret path re-verifies at spawn, so a non-principal like
`echo` runs either way and shows nothing). Full timeline in unraid_admin
`vault/Clearpass Test B450.md`.

- **Open and spawn: pass.** Window opened from a writable one-shot; the changed `run.py` was
  accepted at spawn with the WARN line and a `clearpass_spawn` row, and got its secrets.
- **Gateway close: fails.** The live container mounts `/app/config` read-only, so the closer
  cannot write `.checksums` (`failed to create checksums temp file`). The plugin is then
  denied with the SECURITY line until `plugin lock`. Fail-closed, but the gateway never
  commits on a read-only-config deployment, and nothing warns before the deadline.
- **A failed write uses up the window.** `consume` writes the `clearpass_close ok` row before
  the `.checksums` write, so `admit` reads the window as already closed on every later sweep
  and the closer never retries. This contradicts the `closeOne` comment ("the next sweep tries
  again"). The `error` row written afterwards does not undo it.
- **The close row claims a commit that did not happen:** `committed entrypoint 5dc99b230ad6
  (was 914bd908827e)`, 13 ms before the write failed.

Changes requested:

1. At boot and each sweep, WARN when a window is open and the config dir is not writable,
   naming the window, the deadline and `--close`.
2. Do not use up a window on a failed write: probe that `.checksums` can be written before
   `consume`, or write the row as pending and let a later `error` row for the same id reopen it.
   Keep the replay protection.
3. Keep "committed" for a row written after `.checksums` changed.
4. OPERATOR_GUIDE and SECRETS.md: a read-only-config gateway cannot close its own windows;
   close with the CLI from a writable one-shot before the deadline.
5. Not yet exercised anywhere: `--close` from a one-shot with changed bytes.

Unraid holds config read-only until this lands and closes windows by hand.

## Narrative

- **Source:** operating card 208 on the unraid gateway (importer tree swap, hand
  attestation, quiet refusal); the operator's own proposal of a time-boxed clearpass.
- **Related:** #12 (keyed attestation), #25 (mismatch escalation), #93 (fingerprint-bound
  account grant). None of them give the operator a way to pre-authorise a change.

### Fix (2026-10-26, branch plugin/card144-readonly-close)

1. **Warn:** each sweep, an open window on a config directory that fails a write probe
   logs a WARN naming the plugin, the deadline and `ductile plugin clearpass <name> --close`.
2. **No window used up by a failed write:** the close is two-phase. The probe runs first
   (a read-only directory stops with nothing recorded); then a `pending` row consumes the
   window; then `.checksums` is written; a failed write records `error`, which reopens it.
   `admit` reads the newest state-changing close row (`pending`/`ok`/`superseded` closed,
   `error` open); `refused` rows change nothing, so a refusal can never reopen a replay.
   `plugin lock` writes `error` after a failed write too.
3. **"committed" only after the write:** the `pending` row says "closing: will commit";
   the `ok` row is written after `.checksums` changed.
4. **Docs:** OPERATOR_GUIDE and SECRETS.md say a read-only-config gateway cannot close
   its own windows and how to close one from a writable one-shot.
5. **Not yet exercised in the field:** `--close` from a one-shot with changed bytes (the
   CLI test covers it against a local fixture).
- Also: refusals are audited once per window and kind, not every minute.
