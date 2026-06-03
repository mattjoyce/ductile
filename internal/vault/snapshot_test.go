package vault

import (
	"path/filepath"
	"testing"
	"time"
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

// TestSetManualBatchPersistsOnceAndReportsFailures proves the guarded batch
// upsert lands valid entries (persisted to disk in one Save), collects per-entry
// failures without aborting, and never reaches the live model through Store().
func TestSetManualBatchPersistsOnceAndReportsFailures(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")
	v := New(path, kr, NewStore())
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)

	failures, err := v.SetManualBatch([]ManualSecret{
		{Name: "alpha", Value: "a-val"},
		{Name: "Invalid Name", Value: "ignored"}, // rejected by the name rule
		{Name: "beta", Value: "b-val"},
	}, now)
	if err != nil {
		t.Fatalf("SetManualBatch: %v", err)
	}
	if len(failures) != 1 || failures[0].Name != "Invalid Name" {
		t.Fatalf("expected one failure for the invalid name, got %+v", failures)
	}

	// Valid entries persisted and survive a reload from disk.
	reloaded, err := Load(path, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		sec, ok := reloaded.Store().Secret(name)
		if !ok {
			t.Errorf("%q not persisted", name)
			continue
		}
		if sec.Pattern != PatternManual || sec.Status != StatusActive {
			t.Errorf("%q has wrong pattern/status: %+v", name, sec)
		}
	}
	if _, ok := reloaded.Store().Secret("Invalid Name"); ok {
		t.Error("rejected entry must not be persisted")
	}
}
