## 2026-06-26 - Add G204 Exception Comment
**Vulnerability:** gosec G204 (CWE-78): Subprocess launched with variable warning.
**Learning:** The warning in `internal/dispatch/sidedoor_audit_unix.go` was a false positive because the variable used was safely checked via `exec.LookPath("sudo")` and its arguments are securely passed as separate arguments to `exec.CommandContext`, preventing injection.
**Prevention:** Explicitly ignore such specific false positive cases using `// #nosec G204` along with an explanation to improve the clarity for static code analysis tools while keeping real vulnerabilities visible.
