package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
)

// runStopwatchNoun is the dispatcher for `ductile stopwatch <action>`.
// Honors the 1st-class / 2nd-class distinction: `prune` drops parent
// rows (which take their sub-spans with them); `prune-subs` keeps the
// row + supervisor timing but clears the plugin-supplied sub-spans.
func runStopwatchNoun(args []string) int {
	if len(args) < 1 {
		printStopwatchHelp(os.Stderr)
		return 1
	}
	if isHelpToken(args[0]) {
		printStopwatchHelp(os.Stdout)
		return 0
	}
	action := args[0]
	actionArgs := args[1:]
	switch action {
	case "prune":
		if hasHelpFlag(actionArgs) {
			printStopwatchPruneHelp()
			return 0
		}
		return runStopwatchPrune(actionArgs)
	case "prune-subs":
		if hasHelpFlag(actionArgs) {
			printStopwatchPruneSubsHelp()
			return 0
		}
		return runStopwatchPruneSubs(actionArgs)
	case "help":
		printStopwatchHelp(os.Stdout)
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown stopwatch action: %s\n", action)
		printStopwatchHelp(os.Stderr)
		return 1
	}
}

func printStopwatchHelp(w *os.File) {
	_, _ = fmt.Fprintln(w, "Usage: ductile stopwatch <action>")
	_, _ = fmt.Fprintln(w, "Actions:")
	_, _ = fmt.Fprintln(w, "  prune       Delete whole job_stopwatch rows (1st-class spans + their sub-spans).")
	_, _ = fmt.Fprintln(w, "  prune-subs  Clear sub-spans on matching rows (keeps the row + supervisor timing).")
}

func printStopwatchPruneHelp() {
	fmt.Println(`Usage: ductile stopwatch prune --older-than <duration> [filters] [options]

Deletes whole job_stopwatch rows matching the filters. The deleted rows
take their sub-spans with them (sub-spans live in the subs_json column
ON the row).

Required:
  --older-than DUR   Only rows with recorded_at older than now-DUR are
                     touched. Supports Go durations (e.g. 24h, 720h)
                     plus 'd'/'w' suffixes (7d, 2w).

Filters (AND-combined, all optional):
  --plugin NAME      Only rows where plugin = NAME.
  --step NAME        Only rows where step_id = NAME.
  --status STATE     Only rows where status = STATE (ok, err, timeout, capture_error).

Options:
  --config PATH      Path to configuration (defaults to auto-discovery).
  --dry-run          Print the count of matching rows; do not delete.
  --limit N          Bound the number of rows deleted in one call (default 5000).
  --json             Emit a JSON summary instead of human-readable text.

Examples:
  ductile stopwatch prune --older-than 14d
  ductile stopwatch prune --older-than 7d --plugin folder_watch
  ductile stopwatch prune --older-than 30d --status err --dry-run`)
}

func printStopwatchPruneSubsHelp() {
	fmt.Println(`Usage: ductile stopwatch prune-subs --older-than <duration> [filters] [options]

Clears the subs_json field on matching job_stopwatch rows without
touching any other field. The supervisor's authoritative timing
(dur_ns, status, recorded_at, ...) is preserved; only the plugin-
supplied sub-span breakdown is removed.

Required:
  --older-than DUR   Only rows with recorded_at older than now-DUR are
                     touched. Supports Go durations plus 'd'/'w' suffixes.

Filters (AND-combined, all optional):
  --plugin NAME      Only rows where plugin = NAME.
  --step NAME        Only rows where step_id = NAME.
  --status STATE     Only rows where status = STATE.
  --span NAME        Remove only sub-spans whose "name" field equals NAME
                     (rest of subs_json stays). Omit to clear ALL sub-spans.

Options:
  --config PATH      Path to configuration.
  --dry-run          Print the count of rows that would be touched; do not modify.
  --limit N          Bound the number of rows modified in one call (default 5000).
  --json             Emit a JSON summary instead of human-readable text.

Examples:
  ductile stopwatch prune-subs --older-than 7d
  ductile stopwatch prune-subs --older-than 7d --plugin fetch --span fetch.body_read
  ductile stopwatch prune-subs --older-than 30d --plugin folder_watch --dry-run`)
}

// stopwatchPruneFlags is the shared flag surface for both prune and
// prune-subs. Pulled into a helper because the filter fields and
// boilerplate are identical; only the action and the extra --span
// flag differ.
type stopwatchPruneFlags struct {
	configPath string
	olderThan  string
	plugin     string
	step       string
	status     string
	span       string // used only by prune-subs
	dryRun     bool
	limit      int
	jsonOut    bool
}

// register attaches all common flags. registerSpan controls whether
// --span is also bound (only true for prune-subs).
func (f *stopwatchPruneFlags) register(fs *flag.FlagSet, registerSpan bool) {
	fs.StringVar(&f.configPath, "config", "", "Path to configuration")
	fs.StringVar(&f.olderThan, "older-than", "", "Required: rows older than now-DUR (e.g. 14d, 24h, 2w)")
	fs.StringVar(&f.plugin, "plugin", "", "Filter by plugin name")
	fs.StringVar(&f.step, "step", "", "Filter by step_id")
	fs.StringVar(&f.status, "status", "", "Filter by status (ok|err|timeout|capture_error)")
	if registerSpan {
		fs.StringVar(&f.span, "span", "", "Filter to sub-spans with name=SPAN (omit to clear all)")
	}
	fs.BoolVar(&f.dryRun, "dry-run", false, "Print count of affected rows; do not modify")
	fs.IntVar(&f.limit, "limit", 5000, "Max rows touched per invocation")
	fs.BoolVar(&f.jsonOut, "json", false, "Emit JSON summary")
}

