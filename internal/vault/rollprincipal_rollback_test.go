package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRollPrincipalRollsBackOnSaveFailure proves the #27/F6 fix: RollPrincipal is
// atomic — when the persist fails after rolling secrets in memory, the model is
// restored, so no partial roll is left resident (the value stays at its original).
func TestRollPrincipalRollsBackOnSaveFailure(t *testing.T) {
	requireWritablePermsEnforced(t)
	v := savedVault(t)
	if err := v.RegisterPrincipal("rollerx", KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := v.SetSecret("hmacx", "ORIG", []string{"rollerx"}, PatternAuto, testTime); err != nil {
		t.Fatalf("set: %v", err)
	}

	dir := filepath.Dir(v.Path())
	if err := os.Chmod(dir, 0o500); err != nil { // unwritable → Save's temp write fails
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, _, err := v.RollPrincipal("rollerx", laterTime); err == nil {
		t.Fatal("expected a save failure")
	}
	sec, ok := v.Store().Secret("hmacx")
	if !ok {
		t.Fatal("secret vanished")
	}
	if sec.Value != "ORIG" {
		t.Fatalf("partial roll left in memory after failed save: value=%q, want ORIG", sec.Value)
	}
}
