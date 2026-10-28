# Unraid (Docker) deployment

Ductile runs on Unraid as one Docker container built from this repo. The repo `Dockerfile` is the
starting point. Add your own extra tools and mounts in your copy, and keep your `docker-compose.yml`
beside it in appdata.

- Upgrade procedure, gotchas and rollback: [`docs/runbooks/unraid-docker-upgrade.md`](../../docs/runbooks/unraid-docker-upgrade.md)
- Posture and privsep for containers: [`docs/DEPLOYMENT.md`](../../docs/DEPLOYMENT.md) §5b

## Recommended layout

```
/mnt/user/appdata/ductile/          compose file, Dockerfile, config/ (mounted read-only at /app/config)
/mnt/diskN/appdata/ductile/data/    ductile.db (+ -wal/-shm), vault.age, ductile-backups/   -> /app/data
/mnt/diskN/appdata/ductile/secrets/ age.key (0600), mounted read-only at /app/secrets
```

Keep the live SQLite database on a direct disk path (`/mnt/diskN`). A WAL database on the `/mnt/user`
FUSE union can deadlock under load.

## Minimum mounts

| Host path | Container path | Mode |
|---|---|---|
| `.../ductile/config` | `/app/config` | `ro` |
| `.../ductile/data` | `/app/data` | `rw` |
| `.../ductile/secrets` | `/app/secrets` | `ro` |

`vault.age` must sit on a writable bind (under `data`), never under the read-only config mount.
Run `config lock`, `plugin lock` and `plugin clearpass --close` in a one-shot container with the config
mounted `rw`; `docker exec` cannot write through a `:ro` mount.
