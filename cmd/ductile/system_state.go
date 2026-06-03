package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mattjoyce/ductile/internal/api"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/log"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

func runSystemReset(actionArgs []string) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	if err := fs.Parse(actionArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: ductile system reset <plugin> [--config PATH]")
		return 1
	}
	pluginName := strings.TrimSpace(fs.Arg(0))
	if pluginName == "" {
		fmt.Fprintln(os.Stderr, "plugin name is required")
		return 1
	}

	if *configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to discover config: %v\n", err)
			return 1
		}
		*configPath = discovered
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}
	if _, ok := cfg.Plugins[pluginName]; !ok {
		fmt.Fprintf(os.Stderr, "Unknown plugin: %s\n", pluginName)
		return 1
	}

	db, err := storage.OpenSQLite(context.Background(), cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	q := queue.New(db)
	if err := q.ResetCircuitBreaker(context.Background(), pluginName, "poll"); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to reset circuit breaker: %v\n", err)
		return 1
	}

	fmt.Printf("Reset circuit breaker for %s (poll)\n", pluginName)
	return 0
}

type systemBreakerTransitionReport struct {
	ID           string  `json:"id"`
	FromState    *string `json:"from_state,omitempty"`
	ToState      string  `json:"to_state"`
	FailureCount int     `json:"failure_count"`
	Reason       string  `json:"reason"`
	JobID        *string `json:"job_id,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

type systemBreakerReport struct {
	Plugin        string                          `json:"plugin"`
	Command       string                          `json:"command"`
	State         *string                         `json:"state,omitempty"`
	FailureCount  int                             `json:"failure_count"`
	OpenedAt      *string                         `json:"opened_at,omitempty"`
	LastFailureAt *string                         `json:"last_failure_at,omitempty"`
	LastJobID     *string                         `json:"last_job_id,omitempty"`
	UpdatedAt     *string                         `json:"updated_at,omitempty"`
	Transitions   []systemBreakerTransitionReport `json:"transitions"`
}

type systemPluginFactRow struct {
	ID        string          `json:"id"`
	Plugin    string          `json:"plugin"`
	FactType  string          `json:"fact_type"`
	JobID     string          `json:"job_id"`
	Command   string          `json:"command"`
	Fact      json.RawMessage `json:"fact"`
	CreatedAt string          `json:"created_at"`
}

type systemPluginFactsReport struct {
	Plugin   string                `json:"plugin"`
	FactType string                `json:"fact_type,omitempty"`
	Facts    []systemPluginFactRow `json:"facts"`
}

func runSystemBreaker(actionArgs []string) int {
	fs := flag.NewFlagSet("breaker", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	command := fs.String("command", "poll", "Plugin command")
	jsonOut := fs.Bool("json", false, "Output breaker report as JSON")
	limit := fs.Int("limit", 20, "Maximum transition rows to show")
	if err := fs.Parse(actionArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: ductile system breaker <plugin> [--command COMMAND] [--config PATH] [--json] [--limit N]")
		return 1
	}
	pluginName := strings.TrimSpace(fs.Arg(0))
	if pluginName == "" {
		fmt.Fprintln(os.Stderr, "plugin name is required")
		return 1
	}
	commandName := strings.TrimSpace(*command)
	if commandName == "" {
		fmt.Fprintln(os.Stderr, "command is required")
		return 1
	}

	if *configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to discover config: %v\n", err)
			return 1
		}
		*configPath = discovered
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}
	if _, ok := cfg.Plugins[pluginName]; !ok {
		fmt.Fprintf(os.Stderr, "Unknown plugin: %s\n", pluginName)
		return 1
	}

	db, err := storage.OpenSQLite(context.Background(), cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	q := queue.New(db)
	breaker, err := q.GetCircuitBreaker(context.Background(), pluginName, commandName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load circuit breaker: %v\n", err)
		return 1
	}
	transitions, err := q.ListCircuitBreakerTransitions(context.Background(), pluginName, commandName, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load circuit breaker transitions: %v\n", err)
		return 1
	}

	report := buildSystemBreakerReport(pluginName, commandName, breaker, transitions)
	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render JSON breaker report: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	renderSystemBreakerHuman(report)
	return 0
}

func buildSystemBreakerReport(pluginName, commandName string, breaker *queue.CircuitBreaker, transitions []queue.CircuitBreakerTransition) systemBreakerReport {
	report := systemBreakerReport{
		Plugin:      pluginName,
		Command:     commandName,
		Transitions: make([]systemBreakerTransitionReport, 0, len(transitions)),
	}
	if breaker != nil {
		state := string(breaker.State)
		report.State = &state
		report.FailureCount = breaker.FailureCount
		if breaker.OpenedAt != nil {
			openedAt := breaker.OpenedAt.Format(time.RFC3339Nano)
			report.OpenedAt = &openedAt
		}
		if breaker.LastFailure != nil {
			lastFailure := breaker.LastFailure.Format(time.RFC3339Nano)
			report.LastFailureAt = &lastFailure
		}
		report.LastJobID = breaker.LastJobID
		if !breaker.UpdatedAt.IsZero() {
			updatedAt := breaker.UpdatedAt.Format(time.RFC3339Nano)
			report.UpdatedAt = &updatedAt
		}
	}
	for _, transition := range transitions {
		row := systemBreakerTransitionReport{
			ID:           transition.ID,
			ToState:      string(transition.ToState),
			FailureCount: transition.FailureCount,
			Reason:       string(transition.Reason),
			JobID:        transition.JobID,
			CreatedAt:    transition.CreatedAt.Format(time.RFC3339Nano),
		}
		if transition.FromState != nil {
			fromState := string(*transition.FromState)
			row.FromState = &fromState
		}
		report.Transitions = append(report.Transitions, row)
	}
	return report
}

func renderSystemBreakerHuman(report systemBreakerReport) {
	fmt.Printf("Circuit breaker for %s (%s)\n", report.Plugin, report.Command)
	if report.State == nil {
		fmt.Println("State: no current row")
	} else {
		fmt.Printf("State: %s (failure_count=%d)\n", *report.State, report.FailureCount)
		if report.OpenedAt != nil {
			fmt.Printf("Opened at: %s\n", *report.OpenedAt)
		}
		if report.LastFailureAt != nil {
			fmt.Printf("Last failure: %s\n", *report.LastFailureAt)
		}
		if report.LastJobID != nil {
			fmt.Printf("Last job: %s\n", *report.LastJobID)
		}
	}
	fmt.Println("Transitions:")
	if len(report.Transitions) == 0 {
		fmt.Println("- none")
		return
	}
	for _, transition := range report.Transitions {
		from := "<none>"
		if transition.FromState != nil {
			from = *transition.FromState
		}
		job := ""
		if transition.JobID != nil {
			job = fmt.Sprintf(" job=%s", *transition.JobID)
		}
		fmt.Printf("- %s %s -> %s reason=%s failure_count=%d%s\n",
			transition.CreatedAt, from, transition.ToState, transition.Reason, transition.FailureCount, job)
	}
}

type systemSchedulerActivePoll struct {
	JobID     string  `json:"job_id"`
	Plugin    string  `json:"plugin"`
	DedupeKey *string `json:"dedupe_key,omitempty"`
	Status    string  `json:"status"`
	Attempt   int     `json:"attempt"`
	CreatedAt string  `json:"created_at"`
	StartedAt *string `json:"started_at,omitempty"`
}

type systemSchedulerReport struct {
	ServiceName string                      `json:"service_name"`
	Count       int                         `json:"count"`
	ActivePolls []systemSchedulerActivePoll `json:"active_polls"`
}

func runSystemScheduler(actionArgs []string) int {
	fs := flag.NewFlagSet("scheduler", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	jsonOut := fs.Bool("json", false, "Output scheduler report as JSON")
	if err := fs.Parse(actionArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Usage: ductile system scheduler [--config PATH] [--json]")
		return 1
	}

	if *configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to discover config: %v\n", err)
			return 1
		}
		*configPath = discovered
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}
	submittedBy := strings.TrimSpace(cfg.Service.Name)
	if submittedBy == "" {
		fmt.Fprintln(os.Stderr, "service.name is empty; cannot identify scheduler-submitted jobs")
		return 1
	}

	db, err := storage.OpenSQLite(context.Background(), cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	q := queue.New(db)
	polls, err := q.ListSchedulerActivePolls(context.Background(), submittedBy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list scheduler active polls: %v\n", err)
		return 1
	}

	report := buildSystemSchedulerReport(submittedBy, polls)
	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render JSON scheduler report: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}
	renderSystemSchedulerHuman(report)
	return 0
}

func buildSystemSchedulerReport(submittedBy string, polls []*queue.SchedulerActivePoll) systemSchedulerReport {
	report := systemSchedulerReport{
		ServiceName: submittedBy,
		Count:       len(polls),
		ActivePolls: make([]systemSchedulerActivePoll, 0, len(polls)),
	}
	for _, poll := range polls {
		row := systemSchedulerActivePoll{
			JobID:     poll.JobID,
			Plugin:    poll.Plugin,
			DedupeKey: poll.DedupeKey,
			Status:    string(poll.Status),
			Attempt:   poll.Attempt,
			CreatedAt: poll.CreatedAt.Format(time.RFC3339Nano),
		}
		if poll.StartedAt != nil {
			startedAt := poll.StartedAt.Format(time.RFC3339Nano)
			row.StartedAt = &startedAt
		}
		report.ActivePolls = append(report.ActivePolls, row)
	}
	return report
}

func renderSystemSchedulerHuman(report systemSchedulerReport) {
	fmt.Printf("Scheduler: %s\n", report.ServiceName)
	fmt.Printf("Active polls: %d\n", report.Count)
	if report.Count == 0 {
		fmt.Println("(none)")
		return
	}
	fmt.Println("")
	for _, poll := range report.ActivePolls {
		started := "-"
		if poll.StartedAt != nil {
			started = *poll.StartedAt
		}
		dedupe := "-"
		if poll.DedupeKey != nil {
			dedupe = *poll.DedupeKey
		}
		fmt.Printf("- plugin=%s status=%s attempt=%d created=%s started=%s job=%s dedupe=%s\n",
			poll.Plugin, poll.Status, poll.Attempt, poll.CreatedAt, started, poll.JobID, dedupe)
	}
}

func runSystemPluginFacts(actionArgs []string) int {
	fs := flag.NewFlagSet("plugin-facts", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	factType := fs.String("fact-type", "", "Fact type filter")
	jsonOut := fs.Bool("json", false, "Output plugin facts as JSON")
	limit := fs.Int("limit", 20, "Maximum fact rows to show")
	if err := fs.Parse(actionArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: ductile system plugin-facts <plugin> [--fact-type TYPE] [--config PATH] [--json] [--limit N]")
		return 1
	}
	pluginName := strings.TrimSpace(fs.Arg(0))
	if pluginName == "" {
		fmt.Fprintln(os.Stderr, "plugin name is required")
		return 1
	}
	if *limit <= 0 {
		fmt.Fprintln(os.Stderr, "limit must be greater than zero")
		return 1
	}

	cfg, err := loadConfigForTool(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}
	if _, ok := cfg.Plugins[pluginName]; !ok {
		fmt.Fprintf(os.Stderr, "Unknown plugin: %s\n", pluginName)
		return 1
	}

	db, err := storage.OpenSQLite(context.Background(), cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	st := state.NewStore(db)
	facts, err := st.ListFacts(context.Background(), pluginName, strings.TrimSpace(*factType), *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load plugin facts: %v\n", err)
		return 1
	}

	report := buildSystemPluginFactsReport(pluginName, strings.TrimSpace(*factType), facts)
	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render JSON plugin facts: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	renderSystemPluginFactsHuman(report)
	return 0
}

func buildSystemPluginFactsReport(pluginName, factType string, facts []state.PluginFact) systemPluginFactsReport {
	report := systemPluginFactsReport{
		Plugin:   pluginName,
		FactType: factType,
		Facts:    make([]systemPluginFactRow, 0, len(facts)),
	}
	for _, fact := range facts {
		report.Facts = append(report.Facts, systemPluginFactRow{
			ID:        fact.ID,
			Plugin:    fact.PluginName,
			FactType:  fact.FactType,
			JobID:     fact.JobID,
			Command:   fact.Command,
			Fact:      fact.FactJSON,
			CreatedAt: fact.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return report
}

func renderSystemPluginFactsHuman(report systemPluginFactsReport) {
	fmt.Printf("Plugin facts for %s\n", report.Plugin)
	if report.FactType != "" {
		fmt.Printf("Fact type filter: %s\n", report.FactType)
	}
	if len(report.Facts) == 0 {
		fmt.Println("No facts found.")
		return
	}

	for _, fact := range report.Facts {
		fmt.Printf("\n%s  %s  job=%s  command=%s\n", fact.CreatedAt, fact.FactType, fact.JobID, fact.Command)
		fmt.Println(string(fact.Fact))
	}
}

type systemVaultAuditRow struct {
	ID         int64  `json:"id"`
	Op         string `json:"op"`
	Principal  string `json:"principal,omitempty"`
	SecretName string `json:"secret_name,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Outcome    string `json:"outcome"`
	Detail     string `json:"detail,omitempty"`
	CreatedAt  string `json:"created_at"`
}

