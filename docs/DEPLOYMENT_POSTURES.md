# Deployment Postures — choosing how much to confine plugins

> **Diátaxis register: _explanation_.** This page builds the *theory* you need to **choose** a
> deployment shape. It deliberately holds no install steps — once you have chosen, the procedure
> lives in [Deployment §5b](DEPLOYMENT.md) (how-to) and [Deployment §5c](DEPLOYMENT.md)
> (config reference). Theory here; need served by the links.

You can run ductile three ways. They differ in **one thing only**: how much authority a plugin
inherits when it runs. Everything else — the event router, pipelines, the CLI, the API — is identical.
Pick the posture that matches *who wrote your plugins and what they're allowed to touch*, then
follow the linked how-to to stand it up.

All three are real and proven. The third (hybrid trust-tier) is field-proven on a live homelab
gateway — see Posture 3 below.

---

## The one idea everything turns on: secret zero

Privsep exists for exactly one reason: **so that a _plugin_ cannot read secret zero** — the age
private key (`secrets.age_key_file`, e.g. `/etc/ductile/secret/age.key`, mode `0600`) from which the
whole vault decrypts. A plugin must not be able to read that key off disk, *and* must not be able to
`ptrace` the gateway process (attach to it and read its memory) to lift the key out.

Two consequences follow, and they explain every choice below:

- **The threat model is your own fallible code, not a hostile stranger.** You wrote (or vouched for)
  every plugin. The wall is there to contain a *bug* or a *swapped file* — a plugin that does more
  than you intended — not to survive a determined attacker who already owns the box.
- **The vault protects against plugins, never against the operator.** The operator is
  root-equivalent by design (they hold the key, the config, and the host). No posture tries to wall
  the operator off from their own secrets — that would be incoherent.

A posture is just a decision about **how far down** you push that wall.

---

## A few terms, defined before you need them

- **cap-only** — the gateway runs as a non-root `ductile` user holding just two Linux kernel
  capabilities, `CAP_SETUID` and `CAP_SETGID` (granted by the init system). That's exactly enough to
  *drop* a plugin to an unprivileged uid and nothing more — no root, no other power.
- Three words that name three unrelated things — don't conflate them:
    - **account** — a privsep OS user a plugin is dropped to (the `accounts:` map, `run_as:`). About *uid*.
    - **worker** — a concurrency slot (`service.max_workers`, a *general* service setting, not a privsep
      key). About *how many run at once*. Nothing to do with identity.
    - **principal** — a vault identity that may be granted secrets. About *who may decrypt what*.

---

## Choose in one glance

| | **1. Unconfined** | **2. Full privsep** | **3. Hybrid trust-tier** |
|---|---|---|---|
| **Gateway runs as** | service user *or* root | cap-only `ductile` (no root) | cap-only `ductile` (no root) |
| **Plugins run as** | the gateway (no uid drop) | confined accounts (`default` 1001 / `untrusted` 1002) | confined **by default**, plus a **credentialed** tier that drops to *your* uid |
| **Secret zero protected from plugins?** | **No** | **Yes** | **Yes** |
| **Plugins can act as you (`~/.ssh`, `gh`)?** | yes (everything can) | no | only the credentialed tier |
| **Isolation between plugins** | none | per-account `0700` walls | per-account walls; trusted tier shares your home |
| **Root needed at runtime** | optional | **never** (Linux) | **never** (Linux) |
| **Platform** | any | **Linux-proven; macOS-pending** | **Linux-proven; macOS-pending** |
| **Pick when** | a single trusted dev box — *or*, as a deliberate **two-instance** topology, the [admin-glue instance](runbooks/ductile-admin-instance.md) paired with an enforced data-plane | multi-source / untrusted plugins / production | homelab: confined by default, a few vouched plugins act as you |
| **Stand it up** | [§5 / §5b escape hatch](DEPLOYMENT.md) | [§5b enforce](DEPLOYMENT.md) | [§5b enforce](DEPLOYMENT.md) + group ACL |

> A picture helps here. The architecture diagram for these three postures is operator-owned
> (mermaid) and lives alongside this page — it is intentionally not drawn in ASCII.

---

