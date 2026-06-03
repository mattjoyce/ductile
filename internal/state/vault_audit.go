package state

import (
	"context"
	"fmt"
	"time"
)

// VaultAuditEvent is one vault lifecycle fact to record. It carries the secret
// NAME and an outcome -- never a value. There is deliberately no Value field:
// the audit log is observability, not a second copy of the secret store.
type VaultAuditEvent struct {
	Op         string // register | set | roll | revoke | purge | dump_values | compose_denial
	Principal  string // principal name, or "" for secret-scoped ops with no principal
	SecretName string // secret name, or "" for principal-scoped ops
	Actor      string // who invoked: e.g. core-admin-token, cli, core
	Outcome    string // ok | denied | error (+ optional reason in Detail)
	Detail     string // non-secret context: denial reason, pattern, skipped count
}

// VaultAuditRow is a persisted audit fact read back from the log.
type VaultAuditRow struct {
	ID         int64
	Op         string
	Principal  string
	SecretName string
	Actor      string
	Outcome    string
	Detail     string
	CreatedAt  string
}

// AppendVaultAudit persists one append-only audit fact. Parameterized SQL only --
// no value is ever interpolated, and the event has no value field to leak.
//
// Fault model (Armstrong): like RecordStopwatch, this returns an error for the
// caller to log; the vault owner treats audit writes as best-effort and never
// rolls back a completed mutation because an audit row could not be written --
// the blob is already saved and cannot be un-saved. A lost audit row must be
// loud, never a crash.
func (s *Store) AppendVaultAudit(ctx context.Context, ev VaultAuditEvent) error {
	if ev.Op == "" {
		return fmt.Errorf("AppendVaultAudit: op is empty")
	}
	if ev.Outcome == "" {
		return fmt.Errorf("AppendVaultAudit: outcome is empty")
	}
	const q = `
		INSERT INTO vault_audit (op, principal, secret_name, actor, outcome, detail, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	if _, err := s.db.ExecContext(ctx, q,
		ev.Op,
		ev.Principal,
		ev.SecretName,
		ev.Actor,
		ev.Outcome,
		ev.Detail,
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("AppendVaultAudit: insert: %w", err)
	}
	return nil
}

// ListVaultAudit returns the most recent audit facts, newest first, capped at
// limit (limit <= 0 means a default of 100). A non-empty principal filters to
// that principal's facts (served by the vault_audit_principal_created index).
func (s *Store) ListVaultAudit(ctx context.Context, principal string, limit int) ([]VaultAuditRow, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `
		SELECT id, op, principal, secret_name, actor, outcome, detail, created_at
		FROM vault_audit`
	args := make([]any, 0, 2)
	if principal != "" {
		q += "\n\t\tWHERE principal = ?"
		args = append(args, principal)
	}
	q += "\n\t\tORDER BY id DESC\n\t\tLIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("ListVaultAudit: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []VaultAuditRow
	for rows.Next() {
		var r VaultAuditRow
		if err := rows.Scan(&r.ID, &r.Op, &r.Principal, &r.SecretName, &r.Actor, &r.Outcome, &r.Detail, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("ListVaultAudit: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListVaultAudit: rows: %w", err)
	}
	return out, nil
}
