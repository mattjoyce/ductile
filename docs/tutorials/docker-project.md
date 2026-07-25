# Ductile in Docker — Project-in-a-Container

> **Diátaxis register: _tutorial_.** This page walks you from zero to a running
> Ductile gateway inside a Docker container, with your own plugins and
> dependencies baked into the image. It uses the **lowsec (unconfined) posture**
> — the legitimate default for a single-author, trusted-plugin container.

You will end with:

- A Docker image containing the Ductile binary, runtime deps, and your plugins.
- A vault with a genesis age key, an admin token, and a minted API token.
- A gateway serving the full agent surface (`/healthz`, `/openapi.json`,
  `/topology`, `/skills`, `/plugin/...`, `/job/...`).

---

## Prerequisites

- Docker Engine 24+ with the Compose plugin (`docker compose version`).
- The `age` encryption tool installed on your host
  (`sudo apt install age` on Debian/Ubuntu, `brew install age` on macOS).
- A clone of the Ductile repo (or a published base image — see
  [Docker base image](#docker-base-image-optional) below).

```bash
git clone https://github.com/mattjoyce/ductile.git
cd ductile
```

---

## Why this posture

Ductile's privilege-separation model offers three deployment postures (see
[Deployment Postures](../DEPLOYMENT_POSTURES.md)). For a Docker container where
you wrote (or vouched for) every plugin, the **unconfined / hygiene-only**
posture (ADR Layer 1a) is the honest default:

- The container runs as an unprivileged user (`ductile`, uid 100).
- No `CAP_SETUID` — no uid-drop wall between plugins.
- The boot gate runs `unconfined` and logs it loudly.
- You still get spawn hygiene: env allowlist, secrets only over stdin,
  vault-attested plugin delivery.

Adopting full privsep *inside* a container would *raise* the container's
privilege (run as root or add `CAP_SETUID`), which is the worst trade on the
one host where a privileged container is most costly. That path exists but is
intentionally not the default — see
[Deployment §5b](../DEPLOYMENT.md#5b-privsep--system-service-with-account-users-opt-in).

---

## The credential ladder (why you can't just boot)

Since v1.0.0, API tokens are **vault-only** — a literal `token:` value in
config is rejected at load. You cannot boot with `api: enabled: true` and zero
tokens. Instead, Ductile boots into the **management posture**: it serves only
`/vault/*` on a local unix socket, with no public listener, no scheduler, no
dispatcher. You mint the API token through that socket, reference it in config,
then reload to activate the gateway.

```
age key (offline)  ──mints──▶  admin token  ──mints──▶  API token
 (genesis, 0600)                (vault init)             (vault set)
```

This is the bootstrap sequence the tutorial walks through. For the full
rationale see [BOOTSTRAP.md](../BOOTSTRAP.md) and the
[credential-ladder ADR](../adr/vault-credential-ladder.md).

---

## Step 0 — Understand the file ownership gotcha

The container runs as user `ductile` (uid 100 in the Alpine image). Files and
directories you create on the host are owned by your host uid (typically 1000).
The container user cannot read or write them until you fix ownership.

This is the single most common stumbling block. Every volume mount must be
owned by uid 100:gid 100, and the age key must be mode `0600`.

You will see this pattern after every file-creating step:

```bash
sudo chown -R 100:100 config/ data/
sudo chmod 600 config/age.key
```

---

## Step 1 — Create the project layout

Create a project directory with a config directory and a data directory:

```bash
mkdir -p ductile-docker/config ductile-docker/data
cd ductile-docker
```

---

## Step 2 — Write the config files

These are the minimal config files for a from-scratch bootstrap. They
deliberately contain **no API token** — that comes from the vault.

### config/config.yaml

```yaml
service:
  unconfined: true        # lowsec posture — see "Why this posture" above
  strict_mode: false

state:
  path: /app/data/ductile.db

plugin_roots:
  - /app/plugins           # baked into the image; override to use your own

secrets:
  age_key_file: /app/config/age.key
  vault_file: /app/config/vault.age

include:
  - api.yaml
  - plugins.yaml
  - pipelines.yaml
```

### config/api.yaml

```yaml
api:
  enabled: true
  listen: "0.0.0.0:8081"           # bind all interfaces inside the container
  management_socket: /tmp/ductile-admin.sock   # short path — unix socket cap ~104 bytes
  # No auth.tokens yet — the management posture serves /vault/* only until you
  # mint the API token and reference it here (Step 6).
```

### config/plugins.yaml

```yaml
plugins:
  echo:
    enabled: true
    config:
      message: "hello from ductile-in-docker"
```

### config/pipelines.yaml

```yaml
pipelines: []
```

---

## Step 3 — Generate the age key (on the host)

The age key is the root of the credential ladder. Generate it on the host so
it never lives inside the image:

```bash
age-keygen -o config/age.key
# Public key: age1... (the recipient — you don't need it for this tutorial)
```

Now fix ownership and permissions:

```bash
sudo chown -R 100:100 config/ data/
sudo chmod 600 config/age.key
```

Verify:

```bash
ls -la config/age.key
# -rw------- 1 100 100 189 ... config/age.key
```

> **Why 0600?** Ductile's vault init refuses to load an age key that is
> readable by group or others. This is a hard check, not a warning.

---

## Step 4 — Build the Docker image

If you are working from the Ductile repo clone, build from the repo root:

```bash
cd /path/to/ductile   # the cloned repo
sudo docker build -t ductile:latest .
```

This produces an image with:

- The `ductile` binary (multi-stage Go build).
- Runtime deps: bash, jq, python3, uv, sqlite-libs, curl, ca-certificates, tzdata.
- The repo's built-in plugins copied to `/app/plugins/`.
- The `ductile` user (uid 100) and `/app/data/` directory.

If you are using a published base image instead, see
[Docker base image](#docker-base-image-optional) below.

---

## Step 5 — Genesis: create the vault

Run `vault init` in a one-shot container (daemon stopped — this is a
key-touching op). It seeds the `core` principal, the fingerprint nonce, and
prints the **admin token** exactly once:

```bash
sudo docker run --rm \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/data:/app/data" \
  -e DUCTILE_CONFIG_DIR=/app/config \
  ductile:latest \
  ./ductile vault init --vault /app/config/vault.age --key /app/config/age.key
```

You will see output like:

```
Wrote vault to /app/config/vault.age (encrypted; core principal + fingerprint nonce seeded).
Initial admin token (shown once — store it now, it is not recoverable in plaintext):
U4XnbmgKKOdTKGabNLojrQbQ5J6IaRs3VVSILuql6y8
```

**Store the admin token immediately.** It authorizes every `/vault/*` write and
is never printed again. If you lose it, the recovery path is
`ductile vault rotate-admin-token` (daemon stopped, age key in hand — see
[SECRETS.md](../SECRETS.md)).

Fix ownership of the vault file:

```bash
sudo chown -R 100:100 config/ data/
sudo chmod 600 config/age.key
```

---

## Step 6 — Boot the management posture

Start the gateway in the background. Because the config has `api: enabled: true`
but no `auth.tokens`, Ductile boots into the **management posture**: it serves
`/vault/*` on the unix socket only. No public listener, no scheduler, no
dispatcher.

```bash
sudo docker run --rm -d --name ductile \
  -e DUCTILE_CONFIG_DIR=/app/config \
  -v "$(pwd)/config:/app/config" \
  -v "$(pwd)/data:/app/data" \
  -p 18081:8081 \
  ductile:latest
```

Check the logs — you should see the management posture:

```bash
sudo docker logs ductile 2>&1 | grep -E 'posture|management'
```

Expected:

```json
{"level":"WARN","msg":"booting in vault-operable / ductile-closed posture: no api token resolved yet — serving /vault/* on the local management socket only, public gateway listener NOT open. ...","posture":"management-only"}
```

> **The healthz endpoint will not respond yet.** The public listener is not
> open in management posture. This is correct — you have not minted the API
> token.

---

## Step 7 — Mint the API token

With the gateway in management posture, mint the API bearer token via the
vault management API on the unix socket:

```bash
ADMIN_TOKEN="U4XnbmgKKOdTKGabNLojrQbQ5J6IaRs3VVSILuql6y8"   # from Step 5
API_TOKEN="choose-a-strong-token-here"                         # your API token value

sudo docker exec ductile sh -c "printf '%s' '$API_TOKEN' | ./ductile vault set \
  --api-url 'unix:///tmp/ductile-admin.sock' \
  --token '$ADMIN_TOKEN' \
  --name core-api-token \
  --pattern manual"
```

Expected:

```
Set secret "core-api-token" via the daemon.
```

The token is now in the vault but nothing references it yet.

---

## Step 8 — Reference the token and lock

Update `config/api.yaml` to reference the minted token by `secret_ref`:

```yaml
api:
  enabled: true
  listen: "0.0.0.0:8081"
  management_socket: /tmp/ductile-admin.sock
  auth:
    tokens:
      - secret_ref: core-api-token
        scopes: ["*"]
```

Now seal the config and attest the plugins. **Both locks are required** —
`config lock` re-hashes config files; `plugin lock` fingerprints plugin bytes
so the vault can verify them at spawn (see [SECRETS.md](../SECRETS.md)).

```bash
sudo docker exec ductile ./ductile config lock --config /app/config
sudo docker exec ductile ./ductile plugin lock echo --config /app/config
```

Fix ownership again (config lock wrote `.checksums`):

```bash
sudo chown -R 100:100 config/ data/
sudo chmod 600 config/age.key
```

---

## Step 9 — Reload and activate the gateway

Send a reload signal to the running gateway. It hot-swaps the config, resolves
the `core-api-token` secret from the vault, and activates the public listener:

```bash
sudo docker exec ductile ./ductile system reload --config /app/config
```

Verify the gateway is now fully active:

```bash
curl -s http://localhost:18081/healthz | jq .
```

Expected:

```json
{
  "status": "ok",
  "uptime_seconds": 120,
  "queue_depth": 0,
  "plugins_loaded": 10,
  "plugins_circuit_open": 0,
  "posture": "gateway"
}
```

The `"posture": "gateway"` line means the management posture is over — the
scheduler, dispatcher, and public API listener are all running.

---

## Step 10 — Run a job

Trigger the `echo` plugin's `poll` command:

```bash
API_TOKEN="choose-a-strong-token-here"   # the value you minted in Step 7

curl -s -X POST http://localhost:18081/plugin/echo/poll \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"payload": {"message": "hello from docker"}}' | jq .
```

Expected:

```json
{
  "job_id": "497d3bde-f263-4131-b6d8-6bb967255274",
  "status": "queued",
  "plugin": "echo",
  "command": "poll"
}
```

Poll for the result:

```bash
curl -s http://localhost:18081/job/497d3bde-... \
  -H "Authorization: Bearer $API_TOKEN" | jq .
```

Expected:

```json
{
  "job_id": "497d3bde-...",
  "status": "succeeded",
  "plugin": "echo",
  "command": "poll",
  "result": {
    "status": "ok",
    "result": "hello from ductile at 2026-07-20T11:48:05Z"
  }
}
```

---

## Step 11 — Verify the agent surfaces

The gateway exposes structured surfaces an LLM agent can drive without reading
source code:

```bash
# OpenAPI schema — 26+ paths
curl -s http://localhost:18081/openapi.json \
  -H "Authorization: Bearer $API_TOKEN" | jq '.info.title, (.paths | length)'

# Plugin topology graph
curl -s http://localhost:18081/topology \
  -H "Authorization: Bearer $API_TOKEN" | jq '.plugins | length'

# Capability registry (Markdown or JSON)
curl -s http://localhost:18081/skills \
  -H "Authorization: Bearer $API_TOKEN" | jq '.skills | length'

# Health diagnostics
curl -s http://localhost:18081/system/doctor \
  -H "Authorization: Bearer $API_TOKEN" | jq .
```

---

## Step 12 — Stop and clean up

```bash
sudo docker stop ductile
```

Your config and data persist on the host in `config/` and `data/`. To start
again, just `docker run` (Step 6) — the vault and token are already in place,
so the gateway boots straight to `gateway` posture without the management
posture.

To start fresh, remove the vault and database:

```bash
rm config/vault.age data/ductile.db
```

Then repeat from Step 3.

---

## Docker Compose

For repeatable deployments, use a `docker-compose.yml`. This example includes
the env var and volume mounts the tutorial above uses:

```yaml
services:
  ductile:
    image: ductile:latest
    container_name: ductile
    restart: unless-stopped
    environment:
      - DUCTILE_CONFIG_DIR=/app/config
    volumes:
      - ./config:/app/config
      - ./data:/app/data
    ports:
      - "18081:8081"
    healthcheck:
      test: ["CMD", "curl", "-fsS", "http://127.0.0.1:8081/healthz"]
      interval: 30s
      timeout: 10s
      retries: 3
      start_period: 10s
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"
```

Start with:

```bash
sudo docker compose up -d
```

The bootstrap steps (5-9) still need to run once against the running container
before the gateway activates. After that, `docker compose up -d` boots straight
to gateway posture.

---

## Bringing your own plugins and dependencies

The real value of containerized Ductile is baking your own plugins and their
dependencies into the image at build time. This aligns with Ductile's Tier 1
plugin contract: **stdlib, fetch nothing at spawn** (see
[Plugin Development §10.6](../PLUGIN_DEVELOPMENT.md#10-6-runtime-contract-privsep)).

### A project Dockerfile

Create a `Dockerfile` in your project that layers on top of the Ductile image:

```dockerfile
FROM ductile:latest

# Install system-level dependencies your plugins need
USER root
RUN apk add --no-cache ffmpeg yt-dlp

# Install Python dependencies (build-time, not spawn-time)
RUN pip install --no-cache-dir requests httpx

# Copy your plugins into the image
COPY --chown=ductile:ductile plugins/ /app/plugins/

# Copy your pipelines
COPY --chown=ductile:ductile pipelines.yaml /app/config/pipelines.yaml

USER ductile
```

### Project directory structure

```
my-ductile-project/
├── Dockerfile
├── docker-compose.yml
├── config/
│   ├── config.yaml
│   ├── api.yaml
│   ├── plugins.yaml
│   └── age.key              # generated, not committed
├── data/                    # SQLite DB persists here
└── plugins/
    ├── my-fetcher/
    │   ├── manifest.yaml
    │   └── run.py
    └── my-notifier/
        ├── manifest.yaml
        └── run.sh
```

### Point config at your plugins

In `config/config.yaml`, set `plugin_roots` to `/app/plugins` (where your
custom plugins land in the image):

```yaml
plugin_roots:
  - /app/plugins
```

### The musl caveat

The base image uses Alpine Linux (musl libc). This is fine for bash plugins,
stdlib Python, and most CLI tools. However, Python packages with **native C
extensions** (numpy, pandas, cryptography, psycopg2) will not find pre-built
wheels for musl — they need compilation at build time:

```dockerfile
USER root
RUN apk add --no-cache gcc musl-dev python3-dev && \
    pip install --no-cache-dir cryptography && \
    apk del gcc musl-dev python3-dev   # trim the build deps if you want
USER ductile
```

If native-extension dependencies become a recurring problem, consider a
glibc-based variant:

```dockerfile
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata bash jq python3 python3-pip curl sqlite3 && \
    rm -rf /var/lib/apt/lists/*
# ... copy the ductile binary from the builder stage
```

---

## Docker base image (optional)

If you do not want to build from source every time, the Ductile Dockerfile is
structured so the runtime stage can serve as a base image. A published image at
`ghcr.io/mattjoyce/ductile:latest` (or `ductile/ductile:latest`) would let you
`FROM` it directly:

```dockerfile
FROM ghcr.io/mattjoyce/ductile:1.0.1
COPY --chown=ductile:ductile plugins/ /app/plugins/
```

Until a published image exists, build from the repo:

```bash
git clone https://github.com/mattjoyce/ductile.git
cd ductile
sudo docker build -t ductile:latest .
```

---

## Gotchas and troubleshooting

### "permission denied" on config or data files

The container runs as uid 100. Host-created files are owned by your uid (1000).
Fix:

```bash
sudo chown -R 100:100 config/ data/
```

### "age key file has insecure permissions"

The key must be `0600` and owned by the container user:

```bash
sudo chown 100:100 config/age.key
sudo chmod 600 config/age.key
```

### "api.auth.tokens must be configured when API is enabled"

This is the v1.0.0 breaking change. You cannot boot with `api: enabled: true`
and no tokens. Follow the credential-ladder bootstrap (Steps 5-9) to mint the
token from the vault.

### "no config found"

The binary cannot find the config directory. Set `DUCTILE_CONFIG_DIR`:

```bash
-e DUCTILE_CONFIG_DIR=/app/config
```

### Healthz returns nothing in management posture

This is correct. The public listener is not open until you mint the API token,
reference it in config, and reload. Check logs instead:

```bash
sudo docker logs ductile 2>&1 | grep posture
```

### "vault init: write permission denied"

The config directory is mounted read-only, or the directory is not owned by
the container user. Either make the mount read-write, or run `vault init`
before mounting. Fix:

```bash
sudo chown -R 100:100 config/
```

### The `log_level` key is not recognized

Older docs used `log_level: info` at the top level. The current version does
not recognize this field (logs a warning). Omit it — logging is controlled by
the service and is info-level by default.

---

## Summary

| Step | What | Posture |
|------|------|---------|
| 1-4 | Create config, generate age key, build image | — |
| 5 | `vault init` (one-shot container) | — |
| 6 | Boot gateway (detached) | management-only |
| 7 | `vault set` to mint API token | management-only |
| 8 | Update config with `secret_ref`, `config lock`, `plugin lock` | management-only |
| 9 | `system reload` | **gateway** |
| 10-11 | Run jobs, verify agent surfaces | gateway |
| 12 | Stop / clean up | — |

After the first bootstrap, subsequent `docker compose up -d` boots straight to
gateway posture — the vault and token are already in place on the persistent
volumes.

---

## See also

- [Deployment Postures](../DEPLOYMENT_POSTURES.md) — the three ways to confine
  plugins and why unconfined is the legitimate Docker default.
- [Bootstrap](../BOOTSTRAP.md) — the credential-ladder bootstrap for
  host-local deployments (same sequence, different runtime).
- [Secrets & Vault](../SECRETS.md) — the vault model, plugin attestation, and
  spawn hygiene.
- [Deployment Guide](../DEPLOYMENT.md) — systemd/launchd deployments, privsep
  enforce, and the Docker/Unraid subsection (§5b).
- [Plugin Development](../PLUGIN_DEVELOPMENT.md) — the manifest contract, the
  Tier 1 runtime contract, and the spawn hygiene rules.
- [Constitution](https://github.com/mattjoyce/ductile/blob/main/CONSTITUTION.md)
  — the five pillars and the "is not" list.
