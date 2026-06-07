---
id: 99
status: backlog
priority: Normal
blocked_by: []
tags: [privsep, dx, ai-operator, observability]
---

# `explain` verb — present the privsec & vault authority models as observable objects

> **Nav:** [[83-privsep-epic]] · follow-up surfaced by the Kay × Victor "AI as first-class user"
> review (2026-06-07). **Not a merge-blocker for the privsep PR** — a separate slice that exposes
> existing (mostly pure) state, adds no privilege/secret mechanism.

Two sibling verbs under one grammar — `ductile privsep explain --json` and `ductile vault explain
--json` — that **present** (not narrate) the salient values of an authority model so a reader (AI or
human) comprehends fast. The binary stays static/offline; comprehension lives in the consumer. The
verb never concludes and never emits secret values.

**Job story:** *When* an AI operator (or a person) configures or audits privsep, *I want* to **see**
the boot gate's verdict — posture, per-plugin resolved account, tier presence, and the delta a
proposed grant would make — as a structured object **before** restarting the daemon, *so* I can
reason about the wall instead of inferring it from boot logs and failed jobs.

## The gap (from the review)

The privsep model is a **pure function of config** the running system evaluates once at boot and
then **discards** — it keeps the behaviour and throws away the reasoning:
- `evaluateBootGate(capabilityHeld, accountsConfigured, override bool)` — pure, 3 booleans (`internal/dispatch/bootgate.go:38`).
- `resolveAccount(cfg, plugin)` / `bindAccountToFingerprint(...)` — pure over config alone (`account.go:71`, `fingerprint.go`).
- `hasDropCapability()` — a cheap local probe (`process_unix.go:18`).

So the verdict is computable instantly, unprivileged, no daemon, no decrypt — but today it is only
**logged at boot** (`cmd/ductile/runtime.go:631-668`) and never exposed. Consequences:
- `config check` reports a config **valid** without reporting that the host will run **unconfined**
  (valid ≠ enforcing — the live-Mac T7 situation a human had to reason out).
- No dry-run / what-if before a restart; the feedback loop is edit → restart → grep journal → infer.
- `BootMode.String()` ("enforce"/"unconfined") and the resolved-account table exist in code but reach
  no `--json`, no `/system/doctor`, no `/healthz`.

This latency is **accidental, not essential**: reasoning about the wall needs only config; only
*trusting the wall is physically real* (setuid success, fs reconcile, live decrypted state) needs
the kernel/daemon. Separate them.

## Acceptance (sketch — refine when picked up)
- A read-only **`ductile privsep explain [--json]`**: static, runs as the caller, no daemon required.
  Renders posture + truth-table cell, per-plugin resolved account (name/uid/confined/downgrade
  target), tier presence (`default`/`untrusted`), secrets-surface status (or "boot-only").
- A **what-if**: `--grant <plugin>=<tier>` shows the delta without writing config or restarting.
- Posture plumbed to the channels an AI already reads: a `privsep` block in `/system/doctor` and a
  `privsep_mode` field in `/healthz`.
- `config check` warns when a config is valid **but** would run unconfined on the current host.
- No new privilege mechanism — pure exposure of the existing gate/resolve functions; the wall's
  physical proof (setuid, fs reconcile) stays where it is (boot, fail-closed).

### `vault explain --json` (sibling verb)

Same shape on the vault authority model — but with a crucial difference: privsep's authority lives in
**plaintext config** (so its rich tier is static/unprivileged), whereas the vault's authority lives
**inside the age-encrypted blob** (`Secret.AuthorizedPrincipals`, status, pattern — `internal/vault/secret.go`).
So `vault explain` is primarily a **key-holding / daemon** verb; its no-key static tier is thin.

- **Live tier (needs the key/daemon):** the **composition matrix** — principal × secret →
  delivered / denied, reusing the existing denial taxonomy (`compose.go`: `DenialSecretRevoked`,
  `DenialPrincipalNotAuthorized`); per-secret status (active/revoked), auto-pattern, attestation
  (fingerprint nonce per principal); reserved entries (`core` / `core-admin-token`) shown as such.
- **Static tier (no key):** structural wiring only — is a vault configured, age key-file present +
  `0600`, which plugins map to principals, admission gates (`verify_integrity_on_boot` etc.).
- **Salience to surface:** orphaned grants (the `Store.Check` orphaned-grant pass, `secret.go:124`
  already computes this), a principal authorized for a *revoked* secret, a secret with **no**
  authorized principals (dead grant), reserved-entry integrity.
- **Hard rule:** present the authority *shape*, **never secret values** — same discipline as
  `system vault-audit` ("names + outcome only, never values"). `explain` shows *who-can-compose-what*,
  not *what*. Complements `vault-audit` (audit = what happened; explain = what the authority now *is*).
- *Accept:* `vault explain --json` renders the live composition matrix + denials + salience from the
  daemon (key-holding); the static tier degrades honestly to "wiring only, grants behind the key."

## Narrative
- 2026-06-07: Created from the Kay × Victor "AI as first-class user" design review of the privsep
  branch. Kay: the privsep surface recreates the sysadmin paper trail (edit file → restart → read
  journal) instead of being a medium the operator can think in. Victor: the system has posture state
  it never shows, forces the operator to imagine consequences, and runs its feedback loop slower than
  thought — yet the gate is a pure function, so the "explain" affordance is exposure, not new
  mechanism. Logged as a separate slice so it does not block the privsep PR. (by @assistant)
- 2026-06-07: Broadened to the `explain` *verb family* and added `vault explain --json` at the
  operator's request. Key design fact: privsep authority is plaintext config (rich static tier),
  vault authority is inside the encrypted blob (thin static tier; explain is a key-holding/daemon
  verb). Both reuse existing near-pure projections (gate/resolve for privsep; compose denial taxonomy
  + Store.Check for vault). Hard rule for vault: present authority shape, never secret values. (by @assistant)
