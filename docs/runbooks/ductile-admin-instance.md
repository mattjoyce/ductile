# Runbook — the unconfined admin-glue instance (card #106)

> The second of the two ductile roles (PrivSec ADR data-plane/admin split). Pairs with the enforced
> data-plane gateway in [privsep-thinkpad-enforce.md](privsep-thinkpad-enforce.md). Template unit:
> [`deploy/systemd/ductile-admin.service`](../../deploy/systemd/ductile-admin.service).

## Why a second instance

Some automation legitimately needs privilege an unprivileged account uid cannot hold, so it cannot
live behind the privsep wall — but it's coupled to ductile's scheduling + pipeline + `*_notify` chain,
so it shouldn't leave ductile either. The answer is a **separate, unconfined instance**:

| | enforced data-plane (`ductile.service`) | admin-glue (`ductile-admin.service`) |
|---|---|---|
| runs as | `ductile` (984) + `CAP_SETUID/SETGID` | a privileged user in the `docker` group |
| posture | **enforce** (drops plugins to accounts) | **unconfined** (hygiene-only, ADR Layer 1a) |
| config | `/etc/ductile` | `/etc/ductile-admin` |
| listen | `:8081` | a **different** port, e.g. `:8092` |
| plugins | confinable data plugins | the unconfinable set ↓ |

**Admin-instance members** (move these OFF the enforced gateway): `astro_rebuild_staging` (`docker
compose`), `apt_security_check`, `stopwatch_perf_daily`, `file_handler` (reads `/home`), `fabric`
(bound to `~/.config/fabric`), and the `*_notify` plugins triggered by them (notify-locality: a notify
lives on the same instance as its trigger). Each `*_notify` keeps reading the discord webhook from its
config/env as today — unconfined, so the home `.env` is readable.

## Stand-up (on the host, as root)

```bash
# 1. a privileged service user in the docker group (host-specific; e.g.)
sudo useradd --system --home /var/lib/ductile-admin --shell /usr/sbin/nologin ductile-admin
sudo usermod -aG docker ductile-admin
sudo install -d -o ductile-admin -g ductile-admin -m0750 /etc/ductile-admin /var/lib/ductile-admin

# 2. its config: the unconfinable plugins, NO accounts: map (→ unconfined), its own port.
#    Copy the relevant plugin blocks from the legacy config; keep their home/docker paths
#    (this instance CAN read them). Set api.listen to a free port (e.g. localhost:8092).
sudoedit /etc/ductile-admin/config.yaml

# 3. install the template unit (edit User=/paths/port first) + start
sudo install -m0644 deploy/systemd/ductile-admin.service /etc/systemd/system/ductile-admin.service
sudo systemctl daemon-reload && sudo systemctl enable --now ductile-admin
journalctl -u ductile-admin -n 30 --no-pager   # expect "privsep posture: UNCONFINED ..." (by design)
```

## Notes
- It is correct for this instance to log **UNCONFINED** at boot (the #101 posture line) — that's the
  intended hygiene-only state for trusted, privileged admin glue. Don't add an `accounts:` map here.
- Once both instances are green and the old `--user ductile-local` is fully superseded, decommission it.
- Confinable data plugins do NOT belong here — they go on the enforced gateway. This instance is
  strictly the privileged-by-necessity automation.
