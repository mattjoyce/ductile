package main

import (
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/vault"
)

// TestGetSecretValue covers the read policy of `vault get` without touching disk:
// active → value; unknown / revoked / not-yet-minted → an explanatory error.
func TestGetSecretValue(t *testing.T) {
	s := vault.NewStore()
	if err := s.RegisterPrincipal("mailer", vault.KindPlugin); err != nil {
		t.Fatalf("register: %v", err)
	}
	now := time.Now()
	if err := s.SetSecret("api_key", "shh", []string{"mailer"}, vault.PatternManual, now); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if v, err := getSecretValue(s, "api_key"); err != nil || v != "shh" {
		t.Fatalf("active read = %q, %v; want shh, nil", v, err)
	}

	if _, err := getSecretValue(s, "nope"); err == nil {
		t.Fatal("unknown secret should error")
	}

	sec, _ := s.Secret("api_key")
	sec.Status = vault.StatusRevoked
	sec.Value = "" // revoke clears the value
	if _, err := getSecretValue(s, "api_key"); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked read should error naming 'revoked'; got %v", err)
	}

	if err := s.SetSecret("auto_key", "", []string{"mailer"}, vault.PatternAuto, now); err != nil {
		t.Fatalf("auto setup: %v", err)
	}
	if _, err := getSecretValue(s, "auto_key"); err == nil || !strings.Contains(err.Error(), "no value") {
		t.Fatalf("empty auto read should error naming 'no value'; got %v", err)
	}
}
