package vault

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// savedVault returns a Loaded vault (one registered principal) backed by a real
// encrypted blob on disk, so Save/rollback exercise the real persistence path.
func savedVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	kr := testKeyring(t)
	path := filepath.Join(dir, "vault.age")

	v := New(path, kr, NewStore())
	if err := v.Store().RegisterPrincipal("mailer", KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Re-Load so lastYAML reflects the on-disk baseline (the daemon's state).
	loaded, err := Load(path, kr)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return loaded
}

// TestVaultSetSecretPersists — a guarded SetSecret writes through to disk; a
// fresh Load sees it.
func TestVaultSetSecretPersists(t *testing.T) {
	v := savedVault(t)

	if err := v.SetSecret("api", "V1", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatalf("set: %v", err)
	}

	reloaded, err := Load(v.Path(), v.keyring)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	sec, ok := reloaded.Store().Secret("api")
	if !ok || sec.Value != "V1" {
		t.Fatalf("expected persisted api=V1, got %+v ok=%v", sec, ok)
	}
}

// TestVaultComposeGuardedReturnsAuthorized — the guarded read path returns the
// same composition the pure store would.
func TestVaultComposeGuardedReturnsAuthorized(t *testing.T) {
	v := savedVault(t)
	if err := v.SetSecret("api", "V1", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatalf("set: %v", err)
	}

	comp, err := v.Compose("mailer")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if comp.Secrets["api"] != "V1" {
		t.Fatalf("expected composed api=V1, got %v", comp.Secrets)
	}
}

// TestVaultSetSecretValidationLeavesModelClean — a validation failure (orphan
// grant) must not mutate the model.
func TestVaultSetSecretValidationLeavesModelClean(t *testing.T) {
	v := savedVault(t)

	err := v.SetSecret("api", "V1", []string{"ghost"}, PatternManual, testTime) // ghost not registered
	if err == nil {
		t.Fatalf("expected orphan-grant error")
	}
	if _, ok := v.Store().Secret("api"); ok {
		t.Fatalf("model mutated despite validation failure")
	}
}

// TestVaultSetSecretRollsBackOnSaveFailure — when the atomic write cannot
// happen, the in-memory model is reverted to the last persisted state so memory
// and disk never diverge.
func TestVaultSetSecretRollsBackOnSaveFailure(t *testing.T) {
	requireWritablePermsEnforced(t)
	v := savedVault(t)
	dir := filepath.Dir(v.Path())

	// Make the directory unwritable so the temp-file write inside Save fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := v.SetSecret("api", "V1", []string{"mailer"}, PatternManual, testTime)
	if err == nil {
		t.Fatalf("expected save failure")
	}
	if _, ok := v.Store().Secret("api"); ok {
		t.Fatalf("failed save must roll back the in-memory mutation, but 'api' is present")
	}
}

// TestVaultAuthenticateAdmin — the genesis admin token authenticates; anything
// else (wrong value, empty) does not. This is the management-API credential.
func TestVaultAuthenticateAdmin(t *testing.T) {
	dir := t.TempDir()
	kr := testKeyring(t)
	path := filepath.Join(dir, "vault.age")

	v, adminToken, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	if !v.AuthenticateAdmin(adminToken) {
		t.Fatal("genesis admin token must authenticate")
	}
	if v.AuthenticateAdmin("not-the-token") {
		t.Fatal("a wrong token must not authenticate")
	}
	if v.AuthenticateAdmin("") {
		t.Fatal("an empty token must not authenticate")
	}
}

// TestVaultAuthenticateAdminRejectsRevoked — a revoked admin token must not
// authenticate (revocation is the kill switch for the management credential).
func TestVaultAuthenticateAdminRejectsRevoked(t *testing.T) {
	dir := t.TempDir()
	kr := testKeyring(t)
	path := filepath.Join(dir, "vault.age")

	v, adminToken, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	sec, ok := v.Store().Secret(AdminTokenSecret)
	if !ok {
		t.Fatal("admin token secret missing")
	}
	sec.Status = StatusRevoked

	if v.AuthenticateAdmin(adminToken) {
		t.Fatal("a revoked admin token must not authenticate")
	}
}

// TestVaultConcurrentComposeAndSet — run with -race: concurrent reads (Compose)
// and writes (SetSecret) on the same owner must be data-race free.
func TestVaultConcurrentComposeAndSet(t *testing.T) {
	v := savedVault(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = v.Compose("mailer")
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				name := "s" + string(rune('a'+id))
				_ = v.SetSecret(name, "v", []string{"mailer"}, PatternManual, testTime)
			}
		}(i)
	}
	wg.Wait()
}
