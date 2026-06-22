## 2026-06-22 - Subprocess Launched with Variable (G204) False Positive in sidedoor_audit_unix.go
**Vulnerability:** gosec flagged `exec.CommandContext(ctx, sudo, "-n", "-l", "-U", username)` as a potential command injection risk (G204).
**Learning:** When using `exec.CommandContext` with cleanly separated string arguments that are derived from trusted internal sources (e.g., `exec.LookPath` and `user.LookupId` returning a username), it is a false positive. We must document why the input is trusted using a `#nosec` comment.
**Prevention:** Add `// #nosec G204` along with an explanation of why the variables are trusted when executing safely separated commands with dynamic arguments to prevent gosec false positives and maintain clear security intent documentation.
