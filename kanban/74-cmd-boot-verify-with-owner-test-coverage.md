---
id: 74
status: done
priority: Normal
blocked_by: []
tags: [test-coverage, vault, boot, attestation, branch-review, regression]
---

# cmd/ductile · automated coverage for boot-verify with a non-nil vault owner (#43 follow-up)

**Found during the 2026-06-06 Dell stress/regression pass on the #43 single-decrypt fix.**

## Gap
The #43 change threads an `owner *vault.Vault` through
`verifyReloadIntegrity → verifyPluginFingerprintsForConfig → fingerprintNonceForConfig`,
and boot/reload pass a **non-nil** owner (`opts.vaultOwner` / `newOwner`). But every existing
Go test exercised that chain with a **nil** owner only:
- `buildRuntime` had **no** test caller at all.
- `service.admission.verify_integrity_on_boot` (the flag that makes `buildRuntime` run the
  boot integrity-verify, i.e. the owner-threaded path) was **never** set in any test.

So the exact lines #43 added — boot/reload sourcing the attestation nonce from the already-
decrypted owner — were only validated by ad-hoc live daemon scenarios on the Dell, not by the
automated suite. A future regression that broke owner threading (or silently reverted to a
fresh decrypt / unkeyed verify) would not have been caught by `go test`.

## Fix (landed 2026-06-06)
Added three tests in `cmd/ductile/plugin_fingerprint_wiring_test.go`, plus a
`buildBootVerifyFixture` helper (fingerprint fixture + an `admission.verify_integrity_on_boot`
block):
- `TestVerifyReloadIntegrityWithOwnerAcceptsCleanBytes` — owner non-nil, clean bytes → verify
  passes (the boot/reload happy path, previously untested with an owner).
- `TestVerifyReloadIntegrityWithOwnerRejectsTamper` — owner non-nil, tampered attested
  entrypoint → rejected. Proves the owner-nonce path stays fail-closed.
- `TestBuildRuntimeBootVerifyRejectsTamperWithOwner` — drives `buildRuntime` with
  `verify_integrity_on_boot: true` and a non-nil `opts.vaultOwner`; a swapped plugin makes boot
  fail closed with an integrity/fingerprint error. First automated `buildRuntime` caller; proves
  the boot wiring actually threads the owner into the integrity check (the integrity check runs
  before any DB/listener setup, so the test is light and returns early).

These codify what Dell live scenarios B (boot-verify(owner) + reload(newOwner)) and C (tamper →
fail-closed) proved by hand. `go vet ./cmd/ductile` + `go test ./cmd/ductile` clean; the three
new tests are platform-independent (no root/permission tricks), pass locally and in CI-equivalent
runs. Closes the follow-up raised at the end of [[43-vault-single-load-thread-nonce-boot]].
