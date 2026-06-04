package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
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

// TestGetSecretValueRefusesReserved — `vault get` must never print the reserved
// management-API credential, even though the local key-holder could otherwise
// read any entry (branch-review N3, card 42).
func TestGetSecretValueRefusesReserved(t *testing.T) {
	s := vault.NewStore()
	s.SeedCorePrincipal("")
	if err := s.RotateAdminToken("super-secret-admin", time.Now()); err != nil {
		t.Fatalf("seed admin token: %v", err)
	}
	if _, err := getSecretValue(s, vault.AdminTokenSecret); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved read must be refused naming 'reserved'; got %v", err)
	}
}

// TestAuditVaultReadNoStateDBIsNoOp — auditing a read is best-effort and must not
// bootstrap a state DB as a side effect when none exists yet.
func TestAuditVaultReadNoStateDBIsNoOp(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.db")
	cfg := &config.Config{}
	cfg.State.Path = statePath

	auditVaultRead(cfg, "api_key", "ok", "test") // must not panic or create the DB

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("auditVaultRead must not create the state DB on a read; stat err=%v", err)
	}
}

// TestAuditVaultReadAppendsWhenStateDBExists — when a state DB is present, a read
// appends a name-only, value-free audit row.
func TestAuditVaultReadAppendsWhenStateDBExists(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.db")
	ctx := context.Background()

	db, err := storage.OpenSQLite(ctx, statePath) // bootstraps the schema
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	_ = db.Close()

	cfg := &config.Config{}
	cfg.State.Path = statePath
	auditVaultRead(cfg, "api_key", "ok", "local key-holder read")

	db2, err := storage.OpenSQLite(ctx, statePath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	defer func() { _ = db2.Close() }()
	rows, err := state.NewStore(db2).ListVaultAudit(ctx, "", 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Op == "read" && r.SecretName == "api_key" && r.Outcome == "ok" && r.Actor == "cli" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a read/ok audit row for api_key, got %+v", rows)
	}
}
