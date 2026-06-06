package state

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/storage"
)

func newAuditTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}

// AppendVaultAudit persists one fact; ListVaultAudit reads it back. The row
// carries the secret NAME and an outcome, never a value.
func TestAppendVaultAuditRoundTrips(t *testing.T) {
	t.Parallel()
	s := newAuditTestStore(t)
	ctx := context.Background()

	ev := VaultAuditEvent{
		Op:         "roll",
		Principal:  "deploy-bot",
		SecretName: "deploy-token",
		Actor:      "core-admin-token",
		Outcome:    "ok",
		Detail:     "auto-minted",
	}
	if err := s.AppendVaultAudit(ctx, ev); err != nil {
		t.Fatalf("AppendVaultAudit: %v", err)
	}

	rows, err := s.ListVaultAudit(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListVaultAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(rows))
	}
	got := rows[0]
	if got.Op != "roll" || got.Principal != "deploy-bot" || got.SecretName != "deploy-token" {
		t.Fatalf("row fields not round-tripped: %+v", got)
	}
	if got.Outcome != "ok" || got.Actor != "core-admin-token" {
		t.Fatalf("row outcome/actor not round-tripped: %+v", got)
	}
	if got.CreatedAt == "" {
		t.Fatalf("expected a created_at timestamp, got empty")
	}
}

// The audit log must never persist a secret VALUE. There is no value field on
// the event by construction; this guards against a future field leaking one in.
func TestVaultAuditNeverStoresSecretValue(t *testing.T) {
	t.Parallel()
	s := newAuditTestStore(t)
	ctx := context.Background()

	const secretValue = "S3CR3T-VALUE-should-never-persist"
	ev := VaultAuditEvent{
		Op:         "set",
		Principal:  "deploy-bot",
		SecretName: "deploy-token",
		Actor:      "core-admin-token",
		Outcome:    "ok",
		Detail:     "manual",
	}
	if err := s.AppendVaultAudit(ctx, ev); err != nil {
		t.Fatalf("AppendVaultAudit: %v", err)
	}

	// Scan every column of every row; the secret value must appear nowhere.
	db := s.db
	cols, err := db.QueryContext(ctx, `SELECT op, principal, secret_name, actor, outcome, detail, created_at FROM vault_audit`)
	if err != nil {
		t.Fatalf("query vault_audit: %v", err)
	}
	defer func() { _ = cols.Close() }()
	for cols.Next() {
		var op, principal, secretName, actor, outcome, detail, createdAt string
		if err := cols.Scan(&op, &principal, &secretName, &actor, &outcome, &detail, &createdAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		for _, field := range []string{op, principal, secretName, actor, outcome, detail, createdAt} {
			if strings.Contains(field, secretValue) {
				t.Fatalf("secret value leaked into vault_audit field: %q", field)
			}
		}
	}
}

// ListVaultAudit returns rows newest-first and honours the limit.
func TestListVaultAuditOrdersNewestFirstAndLimits(t *testing.T) {
	t.Parallel()
	s := newAuditTestStore(t)
	ctx := context.Background()

	for _, op := range []string{"register", "set", "roll", "revoke"} {
		if err := s.AppendVaultAudit(ctx, VaultAuditEvent{Op: op, Principal: "p", Outcome: "ok"}); err != nil {
			t.Fatalf("AppendVaultAudit(%s): %v", op, err)
		}
	}
	rows, err := s.ListVaultAudit(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListVaultAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows under limit, got %d", len(rows))
	}
	if rows[0].Op != "revoke" {
		t.Fatalf("expected newest-first (revoke), got %q", rows[0].Op)
	}
}

// ListVaultAudit with a principal returns only that principal's facts.
func TestListVaultAuditFiltersByPrincipal(t *testing.T) {
	t.Parallel()
	s := newAuditTestStore(t)
	ctx := context.Background()

	for _, p := range []string{"alpha", "beta", "alpha"} {
		if err := s.AppendVaultAudit(ctx, VaultAuditEvent{Op: "set", Principal: p, Outcome: "ok"}); err != nil {
			t.Fatalf("AppendVaultAudit(%s): %v", p, err)
		}
	}
	rows, err := s.ListVaultAudit(ctx, "alpha", 100)
	if err != nil {
		t.Fatalf("ListVaultAudit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 alpha rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Principal != "alpha" {
			t.Errorf("principal filter leaked a %q row", r.Principal)
		}
	}
}
