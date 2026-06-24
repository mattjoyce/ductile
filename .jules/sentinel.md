## 2024-05-15 - Suppressing G204 False Positives Securely
**Vulnerability:** Gosec flagged a G204 warning for using variables in `exec.CommandContext`.
**Learning:** Using variables in exec commands is safe if they are separated arguments and not evaluated by a shell.
**Prevention:** Add `// #nosec G204` with a clear explanation when safely using variables in exec commands.
