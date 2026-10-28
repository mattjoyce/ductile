# Runbook — upgrading a Ductile Docker container (Unraid)

> Applies to a single-container gateway with the vault and the admission gates on
> (`verify_integrity_on_boot`, `fail_on_drift`). Validated on the v1.0.1 → v1.1.0 upgrade.
> The same order works on any Docker host. Unraid adds the storage notes below.

## Set these first

Every command below uses these. Use the paths the **running container** mounts
(`docker inspect <container> --format '{{json .Mounts}}'`), not paths you think are equivalent.

```bash
C=ductile                              # container name
COMPOSE_DIR=/mnt/user/appdata/ductile  # holds docker-compose.yml + Dockerfile
CONFIG_DIR=$COMPOSE_DIR/config         # mounted at /app/config (read-only)
DATA_DIR=/mnt/disk1/appdata/ductile/data       # mounted at /app/data: db, vault.age, backups
SECRETS_DIR=/mnt/disk1/appdata/ductile/secrets # mounted at /app/secrets: age.key
IMAGE=ductile-ductile                  # image name `docker compose build` produces
```

## How a container host differs from a systemd host

| | systemd host ([DEPLOYMENT.md](../DEPLOYMENT.md) §8) | Docker container |
|---|---|---|
| binary | `go build` on the host | built into the image |
| restart | `systemctl restart` | `docker compose up -d` recreates the container |
| config mount | writable | usually `:ro`, so `config lock` needs a **one-shot rw container** |
| `docker exec ... config lock` | works if the mount is rw | fails on a `:ro` mount |

**Unraid storage.** `/mnt/user/...` is the FUSE union (shfs); `/mnt/diskN/...` is a direct disk. Keep
the live SQLite database and `vault.age` on a direct disk path, because a WAL database on the FUSE
union can deadlock under load. A share can span disks, so a directory seen through `/mnt/diskN` may
show only some of its files. Mount, edit and lock config through the same path the container mounts.

## Why not `docker compose up --build -d` in one step

A release that changes the seal format (v1.1.0: `.checksums` BLAKE3 v2 → SHA-256 v3) makes the new
binary refuse to boot on the old file, so `up --build` would crash-loop. Lock **between** the build
and the `up`, and stop the old container before the live lock. The old binary re-reads `.checksums` on
every spawn of a plugin that receives secrets and rejects version 3, so those jobs fail between the
lock and the new container coming up. Keep that gap short and do the lock with nothing running. The
rehearsal on a copy (step 4) still happens while the old container serves. For a release that does
not change the seal, steps 4 to 6 are only needed if you changed config.

## Procedure

```bash
# 0. State.
docker exec $C /app/ductile version; curl -s localhost:8888/healthz     # your API port

# 1. Rollback point: image tag + backup (bundles vault.age, excludes the age key).
TS=$(date -u +%Y%m%dT%H%M%SZ)
docker tag $IMAGE:latest $IMAGE:pre-$TS
docker exec $C /app/ductile system backup --scope all --config /app/config/config.yaml \
  --to /app/data/ductile-backups/ductile-pre-$TS.tar.gz
# Record the backup UID it prints next to your age key. Large databases take minutes.

# 2. Check out the release in your source clone, then BUILD ONLY. No `up`.
cd $COMPOSE_DIR && docker compose build
docker run --rm --entrypoint /app/ductile $IMAGE version

# 3. Did any baked plugin byte change? If none did, no plugin needs re-attesting.
sums() { docker run --rm --entrypoint sh "$1" -c \
  'cd /app/plugins && find . -type f ! -path "*__pycache__*" ! -path "./.git/*" -exec sha256sum {} + | sort -k2'; }
diff <(sums $IMAGE:pre-$TS) <(sums $IMAGE:latest) && echo IDENTICAL
# If files differ, add `plugin lock <name>` for each to the window, or find out why they moved.

# 4. Rehearse on a COPY of the config. If the seal format changed, `config check` cannot run
#    on the live config yet, so this is the only free place to catch a bad token scope or key.
cp -a $CONFIG_DIR /tmp/cfgcopy
oneshot() { # <config dir> <ro|rw> <ductile args...>
  local env; env=$(umask 077; mktemp); docker exec $C env > "$env"
  docker run --rm --env-file "$env" -e DUCTILE_CONFIG_DIR=/app/config \
    -v "$1":/app/config:"$2" -v $SECRETS_DIR:/app/secrets:ro -v $DATA_DIR:/app/data:ro \
    --entrypoint /app/ductile $IMAGE "${@:3}"
  local rc=$?; rm -f "$env"; return $rc
}
oneshot /tmp/cfgcopy rw config lock  --config /app/config/config.yaml
oneshot /tmp/cfgcopy ro config check --config /app/config/config.yaml

# 5. Keep the old seal for rollback (an old binary cannot read the new one).
cp -a $CONFIG_DIR/.checksums $CONFIG_DIR/.checksums.pre-upgrade-$TS

# 6. Cut over. Stop the old container, lock the LIVE config, start the new one. Keep the gap short.
cd $COMPOSE_DIR && docker compose stop
oneshot $CONFIG_DIR rw config lock --config /app/config/config.yaml
docker compose up -d
```

