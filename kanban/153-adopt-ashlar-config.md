---
id: 153
status: todo
priority: Normal
tags: [config, ashlar, dependencies, integrity, privsep, testing]
depends_on: []
---

# Adopt Ashlar for config reading and composition

> Do not tag, do not merge, do not push. The principal merges.

Ashlar (`github.com/mattjoyce/ashlar`, tag `v0.1.0`, `go 1.25.0`) was extracted and generalised
from `internal/config`. Ductile takes it back as a library: Ashlar owns reading, strict decoding,
include traversal, node-level composition with presence, environment layering and provenance;
ductile keeps every policy (which lists append, which keys replace, defaults, the `.checksums`
manifest, clearpass, plugin fingerprints, age decryption, the vault) and attaches it through the
two hooks `IntegrityVerifier` and `ByteTransformer` (`ashlar/yamlconfig/includes.go:24-30`).

Stepwise and heavily tested. Each slice is one branch, one PR, lands alone.

## Problem now

- **The include merge is an allowlist that drops whole keys.** `deepMergeConfig`
  (`internal/config/loader.go:495-617`) copies named fields from `src` when non-zero. Enumerating
  every `src.` reference in that range: it carries `service.{name, tick_interval, log_level,
  log_format, dedupe_ttl, *_retention}`, `state.path`, `api.{enabled, listen, management_socket,
  max_concurrent_sync, max_sync_timeout, auth.tokens, allowed_origins}`, `plugin_roots`, `plugins`,
  `routes`, `instances`, `remote_ingress.*`, `webhooks.*`. It never reads `accounts`, `secrets`,
  `tcc_paths`, `environment_vars`, `database`, `service.max_workers`, `service.admission`,
  `service.strict_mode`, `service.allow_symlinks`, `service.hook_max_depth`,
  `service.unconfined`, `service.plugin_env_passthrough`. Any of those in an included file is
  dropped silently: they are known fields, so the per-file strict decode
  (`strict_decode.go:37-67`) accepts them and never warns. An `accounts:` map or
  `service.unconfined: true` in an include changes nothing, and the privsep boot gate
  (`cmd/ductile/runtime.go:744`) then decides posture from a config the operator did not write.
- **Non-zero wins.** `loader.go:523` `if src.API.Enabled { dst.API.Enabled = ... }`: an include can
  turn the API on but never off; `remote_ingress.require_key_id` (`loader.go:591`) likewise. Any
  explicit `0`, `""` or `false` in an include is dropped. `TestDeepMergeConfigAPIFields`
  (`loader_test.go:1259`) pins the allowlist for API fields only.
- **Interpolation is a regex over raw text.** `interpolateEnv` (`loader.go:743-757`) runs before
  YAML parse on the file bytes (`loader.go:483`) and leaves an unresolved `${VAR}` in place;
  `TestInterpolateEnv` "undefined var unchanged" (`loader_test.go:922`) pins that. Only plugin
  `config:` maps are checked afterwards (`checkUnresolvedEnvVars`, `loader.go:1161`, called at
  `879`), so `${MISSING}` in `service.name` or `state.path` goes through as a literal. Because it
  is text substitution, a value containing `: ` or a newline changes document structure.
- **Schema validation loses line numbers.** `internal/configschema/configschema.go:80-94`
  `yamlToInstance`: YAML → `any` → JSON → jsonschema instance. A violation names a JSON pointer,
  never a file:line.
- **Copies of what Ashlar now owns, hardened there:** `ComputeFileHash`/`ComputeKeyedFileHash`
  (`hash.go:38,55` → `yamlconfig/paths.go:214,225`, same `sha256:<hex>` form, same 32-byte key
  rule), `DetectSymlinks` (`symlinks.go:61` → `paths.go:177`), `expandTilde` (`tilde.go:14` →
  `ExpandPath` `paths.go:77`), `ParseByteSize` (`bytesize.go:11` → `ashlar/number.go:63`, root
  package, not `yamlconfig`), `loadEnvFile` (`loader.go:424-470` → `ParseDotEnv`
  `yamlconfig/dotenv.go:17`), `DiscoverConfigDir` (`loader.go:208` → `Discover` `paths.go:39`).
