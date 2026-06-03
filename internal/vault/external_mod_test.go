package vault

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/secrets"
)

// TestSaveRefusesExternalModification is #29's acceptance: with a baseline on
// disk, an out-of-band writer changes vault.age, and the owner's next Save must
// fail loud (ErrVaultModifiedExternally) instead of silently clobbering it.
func TestSaveRefusesExternalModification(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	// The daemon owner writes once, establishing its disk baseline.
	daemon := New(path, kr, NewStore())
	if err := daemon.Save(); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// An out-of-band writer (a stray `secrets rotate`, a manual edit, a botched
	// restore) rewrites the blob with different content — it shares no baseline.
	intruder := New(path, kr, populatedStore())
	if err := intruder.Save(); err != nil {
		t.Fatalf("intruder save: %v", err)
	}

	// The daemon now tries to persist a change. It must refuse loudly.
	daemon.Store().Secrets["new"] = &Secret{Value: "v", Status: StatusActive, Pattern: PatternManual}
	if err := daemon.Save(); !errors.Is(err, ErrVaultModifiedExternally) {
		t.Fatalf("expected ErrVaultModifiedExternally, got %v", err)
	}

	// The on-disk blob is untouched — still the intruder's content, and the
	// daemon's refused change never reached disk.
	reloaded, err := Load(path, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Store().Secret("withings_api"); !ok {
		t.Error("daemon clobbered the externally-written blob")
	}
	if _, ok := reloaded.Store().Secret("new"); ok {
		t.Error("daemon's refused change leaked to disk")
	}
}

// TestSaveAfterRotateKeyPersists proves RotateKey rebases the disk baseline: a
// guarded write after rotation must NOT be mistaken for an external modification.
func TestSaveAfterRotateKeyPersists(t *testing.T) {
	v, keyPath, vaultPath, _, _ := rotateFixture(t)

	if _, err := v.RotateKey(keyPath); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if err := v.SetSecret("post_rotate", "v", nil, PatternManual, time.Now()); err != nil {
		t.Fatalf("save after rotate must not false-trigger the backstop: %v", err)
	}

	newKR, err := secrets.LoadKeyringFromFile(keyPath)
	if err != nil {
		t.Fatalf("load rotated keyring: %v", err)
	}
	reloaded, err := Load(vaultPath, newKR)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Store().Secret("post_rotate"); !ok {
		t.Error("post-rotate guarded write was not persisted")
	}
}
