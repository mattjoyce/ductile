---
id: 112
status: done
priority: High
blocked_by: []
tags: [docs, privsep, deployment, onboarding]
---

# Document the three ductile deployment postures (a deploy-mode guide)

> **Nav:** [[111-root-gateway-halfway-tier-nopasswd-check]] · [[83-privsep-epic]] · ADRs
> `confined-plugin-runtime-contract`, `credentialed-runtime-contract`, `filesystem-layout`. Asked by
> operator 2026-06-08 after the trust-tier work landed — the choices now exist in code + ADRs + war-room
> files, but an operator has no single doc that says "pick one of three, here's how."

**Job story:** *When* I stand up (or re-deploy) ductile, *I want* one doc that lays out the three
deployment postures — what each protects, when to pick it, and how to set it up — *so* I choose
deliberately instead of reverse-engineering it from five ADRs and a kanban card.

## The three postures (the content to write)
Increasing in what plugins are permitted; ALL are now real + proven this session.

### 1. Unconfined (hygiene-only / ADR Layer 1a)
- **Gateway:** runs as a service user (or root); **plugins run AS the gateway — no uid drop.**
- **Secret zero:** NOT protected from plugins (a plugin can read the age key / ptrace the gateway).
- **Isolation:** none between plugins or from the gateway's authority.
- **Use when:** fully-trusted single-user / dev, OR the **#106 admin-glue instance** (deliberately
  unconfined for host-entangled tasks — docker, etc.; paired with an enforced data-plane).
- **Setup:** no accounts map / no enforce. Simplest. The boot gate decides if unconfined is permitted.

### 2. Full privsep (FHS enforce / Layer 1b, all-confined)
- **Gateway:** **cap-only** — `CAP_SETUID`+`CAP_SETGID`, a non-root system user (e.g. `ductile` 984). NO root.
- **Plugins:** ALL drop to dedicated confined accounts (`default` 1001 / `untrusted` 1002), **walled** to
  a private 0700 state_dir (the #109 C contract: HOME/cache/cwd = state_dir, 0711 traversal floor).
- **Code:** `/opt/ductile/plugins` (root-owned, world r-x, attested/locked). Secrets via the **vault**.
- **Secret zero:** protected (gateway-owned 0600 key; confined accounts can't read it or ptrace).
- **Use when:** multi-source / untrusted plugins / max isolation / production. Requires deploy-as-new (FHS).

### 3. Hybrid trust-tier (cap-only + `ductile`-group, ONE gateway)
- **Gateway:** same cap-only (NO root). One instance / one event bus (native pipelines).
- **Plugins:** confined-by-default (walled + vault, as #2) PLUS a **credentialed `trusted` tier** that
  drops to the operator's real uid and runs with their REAL home — reaches `~/.ssh`/`~/.config/gh` to act
  as the operator (git push). groups-minimal (gets the operator's FILES, not docker; docker opt-in).
- **Code:** trusted plugins live in `~/ductile/plugins`, read by the gateway via a **`ductile`-group ACL**
  (`g:ductile:x` on the home, `g:ductile:rX` on the dir) — code, not creds; no /home opening.
- **Secret zero:** still protected (gateway-owned key). A trusted plugin runs as the operator, who may be
  root-equivalent via docker — covered by the tier-aware **warn-loud root-sidedoor audit** (#111).
- **Use when:** homelab where a few vouched-for plugins must act as you, but you don't want a second
  instance or root. The recommended homelab shape. `run_as: matt` for now; per-plugin tier review later.

### Rejected (document as the road not taken)
- **Gateway as root** — proven unnecessary: cap-only's `CAP_SETUID` already drops to any uid incl. the
  operator, and the `ductile`-group ACL gives code-read without root. Root only re-enlarges the
  network-facing daemon's blast radius. Dropped this session.

## Cross-cutting (put in the guide)
- The **secret-zero invariant**: privsep exists so no PLUGIN can read the age key (disk or gateway
  memory). Vault protects against plugins, never the operator (who is root-equivalent by design).
- **Tier vocabulary**: unconfined | confined | credentialed (the `AccountMode` enum). "Needs a secret" ≠
  "trusted" — a RO plugin with a vault token stays confined (github_repo_sync is the example).
- **No root anywhere** except one-time install (every posture).

## TODO
- [x] Write the guide — landed as `docs/DEPLOYMENT_POSTURES.md` (not DEPLOYMENT_MODES.md; "posture" is
      the dominant term + avoids clashing with the AccountMode enum). Standalone file, NOT an extension
      of DEPLOYMENT.md: #98 already put how-to (§5b) + reference (§5c) there, so the missing register is
      explanation/chooser. Three postures as a chooser (when-to-use / what-it-protects / setup-pointer /
      tradeoffs) + the rejected-root note.
- [x] Decision table done ("Choose in one glance", 9 axes incl. a Platform row). The architecture
      DIAGRAM stays operator-owned mermaid per [[diagrams-are-user-owned]] — left an explicit placeholder
      note in the doc, did NOT author ASCII.
- [x] Linked the 3 ADRs (confined-runtime, credentialed-runtime, filesystem-layout) + both runbooks
      (privsep-thinkpad-enforce, ductile-admin-instance), all by verified relative path.
- [x] Cross-linked the plugin-tier guidance (PLUGIN_DEVELOPMENT §10.6 + `_template`); postures-vs-tiers
      called out as orthogonal axes.
- [x] Confirmed "three" with the operator before writing — "Yes — these three" (2026-06-08).

## Narrative
- 2026-06-08: Carded after the credentialed flavour landed — the three postures are proven + ADR'd but
  lack a single operator-facing chooser doc. Content drafted above as the spec. (by @assistant)
- 2026-06-08: DONE (authored + reviewed). Wrote `docs/DEPLOYMENT_POSTURES.md` as an **[explanation]**-
  register Diátaxis doc (operator's "Naur × Procida — theory × need" steer) — it transmits the privsep
  theory via a "secret zero" frame so an operator can choose, and points to DEPLOYMENT.md §5b/§5c for
  execution rather than duplicating it (#98 discipline upheld). Structure: secret-zero theory → key-terms
  box → "Choose in one glance" table → three posture sections → road-not-taken (root rejected) →
  cross-cutting (no-root, tier vocab + needs-secret≠trusted, postures-vs-tiers) → where-to-next. Registered
  in `docs/index.md` (grid card) + `mkdocs.yml` (Operating nav, above Deployment). Grilled by two agents:
  a fact-check pass (11/11 claims CONFIRMED vs the 3 ADRs + DEPLOYMENT.md; one max_workers scope nit fixed)
  and an operator-persona clarity pass (all 6 polish items applied: cap-only defined pre-table, Platform row,
  jargon glossed, vocab box hoisted, Posture-1 two-instance annotated, repeated dates/kanban links trimmed).
  No ASCII diagrams. **Commit/merge pending operator go-ahead** (this session.) (by @assistant)
