## CLI Commands

> Operator guidance: use `--json` for structured output; use `--dry-run` before
> any mutation; prefer `config check --json` as the first diagnostic step.

Format: `<command> tier=<READ|WRITE> mut=<0|1> out=<human|json> [flags="<...>"] d="<intent>"`

### System & Lifecycle
- system.start tier=WRITE mut=1 out=human d="Start the gateway service in foreground."
- system.status tier=READ mut=0 out=human|json flags="[--json]" d="Check gateway health: PID lock, state DB, and plugin reachability."
- system.plugin-facts tier=READ mut=0 out=human|json flags="<plugin> [--fact-type <type>] [--json] [--limit <n>]" d="Inspect append-only plugin facts and their recorded JSON payloads."
- system.breaker tier=READ mut=0 out=human|json flags="<plugin> [--command <command>] [--json] [--limit <n>]" d="Inspect circuit breaker current state and transition history."
- system.reload tier=WRITE mut=1 out=human|json flags="[--json] [--api-url <url>] [--api-key <key>]" d="Hot-reload configuration without restart."
- system.reset tier=WRITE mut=1 out=human flags="<plugin>" d="Reset a tripped circuit breaker for a plugin."
- system.skills tier=READ mut=0 out=markdown flags="[--config <dir>]" d="Export live capability manifest for LLM consumption."
- system.selfcheck tier=READ mut=0 out=human|json flags="[--json] [--config <path>]" d="Run integrity checks: PID lock, PRAGMA integrity_check, schema validation, queue terminal-state freshness."
- system.backup tier=READ mut=0 out=human flags="--to <file.tar.gz> [--scope db|config|plugins|all] [--config <path>]" d="Atomic scoped snapshot to a tar.gz archive via SQLite VACUUM INTO. Safe under concurrent writers."

### Configuration Management
- config.check tier=READ mut=0 out=human|json flags="[--json] [--strict]" d="Validate syntax, policy, and integrity checksums."
- config.lock tier=WRITE mut=1 out=human d="Authorize current state by regenerating .checksums."
- config.show tier=READ mut=0 out=human|json flags="[entity] [--json]" d="View resolved configuration for an entity."
- config.get tier=READ mut=0 out=human|json flags="<path> [--json]" d="Read a specific config value by dotted path."
- config.set tier=WRITE mut=1 out=human|json flags="<path>=<val> [--dry-run] [--apply]" d="Update config value; dry-run first."
- config.init tier=WRITE mut=1 out=human flags="[--config-dir <path>] [--force]" d="Initialize a new configuration directory with defaults."
- config.backup tier=READ mut=0 out=human flags="[--output <path>]" d="Create a backup archive of the current configuration."
- config.restore tier=WRITE mut=1 out=human flags="<archive-path>" d="Restore configuration from a backup archive."

### Auth & Scopes
- config.token.create tier=WRITE mut=1 out=human|json flags="--name <n> (--scopes <csv>|--scopes-file <path|->)" d="Create a new scoped API token."
- config.token.list tier=READ mut=0 out=human|json d="List all registered API tokens (redacted)."
- config.token.inspect tier=READ mut=0 out=human|json flags="<name>" d="Inspect token details and verify scope integrity."
- config.token.delete tier=WRITE mut=1 out=human|json flags="<name>" d="Revoke an API token."
- config.scope.add tier=WRITE mut=1 out=human|json flags="<token> <scope>" d="Add a scope to an existing token."
- config.scope.remove tier=WRITE mut=1 out=human|json flags="<token> <scope>" d="Remove a scope from a token."
- config.scope.validate tier=READ mut=0 out=human|json flags="<scope-string>" d="Dry-run validate a scope against discovered plugins."

### Secrets (encryption at rest for config bundles)
> These operate on static config files (e.g. tokens.yaml), NOT the vault. Use vault.rotate-key for the vault's own key.
- secrets.keygen tier=WRITE mut=1 out=human flags="[--out <path>]" d="Generate an age identity (private key at mode 0600 + public recipient)."
- secrets.encrypt tier=WRITE mut=1 out=human flags="--recipient <age1...> [--recipient ...] [--recipients-file <path>] [--in <path>] [--out <path>]" d="Encrypt a plaintext config bundle to recipients."
- secrets.rotate tier=WRITE mut=1 out=human flags="--key <path> --recipient <age1...> [...] [--recipients-file <path>] --file <path>" d="Re-encrypt a config bundle under a new recipient set (in place, atomic)."

