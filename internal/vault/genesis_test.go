package vault

import (
	"path/filepath"
	"testing"
)

func TestInitCreatesGenesis(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")

	v, adminToken, err := Init(path, kr, testTime)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if adminToken == "" {
		t.Fatal("init returned an empty admin token")
	}

	// core principal: gateway, active, with a nonce.
	core, ok := v.Store().Principal(CorePrincipal)
	if !ok {
		t.Fatal("core principal missing after genesis")
	}
	if core.Kind != KindGateway || core.Status != StatusActive {
		t.Fatalf("core = %+v, want active gateway", core)
	}
	if core.Nonce == "" {
		t.Fatal("core has no fingerprint nonce")
	}

	// admin token stored, equal to the returned token, and NOT delivered to any
	// principal (no authorized_principals).
	sec, ok := v.Store().Secret(AdminTokenSecret)
	if !ok {
		t.Fatal("admin token secret missing")
	}
	if sec.Value != adminToken {
		t.Fatal("stored admin token does not match the returned token")
	}
	if len(sec.AuthorizedPrincipals) != 0 {
		t.Fatalf("admin token should authorize no principals, got %v", sec.AuthorizedPrincipals)
	}
	comp, err := v.Store().Compose(CorePrincipal)
	if err != nil {
		t.Fatalf("compose core: %v", err)
	}
	if _, leaked := comp.Secrets[AdminTokenSecret]; leaked {
		t.Fatal("admin token was delivered via Compose; it must stay API-internal")
	}

	// And the genesis blob reloads cleanly.
	if _, err := Load(path, kr); err != nil {
		t.Fatalf("genesis blob does not reload: %v", err)
	}
}

func TestInitRefusesClobber(t *testing.T) {
	kr := testKeyring(t)
	path := filepath.Join(t.TempDir(), "vault.age")
	if _, _, err := Init(path, kr, testTime); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if _, _, err := Init(path, kr, testTime); err == nil {
		t.Fatal("second init succeeded; must refuse to clobber an existing vault")
	}
}

func TestInitTokensAreRandom(t *testing.T) {
	kr := testKeyring(t)
	v1, t1, err := Init(filepath.Join(t.TempDir(), "v1.age"), kr, testTime)
	if err != nil {
		t.Fatalf("init 1: %v", err)
	}
	v2, t2, err := Init(filepath.Join(t.TempDir(), "v2.age"), kr, testTime)
	if err != nil {
		t.Fatalf("init 2: %v", err)
	}
	if t1 == t2 {
		t.Fatal("two genesis admin tokens are identical (not CSPRNG?)")
	}
	c1, _ := v1.Store().Principal(CorePrincipal)
	c2, _ := v2.Store().Principal(CorePrincipal)
	if c1.Nonce == c2.Nonce {
		t.Fatal("two genesis nonces are identical (not CSPRNG?)")
	}
}
