package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewClearpassBoundsTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	cp, err := NewClearpass(now, 2*time.Hour, "card 208")
	if err != nil {
		t.Fatalf("2h must open: %v", err)
	}
	if cp.OpenedAt != "2026-10-23T10:00:00Z" || cp.Until != "2026-10-23T12:00:00Z" || cp.Note != "card 208" || len(cp.ID) != 32 {
		t.Fatalf("unexpected record: %+v", cp)
	}
	again, _ := NewClearpass(now, 2*time.Hour, "card 208")
	if again.ID == cp.ID {
		t.Fatal("every window has its own id")
	}
	for _, d := range []time.Duration{0, -time.Minute, MaxClearpass + time.Second} {
		if _, err := NewClearpass(now, d, ""); !errors.Is(err, ErrClearpassDuration) {
			t.Fatalf("duration %s must be refused with ErrClearpassDuration, got %v", d, err)
		}
	}
	if _, err := NewClearpass(now, MaxClearpass, ""); err != nil {
		t.Fatalf("exactly 24h must open: %v", err)
	}
}

func TestClearpassOpenAndExpiredFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	cp, _ := NewClearpass(now, time.Hour, "")

	var none *Clearpass
	if none.Open(now) || none.Expired(now) {
		t.Fatal("a nil clearpass is neither open nor expired")
	}
	if !cp.Open(now) || cp.Expired(now) {
		t.Fatal("a fresh window is open, not expired")
	}
	if !cp.Open(now.Add(59 * time.Minute)) {
		t.Fatal("still open a minute before the deadline")
	}
	if cp.Open(now.Add(time.Hour)) || !cp.Expired(now.Add(time.Hour)) {
		t.Fatal("closed exactly at the deadline")
	}
	bad := Clearpass{OpenedAt: cp.OpenedAt, Until: "tomorrow"}
	if bad.Open(now) || bad.Expired(now) {
		t.Fatal("a malformed deadline is neither open nor expired")
	}
	if _, err := bad.Deadline(); err == nil {
		t.Fatal("a malformed deadline must be an error")
	}
}

func TestSetPluginClearpassNeedsARecordedFingerprint(t *testing.T) {
	now := time.Now()
	cp, _ := NewClearpass(now, time.Hour, "")
	existing := []PluginFingerprint{{Name: "b", EntrypointHash: "bb"}, {Name: "a", EntrypointHash: "aa"}}

	out, found := SetPluginClearpass(existing, "zzz", &cp)
	if found {
		t.Fatal("an unattested plugin cannot be clearpassed")
	}
	if len(out) != 2 || out[0].Name != "a" {
		t.Fatalf("set must return the sorted set untouched: %+v", out)
	}

	out, found = SetPluginClearpass(existing, "b", &cp)
	if !found || out[1].Clearpass == nil || out[1].Clearpass.Until != cp.Until || out[0].Clearpass != nil {
		t.Fatalf("set must attach the window to b only: %+v", out)
	}
	if existing[0].Clearpass != nil {
		t.Fatal("set must not mutate its input")
	}

	out, found = SetPluginClearpass(out, "b", nil)
	if !found || out[1].Clearpass != nil {
		t.Fatalf("nil clears the window: %+v", out)
	}
}

// The window lives beside the fingerprint it relaxes: it round-trips through
// .checksums, config lock carries it forward, and plugin lock (a merge of a
// fresh record) clears it.
func TestClearpassRoundTripsAndLockClears(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("service: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := &ConfigFiles{Root: dir, Config: cfgPath}
	if err := GenerateChecksumsWithFingerprints(files, nil, false); err != nil {
		t.Fatalf("seed .checksums: %v", err)
	}

	cp, _ := NewClearpass(time.Now(), time.Hour, "swap")
	fp := PluginFingerprint{Name: "gmail", Enabled: true, ManifestPath: "/m", ManifestHash: "m1", EntrypointPath: "/e", EntrypointHash: "e1", Clearpass: &cp}
	if err := WritePluginFingerprints(dir, []PluginFingerprint{fp}, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := LoadChecksums(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, ok := m.FindPluginFingerprint("gmail")
	if !ok || got.Clearpass == nil || *got.Clearpass != cp {
		t.Fatalf("clearpass must round-trip: %+v", got)
	}

	kept := PreservePluginFingerprints(m.PluginFingerprints, map[string]bool{"gmail": true})
	if kept[0].Clearpass == nil || *kept[0].Clearpass != cp {
		t.Fatal("config lock must carry the window forward verbatim")
	}

	relocked := MergePluginFingerprint(m.PluginFingerprints, PluginFingerprint{Name: "gmail", ManifestHash: "m2", EntrypointHash: "e2"})
	if relocked[0].Clearpass != nil {
		t.Fatal("plugin lock replaces the record and so clears the window")
	}
}

func TestClearpassSealBindsWindowAndFingerprint(t *testing.T) {
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	fp := PluginFingerprint{Name: "gmail", ManifestHash: "m1", EntrypointHash: "e1"}
	cp, _ := NewClearpass(time.Now(), time.Hour, "swap")

	if cp.Verify(fp, nonce) {
		t.Fatal("an unsealed window never verifies")
	}
	sealed, err := cp.Sealed(fp, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if !sealed.Verify(fp, nonce) {
		t.Fatal("a sealed window verifies under its nonce and fingerprint")
	}
	other := make([]byte, 32)
	if sealed.Verify(fp, other) {
		t.Fatal("a different nonce must not verify")
	}
	if sealed.Verify(PluginFingerprint{Name: "gmail", ManifestHash: "m1", EntrypointHash: "e2"}, nonce) {
		t.Fatal("a re-locked fingerprint invalidates the window")
	}
	edited := sealed
	edited.Until = "2030-01-01T00:00:00Z"
	if edited.Verify(fp, nonce) {
		t.Fatal("an edited deadline invalidates the seal")
	}
	edited = sealed
	edited.Note = "something else"
	if edited.Verify(fp, nonce) {
		t.Fatal("an edited note invalidates the seal")
	}
	edited = sealed
	edited.ID = "00000000000000000000000000000000"
	if edited.Verify(fp, nonce) {
		t.Fatal("the id is part of the seal")
	}
	if _, err := cp.Sealed(fp, nonce[:16]); err == nil {
		t.Fatal("a short nonce is refused")
	}
}
