#!/usr/bin/env bash
# install.sh — lay the FHS layout for an ENFORCED (privsep) ductile gateway,
# per docs/adr/filesystem-layout.md. Idempotent. Run as root from the repo root.
#
# Installs the PACKAGE layer only: service accounts, the FHS directory skeleton
# (owner/mode = the privsep wall), the binary (root-owned, never setuid), and the
# cap-only systemd unit. It deliberately does NOT place config, secrets, or plugin
# code — that is operator data; see docs/runbooks/privsep-thinkpad-enforce.md for
# the config/vault/plugin-code placement + cutover + wall-bite. Validated by the
# live Thinkpad redeploy (epic #83).
set -euo pipefail

BIN_SRC=${BIN_SRC:-./ductile}               # the built ductile binary to install
BIN_DST=${BIN_DST:-/usr/local/bin/ductile}  # /usr/local/bin for a from-source install; a .deb would use /usr/bin
HERE=$(cd "$(dirname "$0")" && pwd)

[ "$(id -u)" -eq 0 ] || { echo "install.sh must run as root" >&2; exit 1; }
[ -x "$BIN_SRC" ] || { echo "no executable binary at BIN_SRC=$BIN_SRC (set BIN_SRC=/path/to/ductile)" >&2; exit 1; }

# 1. service accounts + the dirs tmpfiles owns (/var/lib/ductile/*, /run/ductile)
install -m0644 "$HERE/systemd/ductile-accounts.sysusers.conf" /etc/sysusers.d/ductile-accounts.conf
install -m0644 "$HERE/systemd/ductile-accounts.tmpfiles.conf" /etc/tmpfiles.d/ductile-accounts.conf
systemd-sysusers
systemd-tmpfiles --create /etc/tmpfiles.d/ductile-accounts.conf

# 2. persistent FHS dirs not owned by tmpfiles (/etc + /opt) — owner/mode per the ADR
install -d -o ductile -g ductile -m0700 /etc/ductile          # config + integrity surface (accounts can't traverse)
install -d -o ductile -g ductile -m0700 /etc/ductile/secret   # secret-zero (age key) home, sshd-style
install -d -o root    -g root    -m0755 /opt/ductile          # plugin code: root-owned, world r-x (immutable to gateway+accounts)
install -d -o root    -g root    -m0755 /opt/ductile/plugins

# 3. binary — root-owned 0755, NEVER setuid (privilege is init-conferred by the unit's AmbientCapabilities)
install -m0755 "$BIN_SRC" "$BIN_DST"

# 4. cap-only systemd unit (User=ductile + CAP_SETUID/SETGID)
install -m0644 "$HERE/systemd/ductile.service" /etc/systemd/system/ductile.service
systemctl daemon-reload

cat <<EOF

FHS skeleton laid (per docs/adr/filesystem-layout.md):
  binary   $BIN_DST                 root:root 0755, never setuid
  config   /etc/ductile             ductile 0700   <- place config.yaml + includes
  secret   /etc/ductile/secret      ductile 0700   <- place age.key (0600)
  state    /var/lib/ductile         ductile 0700   + accounts/{default,untrusted} (tmpfiles)
  code     /opt/ductile/plugins     root 0755 world r-x  <- copy plugin code here
  runtime  /run/ductile             ductile 0700   (tmpfiles, boot-recreated)

NOT placed by this installer (operator data): config, age key, vault.age, plugin code.
Next: place those per docs/runbooks/privsep-thinkpad-enforce.md, run
  config lock && plugin lock --all   (BEFORE first enforce boot, else plugins downgrade to untrusted)
then: systemctl enable --now ductile  and verify the wall-bite.
EOF
