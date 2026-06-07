---
id: 112
status: todo
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
- [ ] Write the guide — likely `docs/DEPLOYMENT_MODES.md` (or extend DEPLOYMENT.md): the three postures
      as a chooser (when-to-use / what-it-protects / setup-essentials / tradeoffs) + the rejected root note.
- [ ] A decision table / one diagram (operator-owned mermaid per [[diagrams-are-user-owned]]).
- [ ] Link the relevant ADRs (confined-runtime, credentialed-runtime, filesystem-layout) + runbooks
      (privsep-thinkpad-enforce, ductile-admin-instance).
- [ ] Cross-link the plugin-tier guidance (PLUGIN_DEVELOPMENT.md §10.6, the `_template`) — postures
      (deploy) vs tiers (per-plugin) are orthogonal axes; say so.
- [ ] Confirm "three" matches the operator's intent (unconfined / full-privsep / hybrid) before writing.

## Narrative
- 2026-06-08: Carded after the credentialed flavour landed — the three postures are proven + ADR'd but
  lack a single operator-facing chooser doc. Content drafted above as the spec. (by @assistant)
