package config

import (
	"os"
	"strings"
	"testing"
)

// VerifyResolvedPluginFingerprint re-hashes one plugin's live bytes and compares
// against its recorded keyed fingerprint — the focused per-spawn check behind
// compose-time re-verification (§3.3). It is fail-closed: any mismatch, missing
// nonce, or unreadable file is an error.
func TestVerifyResolvedPluginFingerprintMatch(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := VerifyResolvedPluginFingerprint(fp, cur, testFPNonce()); err != nil {
		t.Fatalf("unchanged bytes must verify: %v", err)
	}
}

func TestVerifyResolvedPluginFingerprintManifestMismatch(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := os.WriteFile(cur.ManifestPath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatalf("tamper manifest: %v", err)
	}
	err := VerifyResolvedPluginFingerprint(fp, cur, testFPNonce())
	if err == nil {
		t.Fatal("manifest tamper must fail verification")
	}
	if !strings.Contains(err.Error(), "gmail") || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("error should name plugin + manifest: %v", err)
	}
}

func TestVerifyResolvedPluginFingerprintEntrypointMismatch(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := os.WriteFile(cur.EntrypointPath, []byte("#!/bin/sh\necho swapped\n"), 0o755); err != nil {
		t.Fatalf("tamper entrypoint: %v", err)
	}
	err := VerifyResolvedPluginFingerprint(fp, cur, testFPNonce())
	if err == nil {
		t.Fatal("entrypoint tamper must fail verification")
	}
	if !strings.Contains(err.Error(), "entrypoint") {
		t.Fatalf("error should name entrypoint: %v", err)
	}
}

// Fail-closed: a missing/short nonce must NOT silently pass (downgrade guard).
func TestVerifyResolvedPluginFingerprintFailsClosedWithoutNonce(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := VerifyResolvedPluginFingerprint(fp, cur, nil); err == nil {
		t.Fatal("nil nonce must fail closed")
	}
	if err := VerifyResolvedPluginFingerprint(fp, cur, []byte("short")); err == nil {
		t.Fatal("short nonce must fail closed")
	}
}

func TestVerifyResolvedPluginFingerprintUnreadableBytesError(t *testing.T) {
	tmp := t.TempDir()
	fp, cur := lockAndCurrent(t, tmp, "gmail", true, "")
	if err := os.Remove(cur.EntrypointPath); err != nil {
		t.Fatalf("remove entrypoint: %v", err)
	}
	if err := VerifyResolvedPluginFingerprint(fp, cur, testFPNonce()); err == nil {
		t.Fatal("unreadable entrypoint must fail closed")
	}
}
