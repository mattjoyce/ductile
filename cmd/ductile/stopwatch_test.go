package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/storage"
)

// stopwatchE2EBuildOnce builds the ductile binary once per test
// invocation. The CLI surface is the contract under test; using `go
// run` per case would multiply build cost across 5+ subtests.
func stopwatchE2EBuildOnce(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ductile")
	cmd := exec.Command("go", "build", "-o", bin, "./")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// stopwatchE2EConfig writes a minimal config + an empty SQLite DB
// (which BootstrapSQLite will populate on first open) and returns the
// config path.
func stopwatchE2EConfig(t *testing.T) (configPath, dbPath string) {
	t.Helper()
	dir := t.TempDir()
	dbPath = filepath.Join(dir, "test.db")
	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.Mkdir(pluginsDir, 0o755); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	configPath = filepath.Join(dir, "config.yaml")
	yamlBody := "" +
		"service:\n" +
		"  name: test\n" +
		"  tick_interval: 60s\n" +
		"  log_level: info\n" +
		"  max_workers: 1\n" +
		"state:\n" +
		"  path: " + dbPath + "\n" +
		"plugin_roots:\n" +
		"  - " + pluginsDir + "\n" +
		"plugins: {}\n"
	if err := os.WriteFile(configPath, []byte(yamlBody), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, dbPath
}

// seedStopwatchRow inserts one row with a specified recorded_at and
// subs_json. Uses raw SQL via the same driver the binary uses, so the
// seeded rows match what RecordStopwatch would write.
func seedStopwatchRow(t *testing.T, dbPath, plugin, status string, recordedAt time.Time, subsJSON string) {
	t.Helper()
	// Use the storage package directly to ensure schema is bootstrapped.
	// Importing it would create a cycle; instead, invoke a tiny helper
	// via a separate process - or simpler, just ensure the dir exists
	// and let the first `stopwatch prune --dry-run` open the DB.
	//
	// Practically: the CLI itself bootstraps via storage.OpenSQLite.
	// We can use sqlite3 directly here through database/sql with the
	// same driver the binary uses (modernc.org/sqlite).
	openAndExec(t, dbPath, []string{
		`INSERT INTO job_stopwatch (
			job_id, plugin, pipeline, step_id, pipeline_instance_id,
			attempt, enter_wall_ns, exit_wall_ns, dur_ns,
			runtime_pre_ns, runtime_post_ns, status, subs_json, recorded_at
		) VALUES (
			'` + plugin + recordedAt.Format("150405.000000000") + `',
			'` + plugin + `', 'p', 's', 'inst',
			1, ` + i64(recordedAt.UnixNano()) + `, ` + i64(recordedAt.UnixNano()+100) + `, 100,
			0, 0, '` + status + `', '` + subsJSON + `', '` + recordedAt.UTC().Format(time.RFC3339Nano) + `'
		)`,
	})
}

func i64(n int64) string { return formatInt(n) }

// formatInt avoids strconv import in a busy test file.
func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// openAndExec opens dbPath via storage.OpenSQLite (which bootstraps the
// schema on a fresh DB) and runs each statement.
func openAndExec(t *testing.T, dbPath string, statements []string) {
	t.Helper()
	db := openSQLiteRaw(t, dbPath)
	defer func() { _ = db.Close() }()
	for _, stmt := range statements {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

func openSQLiteRaw(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := storage.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite(%s): %v", dbPath, err)
	}
	return db
}

func TestStopwatchCLI_PruneAndPruneSubs_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds the binary")
	}
	bin := stopwatchE2EBuildOnce(t)
	configPath, dbPath := stopwatchE2EConfig(t)

	// First invocation: dry-run with no rows. This also bootstraps the
	// schema (storage.OpenSQLite creates tables on first open).
	out := runCLIBinary(t, bin, "stopwatch", "prune", "--config", configPath, "--older-than", "1d", "--dry-run", "--json")
	var summary map[string]any
	if err := json.Unmarshal([]byte(out), &summary); err != nil {
		t.Fatalf("dry-run on empty DB did not produce JSON: %v\nout: %s", err, out)
	}
	if got := summary["affected"]; got != float64(0) {
		t.Errorf("empty-DB dry-run affected = %v, want 0", got)
	}

	// Seed rows: 3 old (eligible for prune), 1 recent.
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	recent := time.Now().UTC()
	seedStopwatchRow(t, dbPath, "fetch", "ok", old, `[{"name":"fetch.http_get","dur_ns":100}]`)
	seedStopwatchRow(t, dbPath, "fetch", "ok", old.Add(time.Second), `[{"name":"fetch.body_read","dur_ns":50}]`)
	seedStopwatchRow(t, dbPath, "other", "err", old.Add(2*time.Second), "[]")
	seedStopwatchRow(t, dbPath, "fetch", "ok", recent, `[{"name":"fetch.http_get","dur_ns":99}]`)

	// Dry-run prune --older-than 14d should report 3 affected.
	out = runCLIBinary(t, bin, "stopwatch", "prune", "--config", configPath, "--older-than", "14d", "--dry-run", "--json")
	_ = json.Unmarshal([]byte(out), &summary)
	if got := summary["affected"]; got != float64(3) {
		t.Errorf("dry-run prune affected = %v, want 3 (3 old rows)\nout: %s", got, out)
	}
	if got := summary["dry_run"]; got != true {
		t.Errorf("dry_run flag missing: %v", got)
	}

	// Dry-run with --plugin filter narrows to fetch only.
	out = runCLIBinary(t, bin, "stopwatch", "prune", "--config", configPath, "--older-than", "14d", "--plugin", "fetch", "--dry-run", "--json")
	_ = json.Unmarshal([]byte(out), &summary)
	if got := summary["affected"]; got != float64(2) {
		t.Errorf("dry-run prune --plugin fetch affected = %v, want 2", got)
	}

	// Actual prune-subs by span name (no delete, just edit subs_json).
	out = runCLIBinary(t, bin, "stopwatch", "prune-subs", "--config", configPath, "--older-than", "14d", "--plugin", "fetch", "--span", "fetch.body_read", "--json")
	_ = json.Unmarshal([]byte(out), &summary)
	if got := summary["affected"]; got != float64(1) {
		t.Errorf("prune-subs by name affected = %v, want 1\nout: %s", got, out)
	}

	// Verify the row that had body_read still exists but has cleared subs.
	db := openSQLiteRaw(t, dbPath)
	defer func() { _ = db.Close() }()
	var subs string
	if err := db.QueryRowContext(context.Background(),
		`SELECT subs_json FROM job_stopwatch WHERE plugin='fetch' AND recorded_at = ?`,
		old.Add(time.Second).Format(time.RFC3339Nano),
	).Scan(&subs); err != nil {
		t.Fatalf("read subs_json: %v", err)
	}
	if strings.Contains(subs, "body_read") {
		t.Errorf("body_read sub-span still present after prune-subs: %s", subs)
	}

	// Actual prune --older-than 14d --plugin fetch should delete 2 fetch rows.
	out = runCLIBinary(t, bin, "stopwatch", "prune", "--config", configPath, "--older-than", "14d", "--plugin", "fetch", "--json")
	_ = json.Unmarshal([]byte(out), &summary)
	if got := summary["affected"]; got != float64(2) {
		t.Errorf("real prune --plugin fetch affected = %v, want 2", got)
	}

	// Recent fetch row + the 'other' row remain.
	var remaining int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM job_stopwatch`,
	).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 2 {
		t.Errorf("remaining rows = %d, want 2 (recent fetch + 'other')", remaining)
	}
}

func TestStopwatchCLI_MissingOlderThanFails(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds the binary")
	}
	bin := stopwatchE2EBuildOnce(t)
	configPath, _ := stopwatchE2EConfig(t)

	cmd := exec.Command(bin, "stopwatch", "prune", "--config", configPath)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit with no --older-than; got success\nout: %s", out)
	}
	if !strings.Contains(string(out), "older-than") {
		t.Errorf("error message should mention --older-than, got: %s", out)
	}
}

func TestStopwatchCLI_InvalidOlderThanFails(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds the binary")
	}
	bin := stopwatchE2EBuildOnce(t)
	configPath, _ := stopwatchE2EConfig(t)

	cmd := exec.Command(bin, "stopwatch", "prune", "--config", configPath, "--older-than", "garbage")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit with invalid --older-than\nout: %s", out)
	}
	if !strings.Contains(string(out), "older-than") {
		t.Errorf("error message should mention --older-than, got: %s", out)
	}
}

func runCLIBinary(t *testing.T, bin string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cli %v: %v\nout: %s", args, err, out)
	}
	return string(out)
}
