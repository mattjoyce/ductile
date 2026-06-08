---
id: 83
status: backlog
priority: High
tags: [privsep, epic]
---

# Privsep — uid separation (worker users) (EPIC)

> [!status] WHERE WE'RE UP TO  *(a resuming agent reads this first; keep it current)*
> - **🎯 ENFORCE PROVEN LIVE on the Thinkpad (2026-06-07):** first live enforced deployment. Stood up a
>   fresh system service (`/etc/ductile` + `/opt/ductile` code + accounts map) running as `ductile`(984)
>   with `CAP_SETUID/SETGID`; boot logged "privsep enforcing". Triggered `sys_exec`(command=`id`) →
>   stdout `uid=1002(ductile-untrusted) groups=1002` (the drop is real, supplementary groups reset clean);
>   `sudo -u ductile-default ls .../untrusted` → Permission denied (cross-account FS isolation). So Layer 1b
>   is proven on a LIVE Linux host, not just the Dell test suite. **Done via deploy-as-new** (not migration):
>   the live config was too home-entangled to re-home (plugin code + 12 secret .env + config all under
>   /home/matt; admin plugins docker/apt are unconfinable). Phase 1 = exemplar plugins only; **real
>   automation is OFFLINE pending Phase 2** ([[103-privsep-thinkpad-phase2-restore-plugins]]). Runbook:
>   `docs/runbooks/privsep-thinkpad-enforce.md`. Old `--user` gateway disabled but revertible from backup.
> - **🟢 PHASE 2 (achievable scope) DONE + HARDENED (2026-06-07):** the enforced gateway is the LIVE prod
>   data plane on :8081. Vault carried (age key → /etc/ductile/secret 0600; vault.age → /var/lib via
>   `secrets.vault_file`); **5 keyless integrations enforced + attested on default(1001)** — jina-reader,
>   check_youtube, youtube_transcript, youtube_playlist, check_db_garmin (verified: garmin poll, playlist
>   emit, live `1001 ductile-default …run.py`). `config lock` + `plugin lock --all` done; **admission
>   gates back ON** → boots clean. All wall-bites pass (age key, vault.age, cross-account state, /opt code
>   → Permission denied to accounts). Live proof of the **#93 downgrade**: relocated-but-unattested code
>   failed SAFE to untrusted until `plugin lock`. **Remaining (carded; need dev / separate sessions, NOT
>   config):** [[107-plugins-vault-native-conformance]] (secret-holders — empirically HEAVY: `_ref` is NOT
>   plugin config-substitution; delivery is stdin-secrets-map only + kebab principal + code change),
>   [[106-ductile-admin-glue-unconfined-instance]] (docker/apt/fabric/file_handler), [[108-vault-compose-fail-open-unknown-principal]],
>   [[105-v1.0-fhs-install-artifact]] (lesson: `config lock`→`plugin lock` BEFORE plugins land on intended
>   account). Old `--user` decommission DEFERRED until #106+#107 land.
> - **🟢 PHASE 3 — vault-native plugins + pipeline model PROVEN (2026-06-07):** #107 first secret-holders
>   live + fail-closed: `discord_notify` + `ap_canary` read secrets from the encrypted vault over stdin
>   (B form: snake instance + `vault_principal: <kebab>` + `requires_vault`), drop to uid 1001, attested.
>   **Fail-closed proven live** (bogus principal → spawn refused). Plugin code @ ductile-plugins `a1934e5`.
>   **Full notify model proven end-to-end under enforce** — on-EVENT (ap_canary→agent_handshake.registered
>   →pipeline→ap_canary_notify→real Discord post) AND on-HOOK (job.failed→pipeline→job_failure_notify→post).
>   In-binary hardening also landed + redeployed: #100 (ResolvedAccount.Validate, no root-drop),
>   #101 (valid≠enforcing warn), #104 (backup-secret-zero test), #108 (requires_vault). Repo @ `055aa88`.
>   **New cards from the push:** [[109-uv-shebang-plugins-under-privsep]] (git/uv plugins exit-127 under
>   enforce), [[110-pipeline-webhook-migration-enforced]] (per-chain notify migration + the 2 findings:
>   notify_on_complete gating; per-route vault_principal'd instance). **Remaining:** #107 (more
>   secret-holders + fabric-last), #106 (admin instance), #109, #110, decommission old `--user`.
> - **State:** ADR accepted; **all build/decision cards DONE — #92, #84, #85, #86, #87, #88(Linux),
>   #93, #90, #89.** The privsep mechanism is complete, proven on macOS (units) + privileged Linux
>   (Dell: wall, cap-only drop under exactly the two caps, fs reconciliation, negative suite). CI gates
>   the wall under sudo. Docker/Unraid decided hygiene-only.
> - **Rename + rebase (2026-06-07):** branch **renamed worker→account / run_as** (commit `5590091`) to
>   decouple from concurrency `workers`/vault `principal`; **rebased onto main** (gosec fix included).
>   Full repo suite green, gofmt-clean.
> - **Luminary code review DONE (2026-06-07):** 4 panels (Brooks×Beck, Hickey×Armstrong,
>   Ousterhout×Liskov, Thompson×Feathers) — **unanimous approve, ZERO merge-blockers.** Synthesis +
>   triage: `~/Obsidian/Personal1/ductile/privsep/PrivSep-Branch-Review_SYNTHESIS.md` (17 themes).
> - **Tier A+B review changes FOLDED (2026-06-07):** boot-time grant-resolution (`validateAccountGrants`,
>   the 4/4 headline) + `default`/`untrusted` tier-absence boot warns; named `SecretSurfacePaths`
>   reconciling the config *directory* (closes file-form sibling-secret gap); `ErrNoDowngradeTarget`
>   split from `ErrAccountDropFailed` (both terminal/no-retry); #96 vocab residue cleaned from CODE
>   (log keys/comments/`a account` strings, `workersConfigured`→`accountsConfigured`). Suite green,
>   gofmt/golangci-lint/gosec clean; independent review confirmed **no fail-open regression**.
>   **Deferred → [[97-privsep-review-followups]]:** T3 (ResolvedAccount sum-type — naive `Confined()`
>   is a zero-value footgun), T5 (once-per-spawn attestation — risky refactor, minor), T9, T15, vocab lint.
> - **T7 finding (#93 activeness):** live gateway (`~/.config/ductile`) loads a vault (age key + `vault.age`;
>   logs "compose-time attestation on") → the **secret-path swap-defence is ACTIVE**. BUT there is **no
>   `accounts:` map** → boot gate → `unconfined` (no CAP_SETUID on macOS launchd) → privsep is **NOT
>   enforcing on the live host**, so the #93 *downgrade* path isn't reached there. Enforce mode is
>   macOS-pending ([[95-privsep-launchd-and-live-rollout]]) — must be stated in the PR body.
> - **Docs pass DONE (2026-06-07):** [[98-privsep-docs-naur-diataxis]] folded all Tier D doc items.
>   ADR T5c (downgrade-vs-secret-denial asymmetry, §4) + T17 (point-in-time-at-boot tradeoff, §8);
>   DEPLOYMENT.md §5b T11 (uid-coupling SSOT) / T12 (Linux-proven, macOS-pending → #95) / T13 (root-refusal
>   dev note) + `account:`→`run_as:` drift fix; new §5c reference (keys, reserved keywords T14, boot-gate
>   table, failure modes); schema drift fixed (`service.unconfined` was missing from `ServiceConfig`,
>   strict-validate would have rejected the documented escape hatch). No code change; suites green.
> - **Next action:** **open the PR.** Body must state enforce is **Linux-proven, macOS-pending #95**, the
>   live host currently **unconfined** (T7). Optionally `/code-review ultra` first.
> - **v1.0 line (2026-06-07 Kay×Victor triage → [[102-v1.0-readiness-privsep-ship-line]]):** v1.0 = the
>   security property is true/correct/documented, not self-describing. **NOW:** this PR + T3
>   [[100-privsep-resolvedaccount-sumtype-t3]] + anti-footgun [[101-privsep-valid-not-enforcing-antifootgun]]
>   + the macOS-prod-host decision (#95 in-scope or ship-with-caveat). **LATER (v1.x):** the `explain`
>   verb family [[99-privsep-explain-observable-posture]] + rest of #97.
> - **Open:** [[102-v1.0-readiness-privsep-ship-line]] (todo), [[100-privsep-resolvedaccount-sumtype-t3]]
>   (todo), [[101-privsep-valid-not-enforcing-antifootgun]] (todo), [[97-privsep-review-followups]] (todo),
>   [[99-privsep-explain-observable-posture]] (backlog), [[95-privsep-launchd-and-live-rollout]] (backlog).
> - **Done:** #92, #84, #85, #86, #87, #88, #89, #90, #93, #96, #98 — **Branch:** `feat/privsep-uid-separation` (pushed, rebased on main).
> - **PR:** not opened yet — docs now match the code; ready to open (state macOS-pending enforce per T7/T12).
> - **Update rule:** when a card's `status:` changes, update this block + the table below.

Make the secret-scoping wall **real**. Today plugins run as the gateway's own OS user
(`internal/dispatch/process_unix.go:12` — `configurePluginProcess` only sets `Setpgid`),
so scoping is an honour system: a popped first-party plugin can read the age key, the
config, the state DB, or attach to a sibling's memory (same-uid `ptrace`). This epic
delivers **PrivSec ADR Layer 1b**: a privileged gateway drops each plugin to an unprivileged
**worker** user at spawn, and filesystem permissions then bite.

**Already shipped (do NOT re-card):**
- **Layer 1a — spawn hygiene:** env allowlist + `service.plugin_env_passthrough`
  (`internal/dispatch/env.go`); secrets over stdin, never env/argv.
- **Layer 2 — scoped secrets:** the Vault epic ([[01-vault-epic]], #1–#47); keyed-nonce
  attestation ([[12-rung4-attestation-upgrade]]).
- **age at rest** for config/secrets.

The value gap is purely **runtime confinement of a popped plugin** — which exists only with a
privileged (`CAP_SETUID`) gateway dropping privilege at spawn.

**Build ladder — tracer first, then generalize one layer at a time:**

| # | card | status | depends on |
|---|------|--------|-----------|
| 92 | [[92-privsep-tracer-wall-off-sys-exec]] — wall off `sys_exec` (1 worker/1 plugin/1 host/1 test) | **done** | — |
| 84 | [[84-privsep-workers-table]] — two worker tiers in config (table deferred) | **done** | 92 |
| 85 | [[85-privsep-per-plugin-worker-grant]] — per-plugin `worker:` grant (*which-worker*) | **done** | 84 |
| 93 | [[93-privsep-fingerprint-bind-grant]] — bind grant to fingerprint (swap defence) | **done** | 85 |
| 86 | [[86-privsep-spawn-uid-drop]] — uid/gid drop, fail-closed + group-reset | **done** | 84, 85 |
| 87 | [[87-privsep-filesystem-permissions]] — full secrets surface + per-worker dirs | **done** | 86 |
| 88 | [[88-privsep-deploy-systemd-launchd]] — deploy host 1 (caps, never setuid) | **done** (Linux) | 86 |
| 95 | [[95-privsep-launchd-and-live-rollout]] — launchd (Mac) + live rollout (observe-then-next) | backlog | 88 |
| 90 | [[90-privsep-negative-test-suite]] — CI gate aggregating the negative tests | **done** | 86, 87 |
| 89 | [[89-privsep-deploy-docker-unraid]] — Docker/Unraid LAST, maybe hygiene-only | **done** (hygiene-only) | 88 |

**Operator decisions (2026-06-06):**
- **Scope:** full Layer 1b — adopt the `CAP_SETUID` gateway (ADR §10 Q1 = yes).
- **Default stance (Q2):** no grant → shared trusted **`default`** worker (ADR §5).
- **Sequencing (Q5):** card now, **grill the ADR in parallel** — cards are `backlog`; open ADR
  questions are flagged on the cards they touch.

**Brooks × Beck review folded in (2026-06-06):**
- **Tracer added ([[92-privsep-tracer-wall-off-sys-exec]]):** the original ladder was
  all-or-nothing (~5 coupled cards before any observable security). The tracer is the smallest
  end-to-end slice — wall off the riskiest plugin on one host, *learn*, then generalize.
- **[[84-privsep-workers-table]] de-scoped:** two fixed tiers, not a configurable table.
- **[[93-privsep-fingerprint-bind-grant]] split from [[85-privsep-per-plugin-worker-grant]]:**
  *which-worker* and *is-the-binary-unchanged* are separate decisions.
- **Old #91 folded into [[86-privsep-spawn-uid-drop]]:** a correct, fail-closed drop is the unit.
- **[[90-privsep-negative-test-suite]] re-sliced:** tests ship with their mechanism; #90 aggregates.
- **[[88-privsep-deploy-systemd-launchd]]/[[89-privsep-deploy-docker-unraid]] sequenced:** one
  host first → observe → next; Docker/Unraid last, possibly hygiene-only.

**Known residual — shared `default` worker (Hickey×Armstrong review + ADR grill, 2026-06-06; now ADR §2):**
- The Q2 default puts every ungranted first-party plugin on **one shared `default` uid**. Same-uid
  means siblings on `default` can `ptrace`/read each other's memory — the same-uid attack this epic
  kills, narrowed from *vs-gateway* to *vs-sibling*, not eliminated. This bites the **primary** threat:
  a popped `fetch` (network-facing) shares a uid with `withings` (holds a token). **Accepted** for the
  homelab floor; the **mitigation is config-only** — move a secret-holder onto its own worker via the
  per-plugin `worker:` grant (#85), since the `workers` map is open (#84). The #84 sizing rule —
  "would you accept one reading the other's memory?" — is the trigger.
- **Stated, not hidden:** [[90-privsep-negative-test-suite]]'s cross-uid `ptrace` test must run **two
  plugins on *different* workers** (a `default`/`default` pair shares a uid and passes trivially), and
  must note that sibling isolation *within* `default` is explicitly not claimed.

**Renamed `worker` → `account` / `run_as` (2026-06-07, commit `5590091`; ADR vocab synced #96):**
- Everywhere this epic and its cards say *worker*, the shipped code/config/ADR now say **account**:
  config map `workers:`→`accounts:`, per-plugin grant `worker:`→`run_as:`, Go `WorkerConf`→`AccountConf` etc.
- Renamed to stop colliding with dispatch **concurrency** *workers* (`service.max_workers`, the pool) and
  the vault ***principal*** — both distinct concepts, **unchanged**.
- Historical card prose below and the card titles (e.g. [[84-privsep-workers-table]]) are left
  as-written for the record; read *worker*→*account*, *`worker:`*→*`run_as:`*, *`workers` table*→*`accounts` table*.

**Vocabulary + boot gate — `default` ≠ `unconfined` (ADR §5, resolved 2026-06-06):**
- **`default`** = a *configured, unprivileged* worker uid a plugin is dropped to (a real wall) — the
  fallback *tier* for an ungranted plugin when a `workers` table exists.
- **`unconfined`** = the *named no-drop state*: spawn at **gateway uid** (today's behaviour). Never a
  synthesised worker, never the name `default`. A reader of the config must tell the wall from its absence.
- **Boot gate (capability × workers-configured must AGREE):** capability+workers → drop; capability+no
  workers → **refuse**; no capability+workers → **refuse**; no capability+no workers → `unconfined`
  (quiet, dev). The one override is explicit, loud **`service.unconfined: true`**. No silent auto-degrade.
  Enforced by #86; the empty-map fork lives on #84.

**Named-deferred (ADR §6 / §11 — not in this epic):**
- §11 schema `//go:embed` as a runtime control (only if config ever *enforces* a security
  invariant; §11 contradiction partially settled by [[08-arch-daemon-sole-writer-api]]).
- many-worker *ergonomics/validation* (the map itself is open — arity is not deferred, only the
  naming rules / sizing helpers); namespace/container sandbox; per-plugin uid proliferation;
  ephemeral/dynamic users; egress credential broker.

## Narrative
- **Authoritative design:** `~/Obsidian/Personal1/ductile/Ductile - PrivSec and Secrets.md`
  (ADR, **status: Proposed** — grilled in parallel; §3 layers, §4 authority, §5 worker model,
  §9 acceptance, §10 open questions).
- **The fork the epic turns on (ADR §3):** uid separation requires a *privileged* gateway —
  no privilege → hygiene-only, the `600` perms protect nothing from a same-uid plugin. Accepted
  cost: a gateway holding `CAP_SETUID`+`CAP_SETGID` (not full root).
- Constraints: solo/homelab → simplest correct floor, YAGNI. Go. TDD encouraged.
  Commit `<component>: <action> <what>`; never attribute AI.
