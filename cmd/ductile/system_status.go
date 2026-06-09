package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/storage"
)

type systemStatusCheck struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Path      string `json:"path,omitempty"`
	Detail    string `json:"detail,omitempty"`
	ActivePID int    `json:"active_pid,omitempty"`
}

type systemStatusReport struct {
	Healthy bool                `json:"healthy"`
	Checks  []systemStatusCheck `json:"checks"`
}

func runSystemStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	jsonOut := fs.Bool("json", false, "Output status as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	report := collectSystemStatus(*configPath)

	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render JSON status: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
	} else {
		renderSystemStatusHuman(report)
	}

	if report.Healthy {
		return 0
	}
	return 1
}

func collectSystemStatus(configPath string) systemStatusReport {
	report := systemStatusReport{
		Healthy: true,
		Checks:  make([]systemStatusCheck, 0, 4),
	}

	resolvedConfigPath := configPath
	if resolvedConfigPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			report.Checks = append(report.Checks,
				systemStatusCheck{
					Name:   "config_discovery",
					OK:     false,
					Detail: err.Error(),
				},
				systemStatusCheck{
					Name:   "config_load",
					OK:     false,
					Detail: "skipped: config discovery failed",
				},
				systemStatusCheck{
					Name:   "state_db",
					OK:     false,
					Detail: "skipped: config discovery failed",
				},
				systemStatusCheck{
					Name:   "pid_lock",
					OK:     false,
					Detail: "skipped: config discovery failed",
				},
			)
			report.Healthy = false
			return report
		}
		resolvedConfigPath = discovered
	}

	report.Checks = append(report.Checks, systemStatusCheck{
		Name:   "config_discovery",
		OK:     true,
		Path:   resolvedConfigPath,
		Detail: "config path resolved",
	})

	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		report.Checks = append(report.Checks,
			systemStatusCheck{
				Name:   "config_load",
				OK:     false,
				Path:   resolvedConfigPath,
				Detail: err.Error(),
			},
			systemStatusCheck{
				Name:   "state_db",
				OK:     false,
				Detail: "skipped: config load failed",
			},
			systemStatusCheck{
				Name:   "pid_lock",
				OK:     false,
				Detail: "skipped: config load failed",
			},
		)
		report.Healthy = false
		return report
	}

	report.Checks = append(report.Checks, systemStatusCheck{
		Name:   "config_load",
		OK:     true,
		Path:   resolvedConfigPath,
		Detail: "configuration loaded",
	})

	stateDBCheck := checkStateDBReadiness(cfg.State.Path)
	if !stateDBCheck.OK {
		report.Healthy = false
	}
	report.Checks = append(report.Checks, stateDBCheck)

	pidLockCheck := checkPIDLockState(getPIDLockPath(cfg))
	if !pidLockCheck.OK {
		report.Healthy = false
	}
	report.Checks = append(report.Checks, pidLockCheck)

	// Informational: the live gateway boot posture (#130). Never flips Healthy —
	// the management-only posture is intentional, not a fault.
	report.Checks = append(report.Checks, checkBootPosture(cfg))

	return report
}

func checkStateDBReadiness(statePath string) systemStatusCheck {
	check := systemStatusCheck{
		Name: "state_db",
		Path: statePath,
	}

	if statePath == "" {
		check.OK = false
		check.Detail = "state path is empty"
		return check
	}

	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		check.OK = true
		check.Detail = "database file does not exist yet (will be created on start)"
		return check
	}

	dsn := fmt.Sprintf("file:%s?mode=ro", filepath.ToSlash(statePath))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		check.OK = false
		check.Detail = fmt.Sprintf("open failed: %v", err)
		return check
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		check.OK = false
		check.Detail = fmt.Sprintf("ping failed: %v", err)
		return check
	}

	check.OK = true
	check.Detail = "database is readable"
	return check
}