- **Two YAML modules.** `go.mod` pins `gopkg.in/yaml.v3 v3.0.1` (27 importing files); Ashlar uses
  `go.yaml.in/yaml/v3 v3.0.4`. `santhosh-tekuri/jsonschema/v6` is `v6.0.2` here, `v6.0.3` there;
  only `internal/configschema` imports it.

## Ashlar gaps and ductile behaviours that must be decided (design before typing)

Verified against `ashlar/yamlconfig/decode.go`, `includes.go`, `env.go` and ductile's types.

- **A. `yaml.Unmarshaler` is not honoured.** `WebhookEndpoints.UnmarshalYAML` (`types.go:694`)
  accepts a flat list or `{endpoints: [...]}`. `DecodeValueStrict` honours `time.Duration`,
  `encoding.TextUnmarshaler` and `json.Unmarshaler` only (`decode.go:82-131`). Ductile-side fix:
  add `UnmarshalJSON` to `WebhookEndpoints` accepting both shapes. No Ashlar change.
- **B. Inline maps are not supported.** `TimeoutsConfig.Overrides map[string]time.Duration
  yaml:",inline"` (`types.go:335`) collects per-command timeouts. `indexStructFieldsInto`
  (`decode.go:250-259`) inlines struct kinds only; every override key under `timeouts:` becomes
  `ErrUnknownField`. **Ashlar card**: inline-map support in `DecodeValueStrict`. The alternative
  (changing the config shape to `timeouts.commands:`) is an operator-visible break; not preferred.
- **C. No lenient decode.** Ductile's posture is warn-only on unknown keys unless
  `admission.validate_config_on_boot` (`runtime.go:577-585`); `LoadLenient` (`loader.go:48`)
  must never fail on them. `DecodeStrict` stops at the first unknown field with no struct
  populated. **Ashlar card**: `DecodeOptions{UnknownFields: Collect}` that decodes and returns
  every unknown-field diagnostic (file:line:column). Without it the cutover turns a typo'd key into
  a boot failure on every install, which the docs do not promise.
- **D. Absolute and `..` includes.** `TestLoadAbsoluteIncludePath` (`loader_test.go:1421`) pins
  absolute includes. `cleanFSPath` (`includes.go:344-356`) strips the leading `/` and rejects `..`
  inside the `fs.FS`. Ductile-side: root the FS at `/` (`os.DirFS("/")`) and pass root-relative
  paths; hook callbacks receive the path without its leading slash and prefix it back. No Ashlar
  change. Decide and pin with that test.
- **E. Env includes are declared inside the YAML.** `environment_vars.include` loads and
  `os.Setenv`s before each file is interpolated (`loadEnvIncludes` `loader.go:391`, `Setenv` at
  `459`). Ashlar interpolates once after composition (`applyEnvLayer`, `env.go:37`). Ductile-side
  two-pass: `LoadIncludeGraph` → read `/environment_vars/include` from each fragment →
  `ParseDotEnv` → `EnvBinding.Lookup` over that map then `os.LookupEnv` → `ResolveFragments`. Keep
  the `Setenv` side effect by policy (keys not already set): `buildPluginEnv`
  (`internal/dispatch/env.go:56`) reads `os.Environ()`, so `plugin_env_passthrough` can forward a
  `.env`-loaded variable to a plugin today. Dropping it silently would change plugin environments.
- **F. `${VAR}` in include paths** (`loader.go:310`) is not interpolated by Ashlar. Decide drop;
  confirm no live config uses it (the three instances' configs; the Thinkpad's is unreadable from
  `matt`, so the operator checks).
- **G. Tightenings to accept and document:** a bare integer duration (`tick_interval: 60`) decodes
  in yaml.v3 and fails in Ashlar (`decode.go:84-86`); an empty include file is accepted today and
  refused by `Read` (`yamlconfig/loader.go:45-56`); YAML anchors/aliases are refused
  (`loader.go:217`); an unresolved `${VAR}` anywhere fails closed (`env.go:139-147`). No docker
  fixture config uses anchors, `.yml`, or `${` in an include (grepped); live configs unverified.
