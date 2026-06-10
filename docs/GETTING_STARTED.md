# Getting Started with Ductile

Welcome to **Ductile**, an automation runtime AI agents can run, debug, and build for — and humans can audit. This guide will help you get up and running in minutes. See [`CONSTITUTION.md`](../CONSTITUTION.md) for why the system is shaped this way.

---

## 1. Installation

Ductile is written in Go and requires version **1.25.0** or newer (see `go.mod`).

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/mattjoyce/ductile.git
    cd ductile
    ```

2.  **Build the gateway:**
    ```bash
    go build -o ductile ./cmd/ductile
    ```

    This creates a single executable named `ductile` in your project root.

---

## 2. Basic Usage (The Echo Showcase)

After building the binary, you can run the included `echo` plugin to verify the system.

### Step 1: Verify Plugin Discovery
Ductile discovers plugins from `plugin_roots`.
For this repo, the local `plugins/` directory includes `echo`:
```bash
ls -F plugins/echo/manifest.yaml
```

### Step 2: Configure the Plugin
Ductile uses a directory-based config layout (typically `~/.config/ductile/`).
This repo ships example files in `config/` — copy that folder to your config dir and edit.

```bash
cp -R ./config ~/.config/ductile
cp -R ./plugins/echo ~/.config/ductile/plugins/   # put echo in the config's plugin root
```

```yaml
# ~/.config/ductile/config.yaml excerpt
plugin_roots:
  # Relative entries resolve against the config directory; "~" is never expanded.
  - "plugins"

include:
  - api.yaml
  - plugins.yaml
  - pipelines.yaml
  - webhooks.yaml
```

```yaml
# ~/.config/ductile/plugins.yaml excerpt
plugins:
  echo:
    enabled: true
    schedules:
      - id: default
        every: 5m
        jitter: 30s
    config:
      message: "Hello from Ductile!"
```

### Step 2b: Add an External Plugin Root (Optional)
You can mount additional plugin volumes and add them to `plugin_roots` in priority order:

```yaml
plugin_roots:
  - "plugins"                          # relative to the config directory
  - "/opt/ductile/plugins-private"     # absolute roots work anywhere
```

Container example:
```bash
docker run --rm \
  -v "$PWD/config:/config" \
  -v "$PWD/plugins:/config/plugins" \
  -v "/srv/ductile-private-plugins:/opt/ductile/plugins-private:ro" \
  ductile:latest ./ductile system start --config /config
```

### Step 3: Start the Gateway
Run the service in the foreground (defaults to `~/.config/ductile`):
```bash
./ductile system start
```

Or explicitly point to a config directory:
```bash
./ductile system start --config ~/.config/ductile
```

You will see logs indicating the scheduler has started. After 5 minutes (or however you configured it), you'll see the echo job execute and complete.

### Step 4: Graceful Shutdown
Press `Ctrl+C` to stop the gateway. It will wait for any in-flight jobs to finish before releasing the process lock.

### Step 5: (Optional) Initialize the vault for plugin secrets
The echo demo needs no secrets. But when you add a plugin that needs an API token, the credential comes from the **vault** — not an env var, not config. Stand it up once, with the gateway stopped:
```bash
ductile secrets keygen --out ~/.config/ductile/age.key    # an age key — back this up
ductile vault init --vault ~/.config/ductile/vault.age --key ~/.config/ductile/age.key
# prints a one-time admin token — store it; it authorizes vault writes
```
Then start the gateway and grant a plugin its secret. The full lifecycle (register → set → roll → revoke, and the `plugin lock` attestation that gates delivery) is in the [Secrets & Vault guide](SECRETS.md).

---

## 3. CLI Principles

Ductile is designed to be operated by both humans and LLMs. All commands follow a strict **NOUN ACTION** hierarchy:

-   **Hierarchy:** `ductile job inspect`, `ductile config lock`, `ductile system status`.
-   **Verbosity:** `-v` / `--verbose` gives detailed logic traces where a command supports it.
-   **Safety:** mutating config commands accept `--dry-run` to preview changes.
-   **Machine-Readability:** read commands accept `--json` for structured output.

Check `ductile <noun> <action> --help` for the flags a specific command takes.

---

## Next Steps

-   **Operators:** Read the [Operator Guide](OPERATOR_GUIDE.md) to learn about monitoring and system maintenance.
-   **Developers:** Visit the [Plugin Development Guide](PLUGIN_DEVELOPMENT.md) to start building your own skills.
-   **Architects:** Deep dive into the [Architecture](ARCHITECTURE.md) and [Pipelines](PIPELINES.md) model.
-   **Secrets:** See [Secrets & Vault](SECRETS.md) for delivering credentials to plugins (the vault, principals, and attestation).
