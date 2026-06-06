---
id: 89
status: backlog
priority: Low
blocked_by: [88]
tags: [privsep, deploy, docker, unraid, deferred]
---

# Privsep · deploy — Docker / Unraid (LAST; may stay hygiene-only)

> **Nav:** [[83-privsep-epic]] · after [[88-privsep-deploy-systemd-launchd]] · **last rung**

The hardest, riskiest target — and deliberately **last**. No init manager in the container,
today's image runs `USER ductile` (can't change uid), and adopting privsep *raises* the
container's privilege. Only do this once systemd+launchd (#88) are proven, and only if the
isolation is worth a privileged container here.

**Scope (if adopted):**
- Bake `default`/`untrusted` worker uids into the Dockerfile.
- Run the container **root or `--cap-add=SETUID --cap-add=SETGID`** (prefer caps), replacing
  `USER ductile`; document the Unraid template change.
- Fix volume ownership so #87's `0700` per-worker dirs survive container recreation.
- **Secret-zero (Q3):** no TPM/Keychain in a container → the age key is a **`0600` key file
  bind-mounted from the host, outside the image and the config bundle** (ADR §8). Worker uids
  baked into the image (Q4) — the k8s/image provisioning pattern.

**Acceptance:** Unraid container boots with worker uids + SETUID/SETGID, drops plugins to
workers, worker state dirs persist across recreation. **OR** an explicit decision that Unraid
**stays hygiene-only (1a)** — the ADR allows hygiene-only as a legitimate per-host default.

## Narrative
- **Source:** PrivSec ADR §5 "Per-host reality — Unraid/Docker" (the explicit hard case).
- **Sequenced + down-priced (Brooks×Beck review):** deferred behind #88, priority Low. Decide
  *whether* to privilege this container at all — hygiene-only may be the honest answer for the
  one host where raising privilege is most costly. Flag for the parallel ADR grill.
- Live host Unraid (#68); riskiest re-deploy of the three.
