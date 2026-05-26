# Getting Started with Ductile

Welcome to **Ductile**, an automation runtime AI agents can run, debug, and build for — and humans can audit. This guide will help you get up and running in minutes. See [`CONSTITUTION.md`](../CONSTITUTION.md) for why the system is shaped this way.

---

## 1. Installation

Ductile is written in Go and requires version **1.25.4** or newer.

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
```

```yaml
# ~/.config/ductile/config.yaml excerpt
plugin_roots:
  - "~/.config/ductile/plugins"
  - "./plugins"

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
  - "~/.config/ductile/plugins"
  - "./plugins"
  - "/opt/ductile/plugins-private"
```

Container example:
```bash
docker run --rm \
  -v "$PWD/config:/config" \
  -v "$PWD/plugins:/app/plugins" \
  -v "/srv/ductile-private-plugins:/opt/ductile/plugins-private:ro" \
  ductile:latest ./ductile system start --config-dir /config
```

### Step 3: Start the Gateway
Run the service in the foreground (defaults to `~/.config/ductile`):
```bash
./ductile system start
```

Or explicitly point to a config directory:
```bash
./ductile system start --config-dir ~/.config/ductile
```

You will see logs indicating the scheduler has started. After 5 minutes (or however you configured it), you'll see the echo job execute and complete.

### Step 4: Graceful Shutdown
Press `Ctrl+C` to stop the gateway. It will wait for any in-flight jobs to finish before releasing the process lock.

---

## 3. CLI Principles

Ductile is designed to be operated by both humans and LLMs. All commands follow a strict **NOUN ACTION** hierarchy:

-   **Hierarchy:** `ductile job inspect`, `ductile config lock`, `ductile system status`.
-   **Verbosity:** Use `-v` or `--verbose` for detailed logic traces.
-   **Safety:** Use `--dry-run` for any mutation to preview changes.
-   **Machine-Readability:** Use `--json` to get structured data for scripts or agents.

---

## Next Steps

-   **Operators:** Read the [Operator Guide](OPERATOR_GUIDE.md) to learn about monitoring and system maintenance.
-   **Developers:** Visit the [Plugin Development Guide](PLUGIN_DEVELOPMENT.md) to start building your own skills.
-   **Architects:** Deep dive into the [Architecture](ARCHITECTURE.md) and [Pipelines](PIPELINES.md) model.
