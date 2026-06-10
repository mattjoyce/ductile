package storage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

var errSchemaValidation = errors.New("sqlite schema validation failed")

// OpenSQLite opens (and creates if needed) the SQLite database at path and
// ensures required tables exist.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := validateSQLiteFilesystem(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Basic health check + apply safe performance pragmas.
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(pctx, p); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply pragma %q: %w", p, err)
		}
	}

	// Restrict to 1 connection to avoid SQLITE_BUSY errors during concurrent writes.
	// This ensures only one writer at a time while busy_timeout handles waiting.
	db.SetMaxOpenConns(1)

	empty, err := isSQLiteEmpty(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("inspect sqlite schema state: %w", err)
	}

	if empty {
		if err := BootstrapSQLite(ctx, db); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}

	if err := ValidateSQLiteSchema(ctx, db); err != nil {
		_ = db.Close()
		if errors.Is(err, errSchemaValidation) {
			return nil, fmt.Errorf("%w; run scripts/migrate-hickey-sprint-7-plugin-facts.py if plugin_facts is missing, then scripts/migrate-hickey-sprint-9-plugin-fact-seq.py %s before deploy", err, path)
		}
		return nil, err
	}
	return db, nil
}

// BootstrapSQLite creates the full mono-schema for an empty database.
func BootstrapSQLite(ctx context.Context, db *sql.DB) error {
	// Execute the embedded mono-schema.
	// We split by semicolon to execute each statement separately.
	// Note: This is a simple parser that doesn't handle semicolons in strings/comments,
	// but for our controlled schema.sql it is sufficient.
	for _, stmt := range strings.Split(schemaSQL, ";") {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, trimmed); err != nil {
			return fmt.Errorf("bootstrap sqlite (schema.sql): %w", err)
		}
	}
	return ValidateSQLiteSchema(ctx, db)
}

// ValidateSQLiteSchema checks that an existing database matches the expected
// Ductile runtime shape. It does not mutate the schema.
func ValidateSQLiteSchema(ctx context.Context, db *sql.DB) error {
	requiredTables := []string{
		"storage_sequences",
		"job_queue",
		"job_transitions",
		"job_attempts",
		"config_snapshots",
		"plugin_state",
		"plugin_facts",
		"event_context",
		"job_log",
		"circuit_breakers",
		"circuit_breaker_transitions",
		"schedule_entries",
	}
	for _, table := range requiredTables {
		exists, err := sqliteObjectExists(ctx, db, "table", table)
		if err != nil {
			return fmt.Errorf("validate sqlite schema: inspect table %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("%w: missing table %s", errSchemaValidation, table)
		}
	}

	requiredColumns := []struct {
		table  string
		column string
	}{
		{"job_log", "result"},
		{"job_log", "job_id"},
		{"job_queue", "event_context_id"},
		{"job_log", "event_context_id"},
		{"schedule_entries", "last_success_job_id"},
		{"schedule_entries", "last_fired_at"},
		{"schedule_entries", "last_success_at"},
		{"schedule_entries", "next_run_at"},
		{"storage_sequences", "name"},
		{"storage_sequences", "value"},
		{"plugin_facts", "seq"},
		{"plugin_facts", "fact_type"},
		{"plugin_facts", "fact_json"},
		{"plugin_facts", "created_at"},
	}
	for _, req := range requiredColumns {
		hasColumn, err := sqliteColumnExists(ctx, db, req.table, req.column)
		if err != nil {
			return fmt.Errorf("validate sqlite schema: inspect column %s.%s: %w", req.table, req.column, err)
		}
		if !hasColumn {
			return fmt.Errorf("%w: missing column %s.%s", errSchemaValidation, req.table, req.column)
		}
	}

	requiredIndexes := []string{
		"job_log_job_id_attempt_idx",
		"job_log_completed_at_idx",
		"job_queue_dedupe_status_completed_idx",
		"job_transitions_created_at_idx",
		"job_attempts_created_at_idx",
		"circuit_breaker_transitions_created_at_idx",
		"plugin_facts_plugin_created_at_idx",
		"plugin_facts_plugin_type_created_at_idx",
		"plugin_facts_plugin_seq_idx",
		"plugin_facts_plugin_type_seq_idx",
	}
	for _, index := range requiredIndexes {
		exists, err := sqliteObjectExists(ctx, db, "index", index)
		if err != nil {
			return fmt.Errorf("validate sqlite schema: inspect index %s: %w", index, err)
		}
		if !exists {
			return fmt.Errorf("%w: missing index %s", errSchemaValidation, index)
		}
	}

	return nil
}

func isSQLiteEmpty(ctx context.Context, db *sql.DB) (bool, error) {
	row := db.QueryRowContext(ctx, `
SELECT 1
FROM sqlite_master
WHERE type = 'table'
  AND name NOT LIKE 'sqlite_%'
LIMIT 1;
`)
	var marker int
	err := row.Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func sqliteColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	cols, err := db.QueryContext(ctx, "SELECT cid, name, type, \"notnull\", dflt_value, pk FROM pragma_table_info(?);", table)
	if err != nil {
		return false, err
	}
	defer func() { _ = cols.Close() }()

	for cols.Next() {
		// cid, name, type, notnull, dflt_value, pk
		var (
			cid       int
			name      string
			typ       string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := cols.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	if err := cols.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func sqliteObjectExists(ctx context.Context, db *sql.DB, objectType, name string) (bool, error) {
	row := db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type = ? AND name = ? LIMIT 1;", objectType, name)
	var marker int
	err := row.Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
