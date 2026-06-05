---
id: 52
status: done
priority: High
blocked_by: [50]
tags: [vault, deploy, thinkpad, build]
---

# R2 — Build the branch binary in place on the Thinkpad (normal process)

Epic: [[49-epic-thinkpad-vault-field-trial]]. Build on-host at `~/Projects/ductile/` per
`docs/DEPLOYMENT.md` §1/§8. (The host's *default* `go` is 1.22.2, but `GOTOOLCHAIN=auto` + the
`go 1.25.0` directive in go.mod means Go auto-selects the 1.25 toolchain inside the module — verified
`go version` reports go1.25.0 in the repo dir. No cross-compile, no manual Go upgrade needed.)

## Steps
1. On the Thinkpad, in `~/Projects/ductile/`: stash/clear the scratch untracked files if needed, then
   `git fetch && git checkout feat/age-secrets-and-spawn-hygiene && git pull` (currently on `main`).
2. Build, staging the binary so the offline gate ([[53-rung3-offline-deploy-gate]]) can run it before
   cutover while v0.783 is still installed:
   `go build -o ~/admin/ductile-backups/thinkpad/ductile-vaulttrial ./cmd/ductile`
   (Go auto-fetches the 1.25.0 toolchain on first build if not cached.)
3. `~/admin/ductile-backups/thinkpad/ductile-vaulttrial version` → confirm it reports the branch commit.

## Acceptance
- Branch binary built on-host from `~/Projects/ductile/` on `feat/age-secrets-and-spawn-hygiene`,
  reports the branch commit, staged in the backups dir (install to `~/.local/bin` happens at
  cutover [[60-rung10-cutover]]).

## Note
- This follows the normal on-host process; the earlier cross-compile-from-Mac path is NOT needed
  (Go toolchain auto-management covers the 1.22→1.25 gap).