- **H. Directory includes widen.** Ductile walks one level, `.yaml` only (`walkDirWithExt`,
  `discovery.go:61-82`; `CONFIG_REFERENCE.md:88` says non-recursive). Ashlar walks recursively
  and takes `.yml` (`includes.go:325-342`). Decide; update the reference either way.
- **I. Composition policy for parity.** `plugins` is `maps.Copy` (`loader.go:565`): a later
  file's plugin block replaces the whole entry (`CONFIG_REFERENCE.md:56` documents this) →
  `PatternRules: /plugins/*: MergeReplace`. Append lists: `/routes`, `/instances`,
  `/plugin_roots`, `/api/auth/tokens`, `/api/allowed_origins`, `/remote_ingress/trusted_peers`,
  `/webhooks/endpoints` → `MergeAppendSlice`. Everything else: `DefaultCompositionPolicy()`
  (maps merge, scalars replace with `Superseded` recorded). `MergeForbidDuplicate` on plugins (the
  relayd choice) is stricter than today; parity first, tighten later if wanted.
- **J. Hook errors are withheld.** `includes.go:15-22` wraps any hook error as
  `withheldHookError` ("hook error withheld from diagnostics"); `errors.Is/As` reach the cause,
  the text does not. Ductile's scope-verification hints ("Run: ductile config lock --config-dir",
  `loader.go:623-653`) are plain `fmt.Errorf` strings today and would vanish. They must become
  typed errors ductile re-renders after `errors.As`. Same class as #172 (a conclusion printed
  where an observation was available).
- **K. `include` is stripped from fragments** (`extractIncludes`, `includes.go:259`), so
  `cfg.Include` is empty after decode; `len(cfg.Include) > 0` as "multi-file mode"
  (`loader.go:168`) becomes `len(resolved.Files()) > 1`.
- **L. Two hashes that must not be confused.** `Fragment.Raw` is post-transform (plaintext after
  age decryption, `includes.go:175-187`), so `Resolved.SourceHash()` hashes plaintext.
  `.checksums` hashes on-disk bytes (`hash.go:38`) and the snapshot `source_hash`
  (`configsnapshot/snapshot.go:501`) is stored and compared across upgrades. Neither is replaced by
  `SourceHash()` in this card.
- **M. SPEC drift in Ashlar (info):** `SPEC.md` §3.4 lists `ParseByteSize` under `yamlconfig` (it
  is `ashlar.ParseByteSize`, `number.go:63`) and `SourceHash() (string, error)` (it returns
  `string`, `resolved.go:37`). Doc fix upstream.

**Rule:** if Ashlar lacks something ductile needs, it is an Ashlar card, not a ductile workaround.
B and C are Ashlar cards and gate slice 4. A, D, E, J, K are ductile policy and need no upstream
change.

## Open integrity issues this card touches (not folded in)

- **#172** (`verifyReloadIntegrity`, `runtime.go:259-284`, swallows causes): slice 4 moves scope
  verification into the `IntegrityVerifier` hook; gap J means the same swallowing would recur
  one layer down unless the typed-error rule above is applied. Slice 4 keeps #172's own two error
  sites as they are; fix #172 on its own branch.
- **#173** (`verifyPluginFingerprintsForConfig` fails open, `config_manage.go:230`): reads
  `.checksums` via `LoadChecksums`; slice 2 replaces `ComputeFileHash`'s body only. No overlap.
- **#174** (`config check` never runs the integrity gate): the config-shapes fixture in slice 3
  runs `config check` and `system start` on the same tree and will show the gap as a documented
  failing expectation. Do not fix it here; link the fixture from #174.
- **#177** (`SetPath` non-atomic write, `access.go:264`): writes through `SourceFiles`
  (`access.go:183`). Slice 1 changes its import path; slice 4 keeps `SourceFiles` populated for
  it. The follow-up that removes `SourceFiles` lands after #177 or coordinates with it.

