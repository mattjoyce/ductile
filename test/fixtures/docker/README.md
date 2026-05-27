# Docker Test Fixtures

This directory contains fixture-driven Docker/system test scenarios for the staged testing harness.

Current fixtures:
- `webhook-ingress`
- `scheduler-recovery`
- `api-e2e`
- `file_watch`
- `hook-route-compilation`
- `sync-terminal-route`
- `conditional-with-route`
- `pipeline-level-if`

Current status:
- harness base is scaffolded
- fixture execution wiring exists
- existing fixtures cover webhook ingress, scheduler recovery, API e2e, and plugin/runtime behavior
- route-runtime regression fixtures cover hook-entry `call:` expansion and synchronous terminal result selection
- a route-runtime regression fixture covers compiled `if:` branching plus `with:` remapping on the true branch
- `file_watch` fixtures prove append-only `plugin_facts`, derived compatibility state, and operator inspection via `ductile system plugin-facts`
- the `pipeline-level-if` fixture covers the trigger-level `if:` predicate end-to-end across `on:` and `on-hook:` paths