type systemVaultAuditReport struct {
	Principal string                `json:"principal,omitempty"`
	Rows      []systemVaultAuditRow `json:"rows"`
}

// runSystemVaultAudit prints the append-only vault_audit fact log
// (registrations, sets, rolls, revokes, purges, and fail-closed compose
// denials), newest first. It is the read-only inspector for the vault_audit
// table, mirroring `system plugin-facts` — values are never stored or shown.
func runSystemVaultAudit(actionArgs []string) int {
	fs := flag.NewFlagSet("vault-audit", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	principal := fs.String("principal", "", "Filter to one principal")
	jsonOut := fs.Bool("json", false, "Output audit facts as JSON")
	limit := fs.Int("limit", 50, "Maximum audit rows to show")
	if err := fs.Parse(actionArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}
	if *limit <= 0 {
		fmt.Fprintln(os.Stderr, "limit must be greater than zero")
		return 1
	}

	cfg, err := loadConfigForTool(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}

	db, err := storage.OpenSQLite(context.Background(), cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open database: %v\n", err)
		return 1
	}
	defer func() { _ = db.Close() }()

	st := state.NewStore(db)
	rows, err := st.ListVaultAudit(context.Background(), strings.TrimSpace(*principal), *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load vault audit facts: %v\n", err)
		return 1
	}

	report := buildSystemVaultAuditReport(strings.TrimSpace(*principal), rows)
	if *jsonOut {
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to render JSON vault audit: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
		return 0
	}

	renderSystemVaultAuditHuman(report)
	return 0
}

func buildSystemVaultAuditReport(principal string, rows []state.VaultAuditRow) systemVaultAuditReport {
	report := systemVaultAuditReport{
		Principal: principal,
		Rows:      make([]systemVaultAuditRow, 0, len(rows)),
	}
	for _, r := range rows {
		report.Rows = append(report.Rows, systemVaultAuditRow{
			ID:         r.ID,
			Op:         r.Op,
			Principal:  r.Principal,
			SecretName: r.SecretName,
			Actor:      r.Actor,
			Outcome:    r.Outcome,
			Detail:     r.Detail,
			CreatedAt:  r.CreatedAt,
		})
	}
	return report
}

func renderSystemVaultAuditHuman(report systemVaultAuditReport) {
	fmt.Println("Vault audit facts (newest first)")
	if report.Principal != "" {
		fmt.Printf("Principal filter: %s\n", report.Principal)
	}
	if len(report.Rows) == 0 {
		fmt.Println("No audit facts found.")
		return
	}
	for _, r := range report.Rows {
		target := r.Principal
		if r.SecretName != "" {
			if target != "" {
				target += " "
			}
			target += "secret=" + r.SecretName
		}
		fmt.Printf("\n%s  %s  %s  actor=%s  -> %s\n", r.CreatedAt, r.Op, target, r.Actor, r.Outcome)
		if r.Detail != "" {
			fmt.Printf("  %s\n", r.Detail)
		}
	}
}

func runSystemReload(actionArgs []string) int {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	apiURL := fs.String("api-url", "", "API base URL (optional)")
	apiKey := fs.String("api-key", "", "API key (optional)")
	jsonOut := fs.Bool("json", false, "Output JSON response")
	if err := fs.Parse(actionArgs); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if *apiURL != "" {
		key := strings.TrimSpace(*apiKey)
		if key == "" {
			key = strings.TrimSpace(os.Getenv("DUCTILE_API_KEY"))
		}
		if key == "" {
			fmt.Fprintln(os.Stderr, "API key required for reload (set --api-key or DUCTILE_API_KEY)")
			return 1
		}
		endpoint := strings.TrimRight(*apiURL, "/") + "/system/reload"
		req, err := http.NewRequest(http.MethodPost, endpoint, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to build request: %v\n", err)
			return 1
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Reload request failed: %v\n", err)
			return 1
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintf(os.Stderr, "Reload failed (%d): %s\n", resp.StatusCode, strings.TrimSpace(string(body)))
			return 1
		}
		if *jsonOut {
			fmt.Println(string(body))
			return 0
		}
		var result api.ReloadResponse
		if err := json.Unmarshal(body, &result); err == nil && result.Status != "" {
			fmt.Printf("Reloaded at %s\n", result.ReloadedAt)
			if result.Message != "" {
				fmt.Printf("%s\n", result.Message)
			}
			return 0
		}
		fmt.Println(string(body))
		return 0
	}

	if *configPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to discover config: %v\n", err)
			return 1
		}
		*configPath = discovered
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}
	pidPath := getPIDLockPath(cfg)
	pid, err := readPIDFromFile(pidPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read PID: %v\n", err)
		return 1
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to signal PID %d: %v\n", pid, err)
		return 1
	}
	if *jsonOut {
		resp := api.ReloadResponse{Status: "ok", Message: fmt.Sprintf("SIGHUP sent to %d", pid)}
		raw, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Println(string(raw))
		return 0
	}
	fmt.Printf("Reload signal sent to PID %d\n", pid)
	return 0
}