func checkPIDLockState(lockPath string) systemStatusCheck {
	check := systemStatusCheck{
		Name: "pid_lock",
		Path: lockPath,
	}

	// #nosec G304 -- lock path is operator-controlled local input.
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			check.OK = true
			check.Detail = "no active PID lock file"
			return check
		}
		check.OK = false
		check.Detail = fmt.Sprintf("failed to read lock file: %v", err)
		return check
	}

	line := strings.TrimSpace(string(data))
	if line == "" {
		check.OK = true
		check.Detail = "lock file present but empty (not active)"
		return check
	}

	pid, err := strconv.Atoi(line)
	if err != nil || pid <= 0 {
		check.OK = false
		check.Detail = "lock file contains invalid pid"
		return check
	}

	if processExists(pid) {
		check.OK = false
		check.ActivePID = pid
		check.Detail = fmt.Sprintf("another instance appears active (pid %d)", pid)
		return check
	}

	check.OK = true
	check.Detail = fmt.Sprintf("stale lock file detected (pid %d not running)", pid)
	return check
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func renderSystemStatusHuman(report systemStatusReport) {
	state := "HEALTHY"
	if !report.Healthy {
		state = "DEGRADED"
	}
	fmt.Printf("System Status: %s\n", state)
	for _, c := range report.Checks {
		status := "OK"
		if !c.OK {
			status = "FAIL"
		}
		fmt.Printf("- %s: %s", c.Name, status)
		if c.Path != "" {
			fmt.Printf(" (path=%s)", c.Path)
		}
		if c.Detail != "" {
			fmt.Printf(" - %s", c.Detail)
		}
		fmt.Println()
	}
}

// runSystemSelfcheck runs pre-deploy/post-migration health checks. Stricter
// than `system status`: it opens the database directly, runs PRAGMA
// integrity_check, validates the embedded schema shape, and reports invariants
// operators care about during a migration window.
func runSystemSelfcheck(args []string) int {
	fs := flag.NewFlagSet("selfcheck", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	jsonOut := fs.Bool("json", false, "Output report as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	report := collectSystemSelfcheck(*configPath)

	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render JSON selfcheck: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
	} else {
		renderSystemSelfcheckHuman(report)
	}

	if report.Healthy {
		return 0
	}
	return 1
}

func collectSystemSelfcheck(configPath string) systemStatusReport {
	report := systemStatusReport{
		Healthy: true,
		Checks:  make([]systemStatusCheck, 0, 6),
	}

	resolvedConfigPath := configPath
	if resolvedConfigPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			report.Healthy = false
			report.Checks = append(report.Checks, systemStatusCheck{
				Name:   "config_discovery",
				OK:     false,
				Detail: err.Error(),
			})
			return report
		}
		resolvedConfigPath = discovered
	}
	report.Checks = append(report.Checks, systemStatusCheck{
		Name:   "config_discovery",
		OK:     true,
		Path:   resolvedConfigPath,
		Detail: "config path resolved",
	})

	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		report.Healthy = false
		report.Checks = append(report.Checks, systemStatusCheck{
			Name:   "config_load",
			OK:     false,
			Path:   resolvedConfigPath,
			Detail: err.Error(),
		})
		return report
	}
	report.Checks = append(report.Checks, systemStatusCheck{
		Name:   "config_load",
		OK:     true,
		Path:   resolvedConfigPath,
		Detail: "configuration loaded",
	})

	pidLockCheck := checkPIDLockState(getPIDLockPath(cfg))
	if !pidLockCheck.OK {
		report.Healthy = false
	}
	report.Checks = append(report.Checks, pidLockCheck)

	statePath := cfg.State.Path
	if statePath == "" {
		report.Healthy = false
		report.Checks = append(report.Checks, systemStatusCheck{
			Name:   "db_open",
			OK:     false,
			Detail: "state path is empty",
		})
		return report
	}

	// PRAGMA integrity_check on a live WAL is unsafe — refuse if a gateway
	// is holding the PID lock with an active process.
	if !pidLockCheck.OK && pidLockCheck.ActivePID != 0 {
		report.Checks = append(report.Checks,
			systemStatusCheck{
				Name:   "db_integrity",
				OK:     false,
				Detail: "skipped: active gateway holds PID lock — quiesce before selfcheck",
			},
			systemStatusCheck{
				Name:   "db_schema",
				OK:     false,
				Detail: "skipped: active gateway holds PID lock",
			},
			systemStatusCheck{
				Name:   "queue_terminal_freshness",
				OK:     false,
				Detail: "skipped: active gateway holds PID lock",
			},
		)
		return report
	}

	if _, err := os.Stat(statePath); os.IsNotExist(err) {
		report.Healthy = false
		report.Checks = append(report.Checks, systemStatusCheck{
			Name:   "db_open",
			OK:     false,
			Path:   statePath,
			Detail: "database file does not exist; start the gateway once before selfcheck",
		})
		return report
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := sql.Open("sqlite", statePath)
	if err != nil {
		report.Healthy = false
		report.Checks = append(report.Checks, systemStatusCheck{
			Name:   "db_open",
			OK:     false,
			Path:   statePath,
			Detail: fmt.Sprintf("open failed: %v", err),
		})
		return report
	}
	defer func() { _ = db.Close() }()

	integrityCheck := checkDBIntegrity(ctx, db, statePath)
	if !integrityCheck.OK {
		report.Healthy = false
	}
	report.Checks = append(report.Checks, integrityCheck)

	schemaCheck := checkDBSchema(ctx, db, statePath)
	if !schemaCheck.OK {
		report.Healthy = false
	}
	report.Checks = append(report.Checks, schemaCheck)

	freshness := checkQueueTerminalFreshness(ctx, db, statePath)
	if !freshness.OK {
		report.Healthy = false
	}
	report.Checks = append(report.Checks, freshness)

	return report
}

