## 2026-06-12 - Fix CWE-78 Subprocess launched with variable in Sidedoor Audit
**Vulnerability:** The `exec.CommandContext` was being invoked with a variable (`sudo`) instead of a constant string as the executable name in `internal/dispatch/sidedoor_audit_unix.go`. This triggers CWE-78 (G204) warnings as launching a subprocess with a variable path can be risky if the variable is user-controlled.
**Learning:** `exec.LookPath` was used to fast-fail if the `sudo` binary didn't exist, and the result was passed to `exec.CommandContext`. Security linters like `gosec` treat any variable as potentially tainted, raising a flag.
**Prevention:** Always use constant strings for executable names in `exec.Command` and `exec.CommandContext` (e.g. `"sudo"`) even when resolving paths prior to execution.
