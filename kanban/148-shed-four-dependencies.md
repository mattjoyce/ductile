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

## Slice 1: `github.com/google/uuid` (dropped)

**Dropped 2026-10-28.** `modernc.org/sqlite` imports it through `modernc.org/libc`
(`go mod why -m github.com/google/uuid`), so replacing our nine calls only moves it to
`// indirect`: same module in the binary, same trust surface. Not worth owned code.


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

### Slice 3 design note (2026-10-28, for review; no code until approved)

**What is stored and what compares it** (traced at `origin/main`):

| Value | Stored | Compared later | On mismatch |
|---|---|---|---|
| file hash, `ComputeBlake3Hash` (`config/hash.go:31`) | `.checksums` `hashes`, bare hex | boot and reload (`integrity.go:67`), load of `webhooks.yaml` (`loader.go:647`) | boot refused for high-security files |
| plugin fingerprint, `ComputeKeyedBlake3Hash` (`hash.go:48`) | `.checksums` `plugin_fingerprints[]`, bare hex; copied into `config_snapshots.plugin_fingerprints` | boot and reload (`plugin_fingerprint.go:188`), every spawn (`plugin_verifier.go:107`), `plugin lock --all` diff | enabled plugin: boot refused, spawn denied |
| clearpass seal, `clearpassMAC` (`plugin_clearpass.go:94`) | `.checksums` `...clearpass.seal`, bare hex | spawn (`plugin_verifier.go:114`), closer (`clearpass_closer.go:246`) | treated as no window; closer refuses |
| snapshot `config_hash`, `source_hash`, secret fingerprint (`configsnapshot/snapshot.go:485-532`) | `config_snapshots`, tagged `blake3:` / `secretfp_blake3:` | never re-verified; inspect only displays them | none |
| pipeline fingerprint (`router/dsl/compiler.go:789`) | not stored | in-memory only | none |
| `plugin lock --all` code (`plugin_lock.go:77`) | not stored | same invocation | command refuses |

Only the three `.checksums` values need a migration. Snapshots are append-only history and
already carry an algorithm tag; the DSL fingerprint and the confirmation code are recomputed
every time.

**Proposal**

1. Plain hashes become `crypto/sha256`; the two keyed uses become `hmac.New(sha256.New, nonce)`
   with the same 32-byte vault nonce and the same length check. Rename: `ComputeFileHash`,
   `ComputeKeyedFileHash`. No `Blake3` in any name.
2. `.checksums` goes to `version: 3`, and every value in it is tagged `sha256:` (file hashes,
   both plugin hashes, the seal). The version says the file is new; the tag lets an error name
   exactly which value is old, and matches the snapshot convention.
3. Snapshots: new rows write `sha256:` and `secretfp_sha256:`, secret domain string `v2`.
   Old rows stay as written. `inspect` may show one duplicate secret use across the upgrade
   boundary (`mergeSecretUses` keys on the fingerprint): display only, accepted.
4. DSL fingerprint and confirmation code: `sha256:` and first 5 hex of SHA-256. No migration.
5. **Hard cut.** The new binary refuses a `version: 2` manifest at boot, reload and
   `config check` with one error naming the fix:
   `.checksums was written by an older ductile (BLAKE3); run 'ductile config lock'`.
   No BLAKE3 verify path survives, so the dependency leaves in the same PR.
6. **One `config lock` migrates everything.** Today `PreservePluginFingerprints` copies old
   plugin hashes forward, which would leave every plugin mismatching. On a `version: 2` manifest,
   `config lock` instead re-hashes every plugin that has a recorded fingerprint, under the new
   algorithm, keeping its `enabled` flag. No `plugin lock --all` step. This trusts the plugin
   bytes on disk at upgrade time: accepted, the instances are trusted infrastructure (principal,
   2026-10-28). It prints the plugins it re-hashed.
7. Open clearpass windows are invalidated; the v2-to-v3 `config lock` drops clearpass records.
   Checked: no wedge. The closer treats a seal that does not verify as "ignored, plugin stays
   denied" and logs it per sweep (`clearpass_closer.go:246`); it never fails hard. And the new
   binary refuses a v2 manifest before the closer runs, so it never sees an old seal.
8. Rollback: an old binary cannot read a v3 manifest. `config lock` already keeps a backup
   (`config_manage.go:416`); the runbook says roll back by restoring that backup together with
   the old binary.

**Decisions for the principal**

- Hard cut: **decided** (principal, 2026-10-28), with plugins re-hashed in place during the cut.
- Version: **v1.1.0** (principal, 2026-10-28): no new capability, so not a major. Ships with card
  145. The one-time relock is called out at the top of the CHANGELOG entry and in the boot error.
  (Strict SemVer would make a boot refusal a major; the principal chose minor knowingly.)

**Out of scope, noticed:** `scopes_hash` in `docs/ARCHITECTURE.md` and `docs/CONFIG_REFERENCE.md`
describes the retired `tokens.yaml` and nothing reads it.

**Docs to update in the same PR:** AGENTS.md (3d, constraint 6), ARCHITECTURE, CONFIG_REFERENCE,
GLOSSARY, OPERATOR_GUIDE (upgrade note: one `config lock`), SECRETS, REMOTE_EVENT_RELAY,
`schemas/tokens.schema.json`, `skills/ductile/references/config.md`, the CLI skill template, CHANGELOG.

## Done

- `go.mod` lists neither `go-chi/chi`, `zeebo/blake3`, nor `klauspost/cpuid`,
  and no `tool` line.
- The merge-grade gates in AGENTS.md section 9 pass, plus `scripts/test-docker`.
- A real instance relocks and boots on SHA-256, verified on the Dell.

## History

- 2026-10-28: carded from the dependency survey.
- 2026-10-28: slice 1 dropped (uuid stays via sqlite); slice 0 approved on `deps/card148.0-deadcode-tool-pin`.
- 2026-10-28: slice 3 design note written, awaiting review.
- 2026-10-28: slice 3 design approved: hard cut, plugins re-hashed in place, ships in v1.1.0 with card 145.