## YAML and jsonschema convergence (interaction with card 148)

Card 148 kept `gopkg.in/yaml.v3` as "its own question". Answer: the module moves, it does not
leave. `go.yaml.in/yaml/v3 v3.0.4` is the same package under the yaml org's import path; the
exported surface of `gopkg.in/yaml.v3@v3.0.1` and `go.yaml.in/yaml/v3@v3.0.4` is identical
(diffed every exported `func`/`type` across both module-cache copies: no differences). Slice 1
rewrites the 27 import sites, leaving one YAML module in `go.mod`. `jsonschema` moves to `v6.0.3`
by MVS when Ashlar is required (slice 2); if slice 7 lands, ductile's direct requirement on it
goes away and it stays only as Ashlar's dependency, which closes 148's "separate decision".

## Slices

### Slice 0: decision note and Ashlar cards (no code)

- Record decisions for A-M on this card (one line each, with who decided and when).
- Open Ashlar issues for B (inline maps) and C (lenient decode with collected unknown-field
  diagnostics), and the SPEC fix M. Link them here.
- Exit: every item A-M has a decision; the two Ashlar issues exist; the principal has reviewed.

### Slice 1: one YAML module

- Change `gopkg.in/yaml.v3` to `go.yaml.in/yaml/v3` in the 27 files that import it (`rg -l
  'gopkg.in/yaml.v3' --type go`), `go mod tidy`. No other change.
- Tests: the existing suite; plus `scripts/test-docker reload-lifecycle config-view-redaction
  vault-secret-delivery` natively, because those drive the YAML writers (`config set`, `config
  lock`, backup, vault rotate) through a live gateway.
- Exit: `go.mod` lists exactly one YAML module; `rg gopkg.in/yaml` returns nothing; merge gates
  (AGENTS.md §9) pass.
- Lane: native fixtures on the Mac suffice.

### Slice 2: swap the copied helpers behind ductile's names

- Add `github.com/mattjoyce/ashlar v0.4.0`. Rewrite the bodies of `ComputeFileHash`,
  `ComputeKeyedFileHash`, `DetectSymlinks`, `ParseByteSize`, `expandTilde`, the parse half of
  `loadEnvFile`, and `DiscoverConfigDir` to call Ashlar. Keep ductile's signatures; wrap Ashlar's
  `*ashlar.Error` with ductile's existing messages where tests assert text.
- Behaviour deltas to pin, each with a test and a decision: `ExpandPath` rejects `~user`
  (`expandTilde` returns it untouched) → adopt the rejection, error names the field;
  `DetectSymlinks` skips a missing path (ductile errors) → accept, a symlink check on a missing
  file is meaningless; `ParseByteSize` and hash outputs must be byte-identical.
- Parity tests: table-driven old-vs-new over the inputs already in `hash_test.go`,
  `keyed_hash_test.go`, `bytesize_test.go`, `TestExpandTildeForms` (`loader_test.go:2248`),
  `secrets_test.go`; hash equality against the stored manifests in `integrity_test.go` and
  `checksums_legacy_test.go`.
- Exit: the six helper bodies are gone from ductile; `config lock` on a fixture writes the same
  `.checksums` values as before (compare files); gates pass.
- Lane: native.

### Slice 3: parity harness and the config-shapes fixture

- New `internal/config/parity_test.go` (or a `parity` package): an unexported `loadAshlar()` built
  on `yamlconfig.Resolve` with the policy from I, not wired to any caller. Run `load()` and
  `loadAshlar()` over a corpus: every inline YAML in `loader_test.go` (25 tests) and the other
  config tests that build directories (181 tests in the package), every `test/fixtures/docker/*/
  config`, the `cmd/ductile/templates` output of `config init`, and three posture-shaped configs
  built from the docs: accounts map with enforce (shape from
  `docs/runbooks/privsep-thinkpad-enforce.md`), no accounts map and no override (the Mac and the
  `USER ductile` container), and `service.unconfined: true` (every current fixture).
