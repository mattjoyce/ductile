package vault

import (
	"path/filepath"
	"testing"
)

// TestRung1EndToEnd exercises the whole Rung 1 lifecycle through the real
// persistence boundary: genesis writes an encrypted blob to disk, a fresh Load
// reads it back, and Compose delivers exactly the right secrets to a registered
// plugin. It proves the components (#2-#6) compose and survive a round-trip,
// which the per-unit tests do not.
func TestRung1EndToEnd(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	// Genesis on disk.
	v, adminToken, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	// Operator registers a plugin and grants it a secret, then persists.
	if err := v.Store().RegisterPrincipal("withings", KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := v.Store().SetSecret("withings_api", "TOKEN-XYZ", []string{"withings"}, PatternManual, testTime); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload from disk with a fresh handle — this is the real round-trip.
	v2, err := Load(path, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Genesis state survived.
	core, ok := v2.Store().Principal(CorePrincipal)
	if !ok || core.Kind != KindGateway || core.Nonce == "" {
		t.Fatalf("core principal/nonce did not survive reload: %+v ok=%v", core, ok)
	}

	// The plugin receives exactly its granted secret, no denials.
	comp, err := v2.Store().Compose("withings")
	if err != nil {
		t.Fatalf("compose withings: %v", err)
	}
	if comp.Secrets["withings_api"] != "TOKEN-XYZ" {
		t.Fatalf("withings did not receive its secret: %v", comp.Secrets)
	}
	if len(comp.Denials) != 0 {
		t.Fatalf("unexpected denials: %v", comp.Denials)
	}

	// The admin token is never delivered, even to core.
	coreComp, err := v2.Store().Compose(CorePrincipal)
	if err != nil {
		t.Fatalf("compose core: %v", err)
	}
	if _, leaked := coreComp.Secrets[AdminTokenSecret]; leaked {
		t.Fatal("admin token leaked via Compose after reload")
	}
	if sec, _ := v2.Store().Secret(AdminTokenSecret); sec == nil || sec.Value != adminToken {
		t.Fatal("admin token did not survive reload intact")
	}

	// The reloaded store is internally consistent.
	if issues := v2.Store().Check(); len(issues) != 0 {
		t.Fatalf("reloaded store reports issues: %+v", issues)
	}
}

// TestRung1RevocationSurvivesReload proves a status change persists and that
// Compose's fail-closed denial behaviour holds after a round-trip.
func TestRung1RevocationSurvivesReload(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	v, _, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	_ = v.Store().RegisterPrincipal("withings", KindPlugin)
	_ = v.Store().SetSecret("withings_api", "TOKEN-XYZ", []string{"withings"}, PatternManual, testTime)
	// Simulate a revoke (the revoke op itself is a later rung); status must persist.
	v.Store().Secrets["withings_api"].Status = StatusRevoked
	if err := v.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	v2, err := Load(path, kr)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	comp, err := v2.Store().Compose("withings")
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if _, delivered := comp.Secrets["withings_api"]; delivered {
		t.Fatal("revoked secret delivered after reload")
	}
	if len(comp.Denials) != 1 || comp.Denials[0].Reason != DenialSecretRevoked {
		t.Fatalf("expected one secret_revoked denial after reload, got %v", comp.Denials)
	}
}