// validate returns the resolved cutoff and any user-visible error.
func (f *stopwatchPruneFlags) cutoff() (time.Time, error) {
	if strings.TrimSpace(f.olderThan) == "" {
		return time.Time{}, fmt.Errorf("--older-than is required (e.g. 14d, 24h)")
	}
	dur, err := config.ParseInterval(f.olderThan)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid --older-than %q: %w", f.olderThan, err)
	}
	if dur <= 0 {
		return time.Time{}, fmt.Errorf("--older-than must be positive: %s", f.olderThan)
	}
	return time.Now().UTC().Add(-dur), nil
}

func openStopwatchStore(configPath string) (*state.Store, func(), error) {
	resolvedPath := configPath
	if resolvedPath == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			return nil, nil, fmt.Errorf("discover config: %w", err)
		}
		resolvedPath = discovered
	}
	cfg, err := config.Load(resolvedPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	db, err := storage.OpenSQLite(context.Background(), cfg.State.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("open database: %w", err)
	}
	return state.NewStore(db), func() { _ = db.Close() }, nil
}

func runStopwatchPrune(args []string) int {
	flags := &stopwatchPruneFlags{}
	fs := flag.NewFlagSet("stopwatch prune", flag.ContinueOnError)
	flags.register(fs, false)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	cutoff, err := flags.cutoff()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	store, closeFn, err := openStopwatchStore(flags.configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeFn()

	filter := state.StopwatchPruneFilter{
		OlderThan: cutoff,
		Plugin:    strings.TrimSpace(flags.plugin),
		StepID:    strings.TrimSpace(flags.step),
		Status:    strings.TrimSpace(flags.status),
	}

	ctx := context.Background()
	if flags.dryRun {
		n, err := store.CountStopwatchRowsMatching(ctx, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "count: %v\n", err)
			return 1
		}
		emitPruneSummary(flags.jsonOut, "prune", true, n, filter, "", cutoff)
		return 0
	}

	deleted, err := store.PruneStopwatchRows(ctx, filter, flags.limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prune: %v\n", err)
		return 1
	}
	emitPruneSummary(flags.jsonOut, "prune", false, deleted, filter, "", cutoff)
	return 0
}

func runStopwatchPruneSubs(args []string) int {
	flags := &stopwatchPruneFlags{}
	fs := flag.NewFlagSet("stopwatch prune-subs", flag.ContinueOnError)
	flags.register(fs, true)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	cutoff, err := flags.cutoff()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	store, closeFn, err := openStopwatchStore(flags.configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeFn()

	filter := state.StopwatchPruneFilter{
		OlderThan: cutoff,
		Plugin:    strings.TrimSpace(flags.plugin),
		StepID:    strings.TrimSpace(flags.step),
		Status:    strings.TrimSpace(flags.status),
	}
	spanName := strings.TrimSpace(flags.span)

	ctx := context.Background()
	if flags.dryRun {
		n, err := store.CountStopwatchSubsMatching(ctx, filter, spanName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "count: %v\n", err)
			return 1
		}
		emitPruneSummary(flags.jsonOut, "prune-subs", true, n, filter, spanName, cutoff)
		return 0
	}

	touched, err := store.ClearStopwatchSubs(ctx, filter, spanName, flags.limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prune-subs: %v\n", err)
		return 1
	}
	emitPruneSummary(flags.jsonOut, "prune-subs", false, touched, filter, spanName, cutoff)
	return 0
}

// emitPruneSummary prints a uniform result for either prune action.
// dryRun toggles "would" vs "did"; spanName is only set for prune-subs.
func emitPruneSummary(jsonOut bool, action string, dryRun bool, n int, filter state.StopwatchPruneFilter, spanName string, cutoff time.Time) {
	if jsonOut {
		payload := map[string]any{
			"action":     action,
			"dry_run":    dryRun,
			"affected":   n,
			"older_than": cutoff.Format(time.RFC3339Nano),
			"filters": map[string]string{
				"plugin": filter.Plugin,
				"step":   filter.StepID,
				"status": filter.Status,
				"span":   spanName,
			},
		}
		_ = json.NewEncoder(os.Stdout).Encode(payload)
		return
	}
	verb := "deleted"
	if action == "prune-subs" {
		verb = "cleared subs on"
	}
	if dryRun {
		verb = "would " + verb
	}
	desc := describeFilter(filter, spanName)
	fmt.Printf("%s %d job_stopwatch row(s) older than %s%s\n", verb, n, cutoff.Format(time.RFC3339), desc)
}

func describeFilter(filter state.StopwatchPruneFilter, spanName string) string {
	parts := []string{}
	if filter.Plugin != "" {
		parts = append(parts, "plugin="+filter.Plugin)
	}
	if filter.StepID != "" {
		parts = append(parts, "step="+filter.StepID)
	}
	if filter.Status != "" {
		parts = append(parts, "status="+filter.Status)
	}
	if spanName != "" {
		parts = append(parts, "span="+spanName)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}