func checkDBIntegrity(ctx context.Context, db *sql.DB, statePath string) systemStatusCheck {
	check := systemStatusCheck{
		Name: "db_integrity",
		Path: statePath,
	}
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check;")
	if err != nil {
		check.OK = false
		check.Detail = fmt.Sprintf("integrity_check failed: %v", err)
		return check
	}
	defer func() { _ = rows.Close() }()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			check.OK = false
			check.Detail = fmt.Sprintf("scan integrity_check row: %v", err)
			return check
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		check.OK = false
		check.Detail = fmt.Sprintf("integrity_check rows: %v", err)
		return check
	}
	if len(lines) == 1 && lines[0] == "ok" {
		check.OK = true
		check.Detail = "PRAGMA integrity_check returned ok"
		return check
	}
	check.OK = false
	check.Detail = fmt.Sprintf("integrity_check reported issues: %s", strings.Join(lines, "; "))
	return check
}

func checkDBSchema(ctx context.Context, db *sql.DB, statePath string) systemStatusCheck {
	check := systemStatusCheck{
		Name: "db_schema",
		Path: statePath,
	}
	if err := storage.ValidateSQLiteSchema(ctx, db); err != nil {
		check.OK = false
		check.Detail = err.Error()
		return check
	}
	check.OK = true
	check.Detail = "all required tables, columns, and indexes present"
	return check
}

// checkQueueTerminalFreshness reports whether terminal-state rows are accumulating
// in job_queue. Wave-1 (pre-pruneJobQueue): tolerates up to 5000 stale rows so
// existing dirty databases don't fail selfcheck before the pruner ships.
// Wave-2 (post-pruneJobQueue): tighten to fail on any non-zero count.
const queueTerminalFreshnessFailThreshold = 5000

func checkQueueTerminalFreshness(ctx context.Context, db *sql.DB, statePath string) systemStatusCheck {
	check := systemStatusCheck{
		Name: "queue_terminal_freshness",
		Path: statePath,
	}
	// Predicate must match queue.PruneJobQueue's terminal-status set so the
	// selfcheck invariant and the pruner agree on what "stale terminal" means.
	const q = `
SELECT COUNT(*) FROM job_queue
WHERE status IN ('succeeded','skipped','failed','timed_out','dead')
  AND completed_at IS NOT NULL
  AND completed_at < datetime('now','-2 days');
`
	var count int
	if err := db.QueryRowContext(ctx, q).Scan(&count); err != nil {
		check.OK = false
		check.Detail = fmt.Sprintf("query failed: %v", err)
		return check
	}
	switch {
	case count == 0:
		check.OK = true
		check.Detail = "no stale terminal-state rows in job_queue"
	case count <= queueTerminalFreshnessFailThreshold:
		check.OK = true
		check.Detail = fmt.Sprintf("%d stale terminal-state rows in job_queue (within Wave-1 tolerance; ship pruneJobQueue to clear)", count)
	default:
		check.OK = false
		check.Detail = fmt.Sprintf("%d stale terminal-state rows exceeds threshold %d; pruneJobQueue likely missing or stuck", count, queueTerminalFreshnessFailThreshold)
	}
	return check
}

func renderSystemSelfcheckHuman(report systemStatusReport) {
	state := "PASS"
	if !report.Healthy {
		state = "FAIL"
	}
	fmt.Printf("Selfcheck: %s\n", state)
	for _, c := range report.Checks {
		status := "OK"
		if !c.OK {
			status = "FAIL"
		}
		fmt.Printf("- %s: %s", c.Name, status)
		if c.Path != "" {
			fmt.Printf(" (path=%s)", c.Path)
		}
		if c.Detail != "" {
			fmt.Printf(" - %s", c.Detail)
		}
		fmt.Println()
	}
}