- Compare the two `Config` values as canonical JSON with `SourceFiles`, `ResolvedSecrets`,
  `Pipelines`, `ConfigDir` excluded; compare `Files()` to `SourceFiles` keys.
- Every divergence is a golden entry with a reason code: `BUG-FIXED` (Problem now), `TIGHTENED`
  (G), `WIDENED` (H), `UNDECIDED`. The harness fails on `UNDECIDED`.
- Docker-lane half: new fixture `test/fixtures/docker/config-shapes/` with four cases, each
  running `config check`, `config lock`, `system start` to `/healthz`, one `/system/reload`:
  (1) `accounts:` + enforce (needs `CAP_SETUID`/`CAP_SETGID`; reuse the account scaffolding from
  `privsep-artifact-ownership/run.sh`), expecting `privsep enforcing` in the log; (2) no accounts,
  no override, non-root, expecting `privsep posture: UNCONFINED — no accounts configured`
  (`runtime.go:806`); (3) `service.unconfined: true`, expecting `privsep UNCONFINED: plugins run at
  the gateway uid despite configuration` (`runtime.go:800`); (4) the ro-config-mount flow: config
  bind-mounted `:ro`, `docker exec … config lock` must fail, a one-shot rw container locks
  (`docs/runbooks/unraid-docker-upgrade.md:27-28,73-86`), the gateway's reload admits the new
  manifest. Case 4 needs Docker; cases 1-3 run natively on Linux.
- Exit: harness green with a reviewed divergence list; the merge bugs appear as `BUG-FIXED` with
  tests that fail against `load()`; `config-shapes` passes on the current binary (it is the
  baseline the cutover is measured against).
- Lane: Thinkpad Docker lane for cases 1 and 4; Mac native for 2 and 3.

### Slice 4: loader cutover

- `load()` becomes: `yamlconfig.Resolve(ctx, os.DirFS("/"), rootRel, ResolveOptions{Include:
  {Transformer: age decrypt via the keyring (today `readConfigBytes`, `secrets.go:76`), Verifier:
  scope-file hash check when `verifyScopes` (nil for `LoadLenient`, `LoadForLock`)}, Policy: I,
  Defaults: ductile's scalar defaults as `DefaultSetting` overrides, Env: {InterpolateScalar: true,
  Lookup: E}})`, then decode, then the post-decode policy that stays in Go: the `database` alias,
  the API-block default (`loader.go:712-714`, now decided with `PresenceAt("/api")`), state path
  resolution, vault projection, validation, plugin defaults (`LoadRaw` still skips them).
- Delete `deepMergeConfig`, `interpolateEnv`, `loadIncludes`, `loadIncludeFile`,
  `loadConfigFile`, `loadEnvIncludes`, `checkUnresolvedEnvVars`, `TestDeepMerge*`,
  `TestInterpolateEnv`. Keep `Config.SourceFiles` populated (from `Files()`) for the writers and
  `StrictDecodeWarnings` until C lands; add `Config.Resolved yamlconfig.Resolved` (`yaml:"-"`).
- Typed errors for J: scope verification and decryption failures carry path and hint; `load`
  unwraps with `errors.As` and renders ductile's message.
- Tests (new, each must fail on the old loader): an include sets `api.enabled: false` over a root
  `true` → false; an include carrying `accounts:` is honoured and the boot gate sees it; an include
  carrying `service.unconfined: true` is honoured; `${MISSING}` in `service.name` → error naming
  file:line:column; a value with `: ` in an env var does not change structure; `plugins.x` in a
  later file replaces the whole entry; routes from three files are concatenated in include order;
  provenance for `/service/tick_interval` names the winning file and the superseded default; the
  parity harness shows zero `UNDECIDED` and the `BUG-FIXED` entries flip to agreement.
- Exit: `go test ./...` green; parity harness retired or kept as a regression fixture (decide);
  every docker fixture passes, `config-shapes` included; `.checksums` written by `config lock`
  unchanged for an unchanged tree.
