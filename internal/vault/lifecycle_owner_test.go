package vault

import (
	"slices"
	"testing"
)

func TestVaultRollManualUsesOperatorValue(t *testing.T) {
	v := savedVault(t)
	if err := v.SetSecret("api", "V0", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}

	if err := v.Roll("api", "V1", testTime); err != nil {
		t.Fatalf("roll: %v", err)
	}

	reloaded, err := Load(v.Path(), v.keyring)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	sec, _ := reloaded.Store().Secret("api")
	if sec.Value != "V1" || sec.RollCount != 1 {
		t.Fatalf("expected persisted V1/count1, got %q/%d", sec.Value, sec.RollCount)
	}
}

func TestVaultRollAutoMintsFreshValue(t *testing.T) {
	v := savedVault(t)
	if err := v.SetSecret("hmac", "OLD", []string{"mailer"}, PatternAuto, testTime); err != nil {
		t.Fatal(err)
	}

	// operatorValue is ignored for auto; the vault mints a CSPRNG value.
	if err := v.Roll("hmac", "", testTime); err != nil {
		t.Fatalf("roll: %v", err)
	}
	sec, _ := v.Store().Secret("hmac")
	if sec.Value == "OLD" || sec.Value == "" {
		t.Fatalf("auto roll must mint a fresh non-empty value, got %q", sec.Value)
	}
	if sec.RollCount != 1 {
		t.Fatalf("expected roll_count 1, got %d", sec.RollCount)
	}
}

func TestVaultRevokeGuardedPersists(t *testing.T) {
	v := savedVault(t)
	if err := v.SetSecret("api", "V0", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}

	if err := v.Revoke("api", testTime); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	reloaded, err := Load(v.Path(), v.keyring)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	sec, _ := reloaded.Store().Secret("api")
	if sec.Status != StatusRevoked {
		t.Fatalf("expected persisted revoked status, got %q", sec.Status)
	}
}

func TestVaultRevokePrincipalGuardedPersists(t *testing.T) {
	v := savedVault(t)
	if err := v.RevokePrincipal("mailer"); err != nil {
		t.Fatalf("revoke principal: %v", err)
	}
	reloaded, err := Load(v.Path(), v.keyring)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	p, _ := reloaded.Store().Principal("mailer")
	if p.Status != StatusRevoked {
		t.Fatalf("expected persisted revoked principal, got %q", p.Status)
	}
}

func TestVaultPurgePrincipalGuardedPersists(t *testing.T) {
	v := savedVault(t)
	if err := v.SetSecret("api", "V0", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}

	if err := v.PurgePrincipal("mailer"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	reloaded, err := Load(v.Path(), v.keyring)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := reloaded.Store().Principal("mailer"); ok {
		t.Fatal("purged principal must not survive a reload")
	}
	sec, _ := reloaded.Store().Secret("api")
	if slices.Contains(sec.AuthorizedPrincipals, "mailer") {
		t.Fatal("purge must strip the grant from the persisted secret")
	}
}

func TestVaultRegisterPrincipalGuardedPersistsThenGrant(t *testing.T) {
	v := savedVault(t)

	if err := v.RegisterPrincipal("worker", KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	reloaded, err := Load(v.Path(), v.keyring)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	p, ok := reloaded.Store().Principal("worker")
	if !ok || p.Kind != KindPlugin || p.Status != StatusActive {
		t.Fatalf("expected persisted active plugin principal, got %+v ok=%v", p, ok)
	}

	// Duplicate registration is refused.
	if err := v.RegisterPrincipal("worker", KindPlugin); err == nil {
		t.Error("duplicate registration must error")
	}
	// An invalid kind is refused.
	if err := v.RegisterPrincipal("bad", "nonsense"); err == nil {
		t.Error("invalid kind must error")
	}
	// Now a grant to the freshly registered principal succeeds (the gap #19 closes).
	if err := v.SetSecret("wsecret", "V", []string{"worker"}, PatternManual, testTime); err != nil {
		t.Errorf("grant after register should succeed, got %v", err)
	}
}

func TestVaultRollPrincipalRollsAutoSkipsManual(t *testing.T) {
	v := savedVault(t)
	for _, n := range []string{"a", "b"} {
		if err := v.SetSecret(n, n+"0", []string{"mailer"}, PatternAuto, testTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.SetSecret("m", "M0", []string{"mailer"}, PatternManual, testTime); err != nil {
		t.Fatal(err)
	}

	rolled, skipped, err := v.RollPrincipal("mailer", testTime)
	if err != nil {
		t.Fatalf("roll principal: %v", err)
	}
	slices.Sort(rolled)
	if !slices.Equal(rolled, []string{"a", "b"}) {
		t.Fatalf("expected auto secrets a,b rolled, got %v", rolled)
	}
	if !slices.Equal(skipped, []string{"m"}) {
		t.Fatalf("expected manual secret m skipped, got %v", skipped)
	}
	// Auto values changed; the manual value is untouched.
	a, _ := v.Store().Secret("a")
	if a.Value == "a0" {
		t.Error("auto secret 'a' should have a fresh value")
	}
	m, _ := v.Store().Secret("m")
	if m.Value != "M0" {
		t.Errorf("manual secret 'm' must be untouched, got %q", m.Value)
	}

	if _, _, err := v.RollPrincipal("ghost", testTime); err == nil {
		t.Error("roll principal on unknown principal must error")
	}
}
