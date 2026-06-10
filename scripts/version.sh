#!/bin/sh
# Derives the ductile version from git state.
# - With a v* release tag reachable: `git describe` semver — exactly `v1.0.0`
#   on the tagged commit, `v1.0.0-3-gabc1234` for commits after it (3 commits
#   past the tag at abc1234), so a dev build is never mistaken for a release.
# - Before any release tag exists: the historical commit-count scheme,
#   v0.<commit-count>-<short-hash>.
# No manual versioning required — runs identically locally and in CI/Docker.
if v=$(git describe --tags --match 'v[0-9]*' 2>/dev/null) && [ -n "$v" ]; then
  echo "$v"
else
  echo "v0.$(git rev-list --count HEAD)-$(git rev-parse --short HEAD)"
fi
