package vault

import (
	"path/filepath"
	"testing"
)

// TestSnapshotIsIndependentDeepCopy proves Snapshot returns a copy a caller can
// mutate freely without ever touching the live model — closing the Store()
// aliasing hole for the read path (#24).
func TestSnapshotIsIndependentDeepCopy(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")
	v := New(path, kr, populatedStore())

	snap, err := v.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Tear the snapshot every way a second writer might: a nested value, a new
	// entry, a principal status, and an authorized-principals slice element.
	snap.Secrets["withings_api"].Value = "TAMPERED"
	snap.Secrets["withings_api"].AuthorizedPrincipals[0] = "attacker"
	snap.Secrets["injected"] = &Secret{Value: "x", Status: StatusActive, Pattern: PatternManual}
	snap.Principals["withings"].Status = StatusRevoked

	live := v.Store()
	if got := live.Secrets["withings_api"].Value; got != "top-secret-value" {
		t.Errorf("live value mutated through snapshot: got %q", got)
	}
	if got := live.Secrets["withings_api"].AuthorizedPrincipals[0]; got != "withings" {
		t.Errorf("live grant mutated through snapshot: got %q", got)
	}
	if _, ok := live.Secrets["injected"]; ok {
		t.Error("snapshot insertion reached the live model")
	}
	if live.Principals["withings"].Status != StatusActive {
		t.Error("snapshot principal mutation reached the live model")
	}
}
