---
id: 143
status: todo
priority: High
tags: [release, semver, docs, ci]
---

# Cut v1.0.0 — first tagged release, Linux + Darwin

> When the credential-ladder epic is merged and main is gated green, I want a tagged, downloadable
> v1.0.0 for Linux and Darwin, so an outsider can install and bootstrap ductile without reading
> source or shell scripts.

## Already in place (2026-06-10 prep)
- `scripts/version.sh` is tag-aware: `git describe` semver once a `v*` tag exists (exactly `v1.0.0`
  on the tag, `v1.0.0-N-g<hash>` after it), commit-count fallback before the first tag.
- `.github/workflows/release.yml`: tag-push → test gate → CGO-free cross-compile of
  linux/{amd64,arm64} + darwin/{amd64,arm64} → sha256 checksums → GitHub release with generated
  notes. `workflow_dispatch` = dry run (artifacts only, no release).
- `docs/BOOTSTRAP.md`: from-zero ladder walk, both platforms; DEPLOYMENT.md's dead
  MACOS_INSTALLATION.md link repointed.

## Remaining sequence
1. ~~Docs review~~ **DONE 2026-06-10**: Naur×Procida audit of the release path; 16 fixes landed
   (3 live-reproduced blockers incl. the example config + BOOTSTRAP walk; phantom `system stop`;
   DEPLOYMENT/MACOS literal-token fossils). Bootstrap walk proven by execution. Residue for Matt:
   editorial choice of showcased plugins in the commented templates.
2. ~~Root configs~~ **DONE** (`0581e56`): root `config.yaml` + `config.test.yaml` dropped.
   `schemas/tokens.schema.json`: still undecided (nothing in config/ depends on it).
3. ~~#132 stance~~ **DECIDED 2026-06-10**: doc-only, NOT a release blocker. Matt authors the
   nginx reverse-proxy example himself; guardrail (option B) stays on card #132 for post-1.0.
4. ~~CHANGELOG~~ **DONE 2026-06-10**: v1.0.0 entry written, agent-fact-checked against the
   commit range (one naming nit corrected).
5. ~~Dry-run~~ **DONE**: run 27253743242 (4 artifacts, no release). Tests re-gate at tag time.
6. `git tag -a v1.0.0 && git push origin v1.0.0` → workflow publishes the release.
7. Credibility test: install from the published artifacts on the Mac AND the Thinkpad following
   BOOTSTRAP.md verbatim — no improvisation. Fix what it surfaces; if anything changed, v1.0.1.
   (A local-binary preview of this walk already passed on the Mac, 2026-06-10.)

## Done when
A GitHub release v1.0.0 exists with four checksummed binaries; `ductile version` on an installed
artifact prints v1.0.0; both platforms bootstrapped from the published docs without deviation.

## Narrative
- 2026-06-10: Doc review became a Naur×Procida audit — running the docs as programs found what
  reading could not: the shipped example could never pass `config check` once copied (tilde paths
  join under the config dir), BOOTSTRAP's minimal config was missing `plugin_roots`, and lock must
  precede check when webhooks are included. All fixed and proven by execution. (by @assistant)
- 2026-06-10: The audit's two design escalations both landed: the loader now expands a leading
  `~` (kills the error class), and the no-`system stop` stance is canon in code — refusal with
  service-manager guidance, per the Armstrong supervisor-owns-lifecycle frame. (by @assistant)
- 2026-06-10: Matt ruled #132 doc-only and not a blocker (he'll write the nginx example).
  CHANGELOG v1.0.0 entry written and independently fact-checked. Only the tag (step 6) and the
  published-artifact credibility test (step 7) remain. (by @assistant)