func runSystemSkills(args []string) int {
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	explicitConfig := *configPath != ""

	// Try to auto-discover config if not specified; failure is non-fatal.
	if *configPath == "" {
		if discovered, err := config.DiscoverConfigDir(); err == nil {
			*configPath = discovered
		}
	}

	// Attempt to load config and registry.
	var registry *plugin.Registry
	var loadedConfig *config.Config
	hasConfig := false
	if *configPath != "" {
		cfg, err := config.Load(*configPath)
		if err != nil {
			if explicitConfig {
				fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
				return 1
			}
			// Auto-discovered config failed to load — fall through to core mode.
		} else {
			if r, err := discoverRegistry(cfg, *configPath); err == nil {
				registry = r
				loadedConfig = cfg
				hasConfig = true
			}
		}
	}

	if !hasConfig {
		fmt.Print(skillsCoreMode)
		return 0
	}

	// Full manifest output.
	fmt.Println("# Ductile Gateway: LLM Operator Skill Manifest")
	fmt.Println()
	fmt.Print(skillsCLICommands)
	fmt.Println()

	// --- Plugin Skills ---
	plugins := registry.All()
	var pNames []string
	for name := range plugins {
		pNames = append(pNames, name)
	}
	sort.Strings(pNames)

	fmt.Println("## Plugins")
	fmt.Println()
	fmt.Println("Format: `<plugin>.<command> m=<HTTP> p=<path> tier=<READ|WRITE> mut=<0|1> idem=<0|1> retry=<0|1> [in=<schema>] [out=<schema>] [d=<desc>]`")
	fmt.Println()

	for _, name := range pNames {
		p := plugins[name]
		fmt.Printf("### %s\n", p.Name)
		if p.Description != "" {
			fmt.Println()
			fmt.Println(p.Description)
		}
		fmt.Println()
		for _, cmd := range p.Commands {
			mutatesState := cmd.Type == plugin.CommandTypeWrite
			idempotent := !mutatesState
			if cmd.Idempotent != nil {
				idempotent = *cmd.Idempotent
			}
			retrySafe := !mutatesState
			if cmd.RetrySafe != nil {
				retrySafe = *cmd.RetrySafe
			}
			tier := "READ"
			if mutatesState {
				tier = "WRITE"
			}
			fmt.Printf("- %s.%s m=POST p=/plugin/%s/%s tier=%s mut=%d idem=%d retry=%d",
				p.Name, cmd.Name, p.Name, cmd.Name, tier, boolToInt(mutatesState), boolToInt(idempotent), boolToInt(retrySafe))
			if s := renderSchema(cmd.InputSchema); s != "" {
				fmt.Printf(" in=%q", s)
			}
			if s := renderSchema(cmd.OutputSchema); s != "" {
				fmt.Printf(" out=%q", s)
			}
			if cmd.Description != "" {
				fmt.Printf(" d=%q", cmd.Description)
			}
			fmt.Println()
		}
		fmt.Println()
	}

	// --- Pipeline Skills ---
	if loadedConfig != nil {
		pipelineFiles := make([]string, 0, len(loadedConfig.SourceFiles))
		for f := range loadedConfig.SourceFiles {
			pipelineFiles = append(pipelineFiles, f)
		}
		sort.Strings(pipelineFiles)

		routerEngine, err := router.LoadFromConfigFiles(pipelineFiles, registry, log.WithComponent("skills-export"))
		if err == nil {
			if r, ok := routerEngine.(*router.Router); ok {
				pipelines := r.PipelineSummary()
				if len(pipelines) > 0 {
					sort.Slice(pipelines, func(i, j int) bool {
						return pipelines[i].Name < pipelines[j].Name
					})
					fmt.Println("## Pipelines")
					fmt.Println()
					fmt.Println("Format: `<pipeline> m=<HTTP> p=<path> trigger=<trigger> mode=<sync|async> [timeout=<duration>]`")
					fmt.Println()
					for _, p := range pipelines {
						mode := "async"
						if p.ExecutionMode == "synchronous" {
							mode = "sync"
						}
						fmt.Printf("- %s m=POST p=/pipeline/%s trigger=%q mode=%s", p.Name, p.Name, p.Trigger, mode)
						if p.Timeout > 0 {
							fmt.Printf(" timeout=%s", p.Timeout)
						}
						fmt.Println()
					}
				}
			}
		}
	}

	fmt.Println("---")
	fmt.Println()
	fmt.Println("**Next steps:** Use `job inspect <id>` to trace any execution. Use `system status` to verify health.")

	return 0
}

// renderSchema formats a plugin command's raw schema for manifest display.
// Compact map {prop: "type"} → "{key: type, ...}" (sorted keys).
// Full JSON schema (has "type" key) → compact JSON.
// nil → "" (omit field).
func renderSchema(schema any) string {
	if schema == nil {
		return ""
	}
	m, ok := schema.(map[string]any)
	if !ok {
		b, err := json.Marshal(schema)
		if err != nil {
			return ""
		}
		return string(b)
	}
	// Full JSON schema: has a "type" key at the top level.
	if _, hasType := m["type"]; hasType {
		b, err := json.Marshal(schema)
		if err != nil {
			return ""
		}
		return string(b)
	}
	// Compact map {prop: "type"} — render sorted.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
