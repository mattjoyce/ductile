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
1. Matt reviews docs (tonight, 2026-06-10): BOOTSTRAP.md, DEPLOYMENT.md, SECRETS.md, README.
2. Decide repo-hygiene candidates: root `config.yaml` (personal dev config), `config.test.yaml`
   (pre-ladder, unused), `schemas/tokens.schema.json` (legacy, still served by `config validate`).
3. Decide #132 stance for 1.0: ship the cleartext guardrail, or a loud documented stance.
4. CHANGELOG.md entry for 1.0 (last entry is 2026-05-31, pre-epic).
5. Dry-run the workflow (`gh workflow run release.yml`), check the four artifacts + version output.
6. `git tag -a v1.0.0 && git push origin v1.0.0` → workflow publishes the release.
7. Credibility test: install from the published artifacts on the Mac AND the Thinkpad following
   BOOTSTRAP.md verbatim — no improvisation. Fix what it surfaces; if anything changed, v1.0.1.

## Done when
A GitHub release v1.0.0 exists with four checksummed binaries; `ductile version` on an installed
artifact prints v1.0.0; both platforms bootstrapped from the published docs without deviation.
