# System-Tier Fixtures (live gateway, black-box)

Each fixture boots a REAL `ductile system start` and drives it from outside
(curl + CLI + sqlite on the artifact tree). The tier is curated (#118): a
fixture exists only for a property that can ONLY be proven against a live
booted gateway — routing/state/predicate logic lives in the in-process suites
(`internal/router`, `internal/queue`, `internal/dispatch`).

Run: `./scripts/test-docker [fixture-name]` — builds the binary and execs each
fixture's `run.sh` natively (no Docker required). Vault-native fixtures walk
the credential ladder from genesis via `scripts/test-docker-vault-lib`
(`fixture_vault_init`, `fixture_bootstrap_vault`): keygen + vault init →
management posture (mint the api token over the unix socket) → lock → gateway.
No fixture carries a literal token (#94).

Fixtures, each with its live-only property:

- `boot-refuses-bad-config` — fail-closed boot: a literal api token (#94) and a
  credential-less enabled API (#119) both refuse to start, non-zero exit, never
  a half-boot.
- `config-view-redaction` — `/config/view` redacts inline secrets; snapshot
  fingerprint is stable; secret-only rotation shows restart drift.
- `plugin-crash-leaves-deterministic-state` — a plugin subprocess dies by
  uncatchable SIGKILL mid-job: the job lands terminal `failed`, the daemon
  keeps serving AND dispatching, no non-terminal queue rows remain.
- `reload-lifecycle` — the running daemon survives two live `/system/reload`
  cycles and plugins keep firing.
- `sys_exec` — polyglot subprocess round-trip: a python plugin spawns, does
  work, and writes to disk through the live API path.
- `vault-secret-delivery` — secrets delivered to a plugin over stdin; a
  reserved-name read is refused and audited.
- `webhook-ingress` — real webhook ingress: valid signature → 202 + job
  enqueued, invalid → 403.

Conventions: short `/tmp/...` unix-socket paths (sun_path limit); unique
listen ports per fixture (18081/18181/18381/18471/18561/18681);
`service.unconfined: true` unless the fixture tests privsep (#86) — root-run
hosts refuse to boot otherwise; bootstrap configs must not reference a
`secret_ref` that has not been minted yet.
