package vault

import (
	"path/filepath"
	"testing"
)

// TestRotateAdminTokenRollsCredentialInPlace is the #69 acceptance test: an
// operator can roll the genesis admin token in place (no re-genesis), the old
// token stops authenticating, the new one does, the roll is counted, and the
// change is persisted to the blob.
func TestRotateAdminTokenRollsCredentialInPlace(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	v, oldToken, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !v.AuthenticateAdmin(oldToken) {
		t.Fatal("genesis token should authenticate before rotation")
	}

	newToken, err := v.RotateAdminToken(testTime)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if newToken == "" || newToken == oldToken {
		t.Fatalf("rotation must mint a fresh non-empty token, got %q (old %q)", newToken, oldToken)
	}

	// The old token is dead; only the new one authenticates.
	if v.AuthenticateAdmin(oldToken) {
		t.Fatal("old admin token must stop authenticating after rotation")
	}
	if !v.AuthenticateAdmin(newToken) {
		t.Fatal("new admin token must authenticate after rotation")
	}

	// Rotation is counted on the reserved secret (genesis seeded it at count 0).
	sec, ok := v.Store().Secret(AdminTokenSecret)
	if !ok {
		t.Fatal("admin token secret missing after rotation")
	}
	if sec.RollCount != 1 {
		t.Fatalf("expected roll_count 1 after one rotation, got %d", sec.RollCount)
	}
	if len(sec.AuthorizedPrincipals) != 0 {
		t.Fatalf("admin token must authorize no principals, got %v", sec.AuthorizedPrincipals)
	}

	// Persisted: a fresh load from disk sees the new token, not the old.
	reloaded, err := Load(path, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.AuthenticateAdmin(oldToken) {
		t.Fatal("reloaded vault must reject the old token")
	}
	if !reloaded.AuthenticateAdmin(newToken) {
		t.Fatal("reloaded vault must accept the new token (rotation not persisted)")
	}
}

// TestRotateAdminTokenStaysReserved guards the privilege-escalation invariant:
// rotating the admin token must never expose it through the data plane — it is
// not delivered to the core principal, and the reserved guard still refuses
// data-plane writes to it after a rotation.
func TestRotateAdminTokenStaysReserved(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	v, _, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := v.RotateAdminToken(testTime); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	// Data-plane writers still refuse the reserved secret (no escalation path).
	if err := v.SetSecret(AdminTokenSecret, "x", nil, PatternManual, testTime); err == nil {
		t.Fatal("SetSecret must still refuse the reserved admin-token secret after rotation")
	}
	if err := v.Roll(AdminTokenSecret, "x", testTime); err == nil {
		t.Fatal("Roll must still refuse the reserved admin-token secret after rotation")
	}

	// And it is never composed to the core principal.
	comp, err := v.Store().Compose(CorePrincipal)
	if err != nil {
		t.Fatalf("compose core: %v", err)
	}
	if _, leaked := comp.Secrets[AdminTokenSecret]; leaked {
		t.Fatal("rotated admin token must stay API-internal, not delivered via Compose")
	}
}
