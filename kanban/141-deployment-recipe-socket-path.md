---
id: 141
status: backlog
priority: Low
tags: [docs, deployment, bootstrap, unix-socket]
---

# DEPLOYMENT.md bootstrap recipe dials a socket path the daemon doesn't serve

> Raised 2026-06-10, dual luminary code review of `feat/129-vault-operable-posture`
> (Hickey×Armstrong F5).

## The concern
`docs/DEPLOYMENT.md:581` sets `SOCK="$CFG/vault-admin.sock"` with the comment "api.management_socket
(defaults beside the state DB)" — but never actually sets `api.management_socket`. The real default
is `filepath.Dir(cfg.State.Path) + "/vault-admin.sock"` (`cmd/ductile/runtime.go:863-868`), and the
default state path is `./data/state.db` (`internal/config/types.go:854-855`; deploys use
`./state/ductile.db`) — so the daemon serves `$CFG/data/vault-admin.sock` or
`$CFG/state/vault-admin.sock`, never `$CFG/vault-admin.sock`. The recipe's
`vault set --api-url "unix://$SOCK"` step fails with connection refused on any standard layout.
(The `system status` step is unaffected — `checkBootPosture` derives the path from config.)

## Fix
Have the recipe explicitly add `api.management_socket: "$CFG/vault-admin.sock"` to api.yaml (as the
fixtures do — `scripts/test-docker-vault-lib:35`), or compute `SOCK` from the state path.

## Done when
The from-scratch recipe executes end-to-end as written on a fresh directory. (Pairs with #133,
whose `config check` step is the recipe's other breakage; #131's acceptance test should catch both.)