## 1. Unconfined (hygiene-only)

**What it is.** The gateway runs as an ordinary service user (or root), and **plugins run *as* the
gateway — there is no uid drop.** This is the simplest shape and the historical default.

**What it protects.** Nothing, with respect to secret zero. A plugin running as the gateway can read
the age key on disk and can `ptrace` the gateway. There is also no isolation *between* plugins. You
get ductile's operational hygiene (the queue, retries, the ledger) but no privilege wall.

**When to pick it.**

- A fully-trusted single-user or dev box, where every plugin is yours and the threat model is empty.
- The deliberate **admin-glue instance** ([runbook](runbooks/ductile-admin-instance.md), card #106):
  one instance left intentionally unconfined to do host-entangled work (docker, system glue), *paired
  with* a separate enforced data-plane instance. Here "unconfined" is a considered choice, not a
  default — the dangerous instance is the one that does *not* face untrusted input.

**Setup essentials.** No `accounts:` map, no enforce. On a cap-only or non-root setup it is the
default; if the gateway runs as **root**, the boot gate *refuses to boot* unconfined unless you set
the loud escape hatch `service.unconfined: true`. See [§5 and the §5b escape hatch](DEPLOYMENT.md).

---

## 2. Full privsep (FHS enforce, all-confined)

**What it is.** The gateway is **cap-only**: a non-root system user `ductile` granted just
`CAP_SETUID` + `CAP_SETGID` (systemd `AmbientCapabilities`), enough to drop to an unprivileged uid
and nothing more. **Every** plugin drops to a dedicated confined account and runs **walled** inside a
private `0700` state directory.

**What it protects.** Secret zero is safe: the age key is `ductile`-owned `0600`, and a confined
account is neither the gateway user nor in any group that can read the key or `ptrace` the gateway.
Plugins are also isolated from *each other* — each account owns only its own `0700` `state_dir`.

**The mechanics (theory, not steps).**

- **Confined runtime contract:** a confined plugin's `HOME`, cache, and cwd are all set to its
  account's own `state_dir`; it cannot see another account's state. Per-account dirs are `0700`;
  `/var/lib/ductile` and `/var/lib/ductile/accounts` sit at a `0711` *traversal floor*
  (traverse-only, not listable). Full rationale:
  [confined-plugin-runtime-contract ADR](adr/confined-plugin-runtime-contract.md).
- **Code, not creds:** plugin code lives at `/opt/ductile/plugins` (`root:root`, world `r-x`,
  attested and locked — its hash is checked before spawn). Plugins never receive secrets via env or
  config — they get them from the **vault** at spawn, gated by that same attestation check.

**When to pick it.** Multiple sources of plugins, any untrusted or third-party plugin, or production
where isolation matters most. This posture is **deploy-as-new** onto an FHS (Filesystem Hierarchy Standard) layout
([filesystem-layout ADR](adr/filesystem-layout.md)); see the [enforce how-to](DEPLOYMENT.md) (§5b).

> **Platform note:** enforce is **Linux-proven, macOS-pending**. macOS has no cap-only model — the
> daemon runs as root and drops — so treat macOS enforce as not-yet-field-proven (tracked as card #95).

---

## 3. Hybrid trust-tier (the recommended homelab shape)

**What it is.** The same cap-only gateway as posture 2 — **one** instance, **one** event router, so
native pipelines still chain across all your plugins. Plugins are **confined by default** (walled +
vault, exactly as posture 2), *plus* a **credentialed `trusted` tier** that drops to **your real
uid** and runs with **your real home** — so a vouched-for plugin can reach `~/.ssh`, `~/.config/gh`,
`~/.config/fabric` and genuinely act *as you* (e.g. `git push`).

**How the trusted tier reaches your files without opening your home.**

- **Creds come from your home, code comes via a group ACL.** Trusted plugin code lives in your home
  (e.g. `~/ductile/plugins`). The gateway reads that code through a **`ductile`-group ACL** — the
  authoritative entry form is *"`g:ductile:rX` on the plugin dir, `g:ductile:x` on the home"*
  ([credentialed-runtime ADR](adr/credentialed-runtime-contract.md)). That grants the gateway
  **code-read only — never the creds, and never to the confined accounts.** No `/home` is thrown
  open. (The exact `setfacl` invocation is in the
  [ThinkPad enforce runbook](runbooks/privsep-thinkpad-enforce.md).)
- **groups-minimal:** the trusted account gets *your files*, not your group memberships. Docker is
  **not** granted by default — it's a per-plugin opt-in, parked until needed.

**What it protects.** Secret zero is still safe (the age key remains `ductile`-owned; confined
accounts and the trusted account alike are walled off from it). The residual risk is intrinsic: a
trusted plugin runs **as you**, and if *you* are root-equivalent on that host (e.g. a member of the
`docker` group), so is that plugin for the moment it runs. That is **accepted operational
insecurity**, surfaced — not hidden — by the tier-aware, warn-loud **root-sidedoor boot audit**
(tracked as card #111): confined + a sidedoor is a fail-closed lie about the wall; trusted + a
sidedoor is an informed-consent log line.

**When to pick it.** A homelab where a *few* vouched-for plugins must act as you, but you don't want
a second instance and you don't want root anywhere. This is the **recommended homelab posture**.
Current guidance: `run_as: <you>` for the trusted plugins now, with a per-plugin tier review later.

**Field-proven.** Live on the ThinkPad homelab gateway, 2026-06-08: one cap-only gateway, three
accounts, confined and credentialed plugins coexisting on one event router — a trusted plugin cloned a
repo into a directory only your uid can write (proving it ran as you), while confined plugins
remained provably unable to reach your ssh key.

---

## The road not taken: root gateway

A **root gateway** was considered and **rejected**. Cap-only's `CAP_SETUID` already drops to *any*
uid — including your own login uid for the trusted tier — and the `ductile`-group ACL already gives
the gateway code-read without root. Root buys nothing the cap-only model lacks, and it re-enlarges
the blast radius of the one process that faces the network. Every posture above runs **without root
at runtime**; the only root needed anywhere is the one-time install.

---

## Cross-cutting notes (true in every posture)

### No root except one-time install

None of the three postures runs the gateway as root at runtime (on Linux). Root appears only during
the initial install/provisioning. The cap-only model is what makes that possible.

### Tier vocabulary, and why "needs a secret" ≠ "trusted"

The per-account mode is the `AccountMode` enum: **`unconfined` | `confined` | `credentialed`**. The
tempting mistake is to assume a plugin that needs a secret must be trusted. It does not: a read-only
plugin that holds a vault token stays **confined** — the vault delivers its one credential without
ever giving it your home or your uid. `github_repo_sync` is the worked example: it carries a token
yet remains confined. Reserve **credentialed/trusted** for plugins that must act *as you*.

### Postures and tiers are orthogonal axes

A **posture** is a *deployment-wide* choice (this page). A **tier** is a *per-plugin* choice — which
account a given plugin's `run_as:` grants. Posture 3 is precisely the case where both axes are in
play at once. For per-plugin tier guidance see
[PLUGIN_DEVELOPMENT §10.6 — Runtime contract (privsep)](PLUGIN_DEVELOPMENT.md) and the working
[`plugins/_template/`](https://github.com/mattjoyce/ductile/tree/main/plugins/_template).

---

## Where to go next

| You want to… | Read |
|---|---|
| Stand up an enforcing host (steps) | [Deployment §5b](DEPLOYMENT.md) *(how-to)* |
| Look up a privsep config key or boot-gate outcome | [Deployment §5c](DEPLOYMENT.md) *(reference)* |
| Understand *why* confined plugins are shaped this way | [confined-plugin-runtime-contract ADR](adr/confined-plugin-runtime-contract.md) *(explanation)* |
| Understand *why* the trusted tier is safe | [credentialed-runtime-contract ADR](adr/credentialed-runtime-contract.md) *(explanation)* |
| See the FHS install layout | [filesystem-layout ADR](adr/filesystem-layout.md) |
| Follow the first live enforce cutover | [privsep-thinkpad-enforce runbook](runbooks/privsep-thinkpad-enforce.md) |
| Run the deliberate unconfined admin instance | [ductile-admin-instance runbook](runbooks/ductile-admin-instance.md) |
