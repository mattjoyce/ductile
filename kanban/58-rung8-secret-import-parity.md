---
id: 58
status: done
priority: Normal
blocked_by: [57]
tags: [vault, deploy, thinkpad, secrets, migration, parity]
---

# R8 — Import tokens.yaml secrets into the vault + prove parity

Epic: [[49-epic-thinkpad-vault-field-trial]]. Migrate the 6 tokens.yaml secrets into the vault so
`secret_ref` consumers resolve from the vault. Keep tokens.yaml (retiring it is the separate epic
[[48-epic-retire-tokens-yaml]]).

## The 6 secrets (from recon)
astro_rebuild_staging_secret, github_repo_sync_secret, git_repo_sync_secret,
ductile_github_interest_secret, ap_canary_secret, relay-unraid-thinkpad-v1.

## Steps
1. From [[50-rung0-thinkpad-deep-recon]] residual: classify each as literal vs `${ENV}` indirection.
2. **Service stopped** (import takes the PID lock): `ductile vault import --config ~/.config/ductile/ --tokens tokens.yaml [--resolve-env]`
   - `--resolve-env` only if you intend to freeze `${ENV}` values into the vault; otherwise handle those
     entries explicitly (don't silently freeze). Import copies literals; flags ${ENV} for a decision.
3. **Prove parity**: for every tokens.yaml entry, confirm the vault yields the same resolved value
   (use the key-holding `vault get` read path). Resolve any same-name collisions definitively.
4. Leave tokens.yaml in place (coexistence shim); the graft overlays vault-active secrets at load.

## Acceptance
- All 6 secrets present in the vault; per-entry parity verified vs tokens.yaml; ${ENV} entries handled
  by explicit decision; tokens.yaml retained. Retirement deferred to [[48-epic-retire-tokens-yaml]].