The one-shot runs the gateway image with the running container's environment (kept in a 0600 temp
file) so `config lock` sees what the gateway sees. The data mount can stay read-only for `config lock`.

## Verify

- `/healthz` shows `status: ok`, the same `plugins_loaded` as before, the new `version`, and
  `plugins_circuit_open: 0`.
- The boot log shows `admission: verifying config integrity at boot` with no error after it.
  v1.1.0 also logs `API policy cost margin`, which confirms the token policy loaded.
- `duplicate plugin ignored (keeping first discovered)`: confirm each `kept_path` is the copy you meant.
  `plugin_roots` keep the **first** plugin of a name.
- Your narrowest token: 200 on a read route, 403 on `POST /plugin/<name>/poll` and
  `POST /system/reload`. A bad token gets 401.
- Jobs keep succeeding for a few minutes. Compare failures with what was already failing.
- `selfcheck` against a running gateway reports FAIL on `pid_lock` and skips the DB checks. That is the
  WAL-safety skip, not a fault.

## Rollback

```bash
cp -a $CONFIG_DIR/.checksums.pre-upgrade-<TS> $CONFIG_DIR/.checksums   # the old binary cannot read the new seal
docker tag $IMAGE:pre-<TS> $IMAGE:latest
cd $COMPOSE_DIR && docker compose up -d
```

If the database also needs restoring, untar the matching backup. Its `vault.age` needs the age key from
`$SECRETS_DIR`.

## Gotchas

- **`config check` may report `[readability]` errors on a root container, and they are expected.** v1.1.0
  models the service account as the owner of the config directory. If that owner is not the account
  the container runs as (for example a uid left by SMB or NFS writes, with the container running as
  root), `config check` reports that account cannot read `vault.age`, the age key or the database. At
  boot the gateway opens the files as itself, so it passes. Judge a window by the boot log and
  `/healthz`. Do not `chown` the vault, key or database to quiet the check.
- **`config lock` prints one `REHASH [plugin] ... (BLAKE3 -> SHA-256)` line per attested plugin.** That is
  the seal migration, not a downgrade, and `plugin_fingerprints` stay in `.checksums`. A host whose
  fingerprints are already current prints none. Count lines from the whole output, not a `tail`.
- **A reload can fail and roll back** on `database is locked (SQLITE_BUSY)` while the signal still prints
  `Reload signal sent`. Read the log for `config reloaded successfully` and retry on failure.
- **Plugins are spawned with a scrubbed environment.** A compose `environment:` variable reaches `docker exec`,
  not a plugin. Put a tool's settings in the tool's own config, and test it the way the gateway runs
  it: `docker exec $C env -i HOME=/root PATH=/usr/local/bin:/usr/bin:/bin <cmd>`.
- **There is no job retry verb.** Replay a failed job by posting its stored payload to
  `/pipeline/<name>` or `/plugin/<plugin>/<command>`.
- **Disk headroom.** A rebuild adds build cache. If Docker's image is a loop file (Unraid's `docker.img`),
  keep it under about 85% before an upgrade: `docker buildx prune -af`, then `fstrim -v /var/lib/docker`.