### Vault (daemon-owned dynamic secret store)
> Keyless API clients POST to the running daemon (the sole writer) with the vault admin token (--token or DUCTILE_VAULT_TOKEN); --api-url accepts unix://<sock> to reach the vault-operable bootstrap posture. Local key-touching ops (init/rotate-key/rotate-admin-token) read the age key and require the daemon to be STOPPED.
- vault.init tier=WRITE mut=1 out=human flags="--vault <path> --key <path>" d="Genesis: create a new vault (core principal, nonce, admin token). Local, key-touching; daemon stopped."
- vault.rotate-key tier=WRITE mut=1 out=human flags="[--config <dir>]" d="Rotate the vault's age identity (mint, re-encrypt, retire old). Local, key-touching; daemon stopped."
- vault.register-principal tier=WRITE mut=1 out=human flags="--name <n> --kind plugin|consumer|gateway [--api-url <url>] [--token <t>]" d="Register a deliver-to principal."
- vault.set tier=WRITE mut=1 out=human flags="--name <n> [--pattern manual|auto] [--principal <csv>] [--api-url <url>] [--token <t>]" d="Set a secret; value read from stdin, never argv."
- vault.roll tier=WRITE mut=1 out=human flags="--name <n> [--api-url <url>] [--token <t>]" d="Roll (supersede) a secret's value; manual from stdin, auto daemon-minted."
- vault.revoke tier=WRITE mut=1 out=human flags="--name <n> [--api-url <url>] [--token <t>]" d="Revoke a secret; terminal, clears the value."
- vault.revoke-principal tier=WRITE mut=1 out=human flags="--name <n> [--api-url <url>] [--token <t>]" d="Revoke a principal; its secrets stop being delivered."
- vault.purge-principal tier=WRITE mut=1 out=human flags="--name <n> [--api-url <url>] [--token <t>]" d="Remove a principal and strip all its grants."
- vault.roll-principal tier=WRITE mut=1 out=human flags="--name <n> [--api-url <url>] [--token <t>]" d="Roll every auto secret a principal holds."

### Plugins, Routes & Webhooks
- config.plugin.list tier=READ mut=0 out=human|json d="List configured and discovered plugins."
- config.plugin.show tier=READ mut=0 out=human|json flags="<name>" d="Show configuration for a specific plugin."
- config.plugin.set tier=WRITE mut=1 out=human|json flags="<name> <path> <value>" d="Update plugin-specific configuration."
- config.route.list tier=READ mut=0 out=human|json d="List all event routes."
- config.route.add tier=WRITE mut=1 out=human|json flags="--from <p> --event <e> --to <p>" d="Add a new event route."
- config.route.remove tier=WRITE mut=1 out=human|json flags="--from <p> --event <e> --to <p>" d="Remove an event route."
- config.webhook.list tier=READ mut=0 out=human|json d="List configured webhooks."
- config.webhook.add tier=WRITE mut=1 out=human|json flags="--path <p> --plugin <p> --secret-ref <r>" d="Add a new webhook endpoint."

### Jobs & Execution
- plugin.list tier=READ mut=0 out=human|json d="List available plugins and their commands via API."
- plugin.run tier=WRITE mut=1 out=human|json flags="<name> [--command <c>] [--payload <json>]" d="Manually trigger a plugin command via API."
- plugin.lock tier=WRITE mut=1 out=human flags="<name> | --all [--config <dir>]" d="Attest a plugin's bytes (keyed-BLAKE3, vault nonce). Required before it can receive vault secrets; re-verified at compose time."
- job.inspect tier=READ mut=0 out=human|json flags="<job_id>" d="Retrieve logs, baggage, and execution details for a job."
- job.logs tier=READ mut=0 out=human|json flags="[--plugin <p>] [--query <text>] [--limit <n>]" d="Query stored job logs for audit and troubleshooting."