- Lane: Thinkpad Docker lane, all fixtures. `privsep-artifact-ownership` and `config-shapes` case 1
  need `--cap-add SETUID --cap-add SETGID`; nothing needs `--privileged`.
- Docs in the same PR: CONFIG_REFERENCE §3.1 merge logic (presence, per-path rules, the dropped
  keys now honoured), §include directories (H), unresolved `${VAR}` is an error, CHANGELOG.
- Version: the merge fix changes what an existing split config means (an include that silently
  lost `accounts:` now applies it; an unresolved placeholder now refuses to boot). Minor at least;
  the principal decides whether an admission-affecting change is a major.

### Slice 5: `config explain <pointer>`

- New case in `cmd/ductile/config.go` (the `switch` at `63-117`): `config explain /service/
  tick_interval` prints `Resolved.Explain(ptr)`; `--all` prints `FormatProvenanceReport()`.
  Loads through the `LoadRaw` path so defaults are reported as defaults.
- Tests: a three-file fixture plus one env var: a default winner, a root-file winner, an include
  winner, an env winner; each output names file:line:column and the superseded chain; a secret
  shows `[REDACTED]` in its chain.
- Posture (absorbed from card 99): `config explain --posture` prints the boot gate's verdict
  computed from config alone (`evaluateBootGate`, `resolveAccount`): unconfined, enforcing or
  hybrid, and each plugin's resolved account. `config check` stops calling a config "valid"
  without saying whether the host will run unconfined.
- Exit: the command exists, is in the CLI skill template and OPERATOR_GUIDE, CHANGELOG entry.
- Lane: native.

Cut from this card (2026-11-01 trim): moving `/config/view` onto `Resolved.Sanitize` (today's
redaction works) and schema validation through `ashlar/schema`. Either can be its own card later.

## Test lane: the Thinkpad Docker lane, and what it proves

