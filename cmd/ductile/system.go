package main

import (
	"fmt"
	"os"
)

func runSystemNoun(args []string) int {
	if len(args) < 1 {
		printSystemNounHelp(os.Stderr)
		return 1
	}
	if isHelpToken(args[0]) {
		printSystemNounHelp(os.Stdout)
		return 0
	}

	action := args[0]
	actionArgs := args[1:]

	// There is deliberately no "stop" (or "restart") action: the service
	// manager owns the daemon's lifecycle (systemctl / launchctl / Ctrl-C in
	// the foreground), and the gateway is designed to be supervised, not
	// self-managing (let-it-crash; see docs/reload_rca.md). `reload` is the
	// in-process operation; everything below stop is the supervisor's job.
	switch action {
	case "stop", "restart":
		fmt.Fprintf(os.Stderr, "ductile system %s does not exist — the service manager owns the daemon lifecycle.\n", action)
		fmt.Fprintln(os.Stderr, "Use: systemctl --user stop <unit> | launchctl bootout gui/$UID/<label> | Ctrl-C (foreground).")
		fmt.Fprintln(os.Stderr, "For an in-process config swap use: ductile system reload")
		return 1
	case "start":
		if hasHelpFlag(actionArgs) {
			printSystemStartHelp()
			return 0
		}
		return runStart(actionArgs)
	case "status":
		if hasHelpFlag(actionArgs) {
			printSystemStatusHelp()
			return 0
		}
		return runSystemStatus(actionArgs)
	case "plugin-facts":
		if hasHelpFlag(actionArgs) {
			printSystemPluginFactsHelp()
			return 0
		}
		return runSystemPluginFacts(actionArgs)
	case "vault-audit":
		if hasHelpFlag(actionArgs) {
			printSystemVaultAuditHelp()
			return 0
		}
		return runSystemVaultAudit(actionArgs)
	case "reset":
		if hasHelpFlag(actionArgs) {
			printSystemResetHelp()
			return 0
		}
		return runSystemReset(actionArgs)
	case "breaker":
		if hasHelpFlag(actionArgs) {
			printSystemBreakerHelp()
			return 0
		}
		return runSystemBreaker(actionArgs)
	case "scheduler":
		if hasHelpFlag(actionArgs) {
			printSystemSchedulerHelp()
			return 0
		}
		return runSystemScheduler(actionArgs)
	case "reload":
		if hasHelpFlag(actionArgs) {
			printSystemReloadHelp()
			return 0
		}
		return runSystemReload(actionArgs)
	case "selfcheck":
		if hasHelpFlag(actionArgs) {
			printSystemSelfcheckHelp()
			return 0
		}
		return runSystemSelfcheck(actionArgs)
	case "backup":
		if hasHelpFlag(actionArgs) {
			printSystemBackupHelp()
			return 0
		}
		return runSystemBackup(actionArgs)
	case "skills":
		return runSystemSkills(actionArgs)
	case "help":
		printSystemNounHelp(os.Stdout)
		return 0
	default:
		// #nosec G705 -- stderr output is plain text, not HTML.
		fmt.Fprintf(os.Stderr, "Unknown system action: %s\n", action)
		return 1
	}
}

func printSystemNounHelp(w *os.File) {
	_, _ = fmt.Fprintln(w, "Usage: ductile system <action>")
	_, _ = fmt.Fprintln(w, "Actions: start, status, plugin-facts, vault-audit, breaker, scheduler, reset, reload, selfcheck, backup, skills")
	_, _ = fmt.Fprintln(w, "(no stop/restart: the service manager owns the daemon lifecycle)")
}

func printSystemStartHelp() {
	fmt.Println("Usage: ductile system start [--config PATH]")
	fmt.Println("Start the gateway service in the foreground.")
}

func printSystemStatusHelp() {
	fmt.Println("Usage: ductile system status [--config PATH] [--json]")
	fmt.Println("Show global gateway health (config, database readiness, and PID lock state).")
	fmt.Println("")
	fmt.Println("Exit codes:")
	fmt.Println("  0  All required checks passed")
	fmt.Println("  1  One or more checks failed")
}

func printSystemResetHelp() {
	fmt.Println("Usage: ductile system reset <plugin> [--config PATH]")
	fmt.Println("Reset scheduler poll circuit breaker state for a plugin.")
}

func printSystemBreakerHelp() {
	fmt.Println("Usage: ductile system breaker <plugin> [--command COMMAND] [--config PATH] [--json] [--limit N]")
	fmt.Println("Show current circuit breaker state and recent transition history.")
}

func printSystemSchedulerHelp() {
	fmt.Println("Usage: ductile system scheduler [--config PATH] [--json]")
	fmt.Println("Show scheduler-submitted poll jobs currently queued or running.")
	fmt.Println("Reads job_queue, the canonical store the scheduler consults to")
	fmt.Println("decide whether a plugin is already polling.")
}

func printSystemPluginFactsHelp() {
	fmt.Println("Usage: ductile system plugin-facts <plugin> [--fact-type TYPE] [--config PATH] [--json] [--limit N]")
	fmt.Println("Show recent append-only plugin facts and their recorded JSON payloads.")
}

func printSystemVaultAuditHelp() {
	fmt.Println("Usage: ductile system vault-audit [--principal NAME] [--config PATH] [--json] [--limit N]")
	fmt.Println("Show the append-only vault audit log (register/set/roll/revoke/purge + fail-closed")
	fmt.Println("compose denials), newest first. Records secret NAMES and outcomes only — never values.")
}

func printSystemReloadHelp() {
	fmt.Println("Usage: ductile system reload [--config PATH] [--api-url URL] [--api-key TOKEN] [--json]")
	fmt.Println("Reload configuration in a running gateway (SIGHUP or API).")
}

func printSystemSelfcheckHelp() {
	fmt.Println("Usage: ductile system selfcheck [--config PATH] [--json]")
	fmt.Println("Run pre-deploy/post-migration health checks on the local state database.")
	fmt.Println("Intended as the staged-deployment gate (Dev → Test → Prod promotion).")
	fmt.Println("Performs: PRAGMA integrity_check, schema validation, queue invariants.")
	fmt.Println("Refuses to run while a gateway holds the PID lock — quiesce first.")
	fmt.Println("")
	fmt.Println("Exit codes:")
	fmt.Println("  0  All checks passed")
	fmt.Println("  1  One or more checks failed")
}
