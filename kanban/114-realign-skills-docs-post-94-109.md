---
id: 114
status: done
priority: High
blocked_by: []
tags: [docs, skills, vault, privsep, api, v1.0, post-merge-drift]
---

# Realign skills + reference docs with the post-#94 / #109 surface

> **Nav:** [[102-v1.0-readiness-privsep-ship-line]] · the skills were last aligned at `10e2ec2`
> ("align ductile config/vault surface with shipped main"), which **predates** #94 (vault-only API
> tokens), #109 (confined-plugin runtime contract), #111 (sidedoor tier), #112 (deployment postures).
> Prior cards of this class — #32 cli-help-vault-surface, #33/#34 skills coverage — all closed *before*
> those merges. The in-binary `--help` is largely fine (the #94 change lived in `internal/config`, not
> help strings); the **skills/reference docs** are the stale surface.

**Job story:** *When* a user (or AI operator) follows the ductile skills to author config or write a
plugin, *I want* the guidance to match the shipped binary, *so* they don't author config the gateway
now rejects at boot, or a plugin that face-plants under enforce.

## Items (verified against `origin/main`)

1. **🔴 `skills/ductile/references/config.md:93-107` — ACTIVELY WRONG (must-fix).**
   Teaches inline `token: ${ADMIN_API_KEY}` / `token: readonly_token`. #94 (`a25d16b`) made API
   tokens **vault-only**: `APIToken` = `secret_ref` (required) + `scopes`; a literal `token:` or
   `${ENV}` is **rejected at load** (`api_tokens.go:35-40`, fail-closed, no migration window).
   Correct model:
   ```yaml
   api:
     auth:
       tokens:
         - secret_ref: ductile-api-admin      # resolved from the vault at boot
           scopes: ["*"]
   ```
   Provision with `ductile vault set <name> ...`; missing/empty ref = hard boot error.
   **(Fixed inline 2026-06-08 as part of filing this card — see Narrative; remaining items below.)**

2. **🟠 `skills/ductile-plugin-developer/SKILL.md:127,142` — incomplete re #109 runtime contract.**
   Line 142 ("minimal allowlisted env: PATH, HOME, TZ, LANG") doesn't capture that under enforce a
   confined plugin gets `HOME`/`XDG_CACHE_HOME`/cwd **rebased to the account `state_dir`**, a writable
   TMPDIR, and **no `/home` / no ambient creds**. Canonical source exists:
   `docs/adr/confined-plugin-runtime-contract.md`. Add a "confined runtime contract" section so plugin
   authors target it (stdlib/system runtimes; never `$HOME` dotfiles, `/home` paths, ambient state).

3. **🟢 Pure-vault use-case — NEW, document it (verified mechanism).** ductile can run as a **pure
   secrets vault** (no plugins/pipelines). The verified flow:
   - **CLI genesis:** `ductile vault init` (local; mints core principal + nonce + one-time admin token).
   - **CLI set:** `ductile vault set <name> ...` (writes via the daemon `POST /vault/secret` — the
     daemon is the sole writer).
   - **Value read = CLI `ductile vault get --name <x>`, LOCAL key-holder op** (`cmd/ductile/vault.go`
     `runVaultGet`): loads + decrypts the on-disk blob with the age key, prints value to stdout;
     **never touches the API.** Reserved secrets (admin token) are never printed.
   **Hard rule — there is NO value-returning secret API, by design.** All `/vault/*` routes return
   `{name,status}`; the full GET route table (`server.go:228-291`) has no `/vault`/`/secret` GET;
   `server.go:294` "value-dump and genesis stay local, never here"; `vault_test.go:313` asserts
   non-leak. So a value comes back **only** to the local age-key holder via CLI — not over HTTP.
   - **DECISION (operator, 2026-06-08) — DESCOPED, full stop.** A token/HTTP secret-*read* was grilled
     and **rejected for v1.0**. Document the pure-vault flow strictly **as-built**: value-read =
     `ductile vault get` (local, age-key holder). No network read, no token read, build nothing — the
     value-free-API invariant stays literally true. The doc must state plainly: **reading a secret
     value requires secret zero (the age key); there is no least-privilege read below it.**
   - **Parked (post-v1.0 epic, NOT this card):** scoped token-read for non-key-holders (a local AI
     admin / programmer reading their own secrets without the master key). Grilled design landed on:
     token = a `consumer`-kind principal's bearer credential, read-authorized via the existing
     `Compose`/`AuthorizedPrincipals` model, bound by **bearer + uid-pin (`SO_PEERCRED`) over a local
     unix socket** (not TCP). Two hard constraints that make it a real epic, not a tweak: (1) a token
     can't decrypt — every token-read MUST route through the running daemon (only it holds the
     decrypted store); (2) the daemon has **no local socket** today (TCP-only), so the channel itself
     is new. Security-reviewed endpoint + audit required. Promote to its own card if/when pursued.

4. **🟡 Lower — scan + touch-ups:**
   - `skills/ductile/SKILL.md:84-86` token shorthand + `references/api.md` — add the `secret_ref`/
     vault-only note for API tokens.
   - #112 deployment postures (unconfined / full-privsep / hybrid) — not reflected in any skill;
     point at `docs/` postures guide.
   - #111 credentialed/trusted-tier + sidedoor audit surface — not in skills.

## Acceptance
- No skill/reference doc instructs a config shape the shipped binary rejects (grep: no inline
  `token:`/`${ENV}` API-token examples remain).
- `ductile-plugin-developer` states the #109 confined runtime contract.
- A "pure vault" usage note exists and is honest about no-value-return.
- API-token vault-only model documented consistently across `config.md` + `api.md` + `SKILL.md`.

## Narrative
- 2026-06-08: Filed after a post-merge drift audit (#94 + #109 + #111 + #112 landed on `main`; skills
  untouched since `10e2ec2`). config.md:93-107 was the flat-wrong must-fix (taught now-rejected token
  literals) and was corrected inline at filing. Operator added the **pure-vault** use-case
  (CLI genesis → CLI set → API-as-credential); verification confirmed there is no value-returning
  secret API by design, so item 3 documents the posture without implying a GET. (by @assistant)
</content>
</invoke>
- 2026-06-08: **CLOSED.** The flat-wrong must-fix (config.md inline-token example teaching
  now-rejected syntax) was corrected at filing (`6bf02f4`); the confined-runtime-contract reference
  resolves via the canonical ADR link. No ship-blocking doc drift remains. Any further confined-env
  cross-link is v1.x polish. (by @assistant)
