---
id: 148
status: backlog
priority: Normal
tags: [dependencies, supply-chain, integrity, api]
---

# Shed four dependencies: the deadcode tool line, uuid, chi, blake3

> Each one is replaceable by the standard library or a few lines Ductile owns, and none buys a
> capability Ductile uses. Fewer modules is less to trust, audit and keep current.

Survey of 2026-10-28 (main at `7798773`): 8 direct and 17 indirect modules. Kept on purpose:
`filippo.io/age` (never clean-room crypto), `modernc.org/sqlite` (core), `golang.org/x/sys`
(Go team), `gopkg.in/yaml.v3` (hard; its own question). `santhosh-tekuri/jsonschema` is a
separate decision: it backs only `config validate`, a second validator beside `config check`.

One card, four independent slices. Each slice is its own commit and can land alone.

## Slice 0: the `tool golang.org/x/tools/cmd/deadcode` line

- Drop the `tool` directive from `go.mod`; it alone pulls `x/tools`, `x/mod`, `x/sync`,
  `x/telemetry` into the module graph. They are not in the binary.
- Run deadcode pinned instead: `go run golang.org/x/tools/cmd/deadcode@<version>` in whatever
  script or doc calls it today.
- Done when `go mod tidy` removes those four and deadcode still runs from its script.

## Slice 1: `github.com/google/uuid`

- Only `uuid.NewString()` is used: 9 calls in 7 files (`configsnapshot`, `relay`, `router`,
  `api/handlers`, `dispatch`, `queue`, `state`).
- Replace with an owned v4 generator over `crypto/rand` (RFC 9562 version and variant bits),
  about 15 lines, in one small internal package. Same string shape, so stored job IDs and
  anything parsing them are unaffected.
- Test: format and version/variant bits, and uniqueness over a large sample.

## Slice 2: `github.com/go-chi/chi/v5`

- Used in `internal/api` (server, management, handlers, stopwatch), `internal/relay/receiver`,
  `internal/webhook/server`: `NewRouter`, `Group`, `With`, `Use`, `URLParam`, and middleware
  `RequestID`, `RealIP`, `Recoverer`, `NewWrapResponseWriter`, `GetReqID`.
- Go 1.22+ `http.ServeMux` covers method patterns and `{name}` wildcards (`r.PathValue`).
  `Group`/`With` become plain handler composition. The four middlewares are owned code.
- **Sequence after card 145.** 145 rewrites the same route table in `internal/api/server.go`
  (`requireScopes` becomes `require(action, capability)`). Doing chi first means rebasing 145
  through a router change; doing it after is a mechanical port of the new table.
- **`RealIP` is a decision, not a port.** It trusts `X-Forwarded-For` / `X-Real-IP` from any
  client. Decide whether Ductile should honour those at all (only behind a configured proxy?)
  and write that down; do not silently reproduce chi's behaviour.
- **Request IDs are operator-visible** (`host/random-000095` in every log line). Keep the
  format or note the change in CHANGELOG; agents grep for it.
- Parity: a table test over every registered route (method, path, expected handler, auth
  layer) run against both routers before chi is deleted, plus 404/405 behaviour and a panic
  through `Recoverer`.

## Slice 3: `github.com/zeebo/blake3` (and `klauspost/cpuid/v2`)

Not a capability Ductile uses: BLAKE3's gains are throughput on large inputs, tree hashing, XOF
and verified streaming. Ductile hashes small files and short strings. zeebo/blake3 is one
maintainer, pre-1.0, with runtime-selected AVX2/AVX-512 assembly and no known audit;
`crypto/sha256` is Go-team owned, in the FIPS 140 module, hardware-accelerated on amd64 and
arm64.

**Design before typing** (AGENTS.md section 2: integrity is a contract). Write the note on this
card and get it reviewed before code.

- Call sites: `config/hash.go` (`ComputeBlake3Hash`, `ComputeKeyedBlake3Hash`),
  `config/plugin_lock.go` (lock digest), `config/plugin_clearpass.go` (seal),
  `configsnapshot/snapshot.go` (file hash, secret fingerprint), `router/dsl/compiler.go`
  (pipeline fingerprint).
- Plain hashes become `sha256.Sum256`. The two keyed uses (plugin fingerprints under the vault
  nonce, the clearpass seal) become `hmac.New(sha256.New, key)`.
- Tag the algorithm everywhere a hash is stored: `.checksums` values gain `sha256:`; the
  snapshot and DSL fingerprints already carry `blake3:` and move to `sha256:`; the secret
  fingerprint domain string goes to `v2`. Rename the functions (no `Blake3` in names).
- Migration: an upgraded gateway refuses an old manifest until `ductile config lock` is re-run.
  Decide: hard cut (three instances, one relock each, runbook already says relock after edits)
  or accept untagged BLAKE3 for one release. Recommendation: hard cut, with a boot error that
  names the fix. Open clearpass windows are invalidated; they are short-lived by design.
- Docs: AGENTS.md section 3d constraint 6 names BLAKE3; ARCHITECTURE, CONFIG_REFERENCE,
  GLOSSARY, OPERATOR_GUIDE, SECRETS, REMOTE_EVENT_RELAY, the CLI skill template, CHANGELOG.
- SemVer: an existing `.checksums` stops verifying, so this is operator-visible. Fold it into
  the same major/minor decision as card 145's boot refusal.

## Done

- `go.mod` lists neither `google/uuid`, `go-chi/chi`, `zeebo/blake3`, nor `klauspost/cpuid`,
  and no `tool` line.
- The merge-grade gates in AGENTS.md section 9 pass, plus `scripts/test-docker`.
- A real instance relocks and boots on SHA-256, verified on the Dell.

## History

- 2026-10-28: carded from the dependency survey.
