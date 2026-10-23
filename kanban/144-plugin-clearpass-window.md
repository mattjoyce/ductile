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
  forward verbatim like the rest of the fingerprint.
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

## Narrative

- **Source:** operating card 208 on the unraid gateway (importer tree swap, hand
  attestation, quiet refusal); the operator's own proposal of a time-boxed clearpass.
- **Related:** #12 (keyed attestation), #25 (mismatch escalation), #93 (fingerprint-bound
  account grant). None of them give the operator a way to pre-authorise a change.
