package config

// The integrity helpers must read the exact path ductile will open or exec.
// A path that differs only by surrounding whitespace names a different file;
// normalising it (as yamlconfig.ExpandPath does with TrimSpace) lets an
// attacker fingerprint one file while ductile executes its twin.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// writeWhitespaceTwins writes "run.sh" and "run.sh " with different bytes and
// returns the directory.
func writeWhitespaceTwins(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho fingerprinted\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh "), []byte("#!/bin/sh\necho executed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestComputeFileHashReadsTheExactPath(t *testing.T) {
	t.Parallel()
	dir := writeWhitespaceTwins(t)
	twin := filepath.Join(dir, "run.sh ")
	data, err := os.ReadFile(twin)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := "sha256:" + hex.EncodeToString(sum[:])
	got, err := ComputeFileHash(twin)
	if err != nil {
		t.Fatalf("ComputeFileHash(%q): %v", twin, err)
	}
	if got != want {
		t.Fatalf("ComputeFileHash(%q) = %s, want the digest of that file's bytes %s (it hashed a different file)", twin, got, want)
	}
}

func TestComputeKeyedFileHashReadsTheExactPath(t *testing.T) {
	t.Parallel()
	dir := writeWhitespaceTwins(t)
	twin := filepath.Join(dir, "run.sh ")
	data, err := os.ReadFile(twin)
	if err != nil {
		t.Fatal(err)
	}
	key := key32(0x5a)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	want := "sha256:" + hex.EncodeToString(mac.Sum(nil))
	got, err := ComputeKeyedFileHash(twin, key)
	if err != nil {
		t.Fatalf("ComputeKeyedFileHash(%q): %v", twin, err)
	}
	if got != want {
		t.Fatalf("ComputeKeyedFileHash(%q) = %s, want the HMAC of that file's bytes %s (it hashed a different file)", twin, got, want)
	}
}

// TestPluginFingerprintFollowsTheExecutedEntrypoint drives the attack end to
// end through the fingerprint ductile records and re-verifies: the executed
// "run.sh " changes, the untouched "run.sh" does not, and the fingerprint must
// notice.
func TestPluginFingerprintFollowsTheExecutedEntrypoint(t *testing.T) {
	t.Parallel()
	dir := writeWhitespaceTwins(t)
	twin := filepath.Join(dir, "run.sh ")
	key := key32(0x5a)
	before, err := ComputeKeyedFileHash(twin, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(twin, []byte("#!/bin/sh\necho swapped\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := ComputeKeyedFileHash(twin, key)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatalf("fingerprint of %q unchanged after its bytes changed; it is reading another file", twin)
	}
}

func TestDetectSymlinksChecksTheExactPath(t *testing.T) {
	t.Parallel()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(plain, []byte("service: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(target, []byte("service: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	twin := plain + " "
	if err := os.Symlink(target, twin); err != nil {
		t.Fatal(err)
	}
	got, err := DetectSymlinks([]string{twin})
	if err != nil {
		t.Fatalf("DetectSymlinks: %v", err)
	}
	if len(got) != 1 || got[0].Path != twin || got[0].Resolved != target {
		t.Fatalf("DetectSymlinks([%q]) = %+v; want one warning for that exact path resolving to %s", twin, got, target)
	}
}