Verified on 2026-11-01 over `ssh matt@thinkpad`: Docker 29.9.2, `go1.22.2` (too old for `go 1.25`;
build inside `golang:1.26-alpine`, the repo `Dockerfile`'s builder, or cross-compile), 8 cores.
The same host runs the live gateway: system unit `ductile.service` (`Ductile Gateway (privsep)`),
user `ductile` (uid 984), `/usr/local/bin/ductile system start --config /etc/ductile/config.yaml`,
journald sink; accounts `ductile-default` 1001 and `ductile-untrusted` 1002; `matt` (1000) is in
group `docker`, which is the root side-door the boot audit warns about. `ductile-webui` (vite on
`:5173`) and `ductile-discord` run as `systemctl --user` units under `matt`. Host listeners include
`*:8081`, `8082`, `8090`, `8091`, `8095`, `8893`; the fixtures' `18081-18681` do not collide.

Rules for the lane, written into `docs/TESTING.md` with this card:

- Never touch `/etc/ductile`, `/var/lib/ductile`, `/opt/ductile`, `/usr/local/bin/ductile`,
  `~/.config/ductile*`, `systemctl` (system or user), or the `ductile` uid. The lane never runs the
  host binary.
- Run as `matt` through Docker only; bind-mount one fresh clone under `/home/matt`; no other host
  path mounts. No `--privileged`; `--cap-add SETUID --cap-add SETGID` only for
  `privsep-artifact-ownership` and `config-shapes` case 1. No `-p` publishes: fixtures talk to
  themselves inside the container. Artefacts land in the clone's `test-artifacts/`.
- `matt`'s docker-group membership is already root-equivalent on this host; the lane does not widen
  that, but a stray `-v /:/host` would. The clone-only mount rule is the control.
- The Dell (`dell-test-target` skill; card 148 verified slice 3 there) is the alternative lane with
  no live gateway and no trusted-tier side-door; prefer it for the privileged fixtures when it is
  awake.

`scripts/test-docker` → `scripts/test-docker-runner` builds the binary and runs each fixture's
`run.sh` natively (`test/fixtures/docker/README.md`: "no Docker required"; runner line 24). So "the
Docker lane" here means running that harness inside a container, which is what gives a Go 1.22
host a Go 1.25 build and gives the privileged fixtures a throwaway uid namespace.

What a fixture run proves that `go test` does not: a real `system start` performs boot admission
against files with real ownership (readability → integrity → validation → strict decode → privsep
gate, `runtime.go:540-585, 744-806`); `config lock` writes a manifest the gateway then re-reads;
`/system/reload` swaps the runtime under load (`reload-lifecycle`); the `:ro` mount plus one-shot
lock is the Unraid shape; a cap-only drop to a second uid is the Thinkpad shape. The parity harness
compares structs; the fixtures compare behaviour.

Slices needing the lane: 3 (cases 1 and 4), 4 (all fixtures). Slices 1, 2, 5, 6, 7: native.

## Done

- `internal/config` reads through Ashlar; `deepMergeConfig` and `interpolateEnv` no longer exist.
- `go.mod` has one YAML module and requires `github.com/mattjoyce/ashlar`.
- The three posture shapes and the ro-mount flow are fixtures that pass.
- `config explain` exists and reports the resolved privsep posture.
- Merge gates (AGENTS.md §9) and `scripts/test-docker` pass; the cutover was run on the Thinkpad
  lane and the result recorded here.

## History

- 2026-11-01: carded. Found that the include merge drops `accounts`, `admission`, `unconfined`,
  `max_workers`, `secrets` and others outright, not only explicit zeros.

## Compatibility decision note — 2026-11-01

Recorded by @assistant under Matt's instruction to complete plan steps 1 and 2.
This note supersedes conflicting proposals above; it does not authorise a loader cutover.

| Item | Decision |
|---|---|
| A | Preserve both webhook shapes with a Ductile `UnmarshalJSON` adapter; test flat and wrapped endpoints, malformed input, and unknown fields. |
| B | Preserve inline command timeout maps. Upstream inline-map decoding is a cutover prerequisite; do not change operator YAML. |
| C | Preserve warn-only unknown keys and opt-in strict admission. Upstream must collect all unknown-field diagnostics while decoding known fields; syntax/type failures remain errors. Strict decoding remains the library default. |
| D | Preserve absolute and parent-relative includes using a filesystem rooted at `/`; test nested includes, root paths, and cycle detection. `expandIncludeTarget` joins relative targets before cleaning, and handles absolute targets separately. |
| E | Preserve process environment precedence, first-definition-wins dotenv order, age decryption, and export to plugin passthrough. Pin per-file ordering before choosing the adapter: collecting every dotenv before interpolation can incorrectly make a later file's variables visible to an earlier file. The two-pass sketch above is not sufficient proof of parity. |
| F | Retain `${VAR}` in include paths as a compatibility requirement until all three live include graphs have been audited. Do not silently remove it. If used, require an upstream include-path expansion hook rather than a second Ductile traversal implementation. |
| G | Accept source-located failure for unresolved variables and scalar-only interpolation as intentional correctness changes. Accept string-only durations, refusal of empty documents, and refusal of aliases only after live audit and documented migration; no claim that existing installations are compatible yet. Empty-but-set environment variables must remain distinct from unset variables (see prerequisite below). |
| H | Preserve non-recursive, case-sensitive `.yaml` directory includes. Upstream must expose enumeration policy; recursive `.yml` discovery could activate unintended files. |
| I | Preserve replacement of whole `/plugins/*` entries and the listed append paths. Other maps merge and explicitly present scalars replace, including `false`, `0`, and empty strings. Test null and empty collection semantics explicitly rather than infer them from Go zero values. |
| J | Keep hook-error withholding in Ashlar; use typed Ductile errors and render safe path/hint context after `errors.As`. Never render arbitrary decrypted content or hook text. |
| K | Determine multi-file mode from resolved files; retain source nodes for config writers until a separately tested replacement exists. |
| L | Preserve existing on-disk checksum and snapshot hash contracts. Never substitute plaintext `Resolved.SourceHash`. |
| M | Correct upstream SPEC examples for root-package `ParseByteSize` and `SourceHash() string`. |

Scope: loader correctness ends this card. `config explain` (slice 5 above), posture presentation,
and schema/provenance UI are deferred follow-ups; they are not cutover acceptance criteria.
Existing card 99 remains the posture follow-up. No new CLI is needed to fix composition.

### Upstream prerequisite verification

- GitHub's latest tag and remote `master` are both `v0.1.0`, commit
  `5c05954ac5d73e29df84a6502b04b93502fa2f20` (checked 2026-11-01).
- The local Ashlar checkout is clean at `2ccd11ce93a8a0ecc6dbcefe3c55c7ece4fba107`;
  it must not be mistaken for the tagged dependency. The local decoder still has both B and C gaps.
- B: `indexStructFieldsInto` only flattens structs; an inline map falls through to an ordinary
  field. Required tests: fixed duration fields plus arbitrary duration overrides, nested inline
  structs, named string map keys, null/empty maps, invalid override types with location,
  conflicting/unsupported inline declarations rejected without panic.
- C: `decodeStruct` returns on the first unknown key. Required contract: opt-in collected
  diagnostics with pointer and file:line:column, all known fields decoded including nested
  maps/slices, deterministic ordering, no value leakage, unchanged strict entry point, and no
  usable partial result on a real type/syntax error. Custom unmarshaler boundaries must be explicit.
- H is an additional prerequisite: configurable directory enumeration, retaining today's library
  default for other consumers. Tests must distinguish nested files, `.yml`, uppercase extensions,
  sort order, explicit file includes, and traversal failures.
- Empty environment values are an additional compatibility gap: `interpolateSingleString`
  currently rejects `!ok || envVal == ""`; Ductile's text substitution accepts set-empty values.
  Upstream needs an explicit policy distinguishing unset from empty, with secure strict defaults
  retained. Test unset, set-empty, ordinary values, secret redaction, and provenance.
- Before cutover: audit actual Mac, ThinkPad, and Unraid include graphs (including encrypted
  fragments through authorised readers) for F/G/H and dotenv ordering. This audit is pending;
  the earlier logging baselines are not evidence of config syntax compatibility. Report only
  features and locations, never secret values. No live edits, locks, reloads, or restarts.
- Upstream implementation/release and this live audit remain gates. No dependency bump is ready.

## Narrative

- 2026-11-01: Reviewed the Ashlar integration plan before implementation. Kept documented include
  discovery and environment precedence as compatibility contracts, identified additional directory
  enumeration and empty-environment gaps, and deferred CLI explanation work to keep the cutover
  focused. The original two-pass dotenv sketch needs ordering tests. No runtime code or live
  configuration changed. (by @assistant)
- 2026-11-02: On hold for Ashlar. PR #3 (inline map catch-all) covers gap B; a consumer check is
  posted on the PR. Ashlar expects to accommodate the other three asks: collect every unknown key
  with its location, a configurable directory-include policy, and telling empty env values apart
  from unset ones. None is used by a live host today (Mac, Thinkpad and Unraid checked), so they are
  compatibility, not blockers. Found on the way: the Mac has four enabled plugins whose `${VAR}` is
  unset under launchd (email_pipeline_veto, discord_notify, jina reader, firecrawl), so today they
  receive the literal placeholder. Ductile keeps fail-closed on unset variables after cutover;
  fix the Mac (vault or plist env) before it. (by @assistant)
- 2026-11-02: Ashlar v0.4.0 ships the inline map catch-all (PR #3), closing gap B. Slices 1-3
  (one YAML module, helper swap, parity harness) are unblocked; pin `ashlar v0.4.0`. Slice 4 (cutover)
  stays gated by the decision note's C, H and empty-env prerequisites, now Ashlar backlog cards, unless
  Matt waives one. Live-audit progress: no host uses directory includes or `${VAR}` in include paths,
  and no host logged an unknown key. The Mac has four enabled plugins whose `${VAR}` is unset under
  launchd, so today they receive the literal placeholder. Dotenv ordering is not yet audited.
  (by @assistant)
