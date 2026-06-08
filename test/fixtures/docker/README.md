# Docker Test Fixtures

Fixture-driven Docker/system test scenarios for the staged testing harness. Each
subdirectory is one black-box scenario with an executable `run.sh`; the runner
(`scripts/test-docker-runner`) auto-discovers every directory here, so **this README is
the source-of-truth index** — keep it in sync when you add or remove a fixture.

Run one: `./scripts/test-docker <name>` · Run all: `./scripts/test-docker all`

## Tiers

Fixtures are gated deliberately (see `docs/TESTING.md` §9):

- **PR gate** — runs on every PR/push (`docker-validation` job). The load-bearing minimum.
- **Nightly / on-demand** — runs in the `docker-validation-full` job (nightly cron +
  `workflow_dispatch`) and locally via `test-docker all`. Catches the long tail without
  slowing every PR.

### PR-gate fixtures
| Fixture | Proves |
|---|---|
| `webhook-ingress` | inbound webhook accept/reject (signature, oversize), queued work after ingress |
| `scheduler-recovery` | restart/orphan-job recovery from persisted runtime state |
| `api-e2e` | authenticated API + pipeline triggers against real config |
| `vault-secret-delivery` | vault stack black-box acceptance — secret delivery / fail-closed |
| `sync-terminal-route` | synchronous API result selection against compiled terminal routes |

### Nightly / on-demand fixtures
| Fixture | Proves |
|---|---|
| `hook-route-compilation` | hook-entry `call:` expansion; one hook job; root-level dispatch |
| `conditional-with-route` | compiled `if:` branching + `with:` remapping on the true branch |
| `pipeline-level-if` | trigger-level `if:` predicate end-to-end across `on:` / `on-hook:` |
| `context-aware-trigger-if` | pipeline `if:` evaluating upstream durable `context.*` |
| `from-plugin-scoping` | `from_plugin:` source selector matching/suppression |
| `fanout-dedupe-scope` | fan-out dedupe scoping |
| `config-view-redaction` | secret redaction in `GET /config/view` |
| `reload-lifecycle` | config reload/restart lifecycle behavior |
| `file_watch` | append-only `plugin_facts`, compatibility state, `ductile system plugin-facts` |
| `folder_watch` | folder-watch plugin runtime behavior |
| `file_handler` | file-handler plugin runtime behavior |
| `fetch-plugin` | fetch plugin runtime behavior |
| `sys_exec` | `sys_exec` shell-command plugin behavior |
