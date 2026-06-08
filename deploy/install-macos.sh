#!/usr/bin/env bash
# install-macos.sh — lay the layout for an ENFORCED (privsep) ductile gateway on macOS.
# The launchd peer of deploy/install.sh. Run as root from the repo root. Idempotent.
#
# WHY THIS DIFFERS FROM THE LINUX INSTALLER:
#   launchd has no per-capability grant (no CAP_SETUID/SETGID), so the gateway runs as a
#   ROOT LaunchDaemon — there is NO unprivileged `ductile` gateway account on macOS, only
#   the WORKER accounts the plugins drop to. Because the gateway is root it HAS chown, so
#   its boot fs-reconcile can own the account state dirs itself (the Phase-0
#   TestReconcileAccountFilesystemAsRoot path, proven on Darwin — card #95, commit 5770b59);
#   on Linux a cap-only gateway can't chown, so tmpfiles.d must pre-create them. We still
#   lay the skeleton here (the package layer), the same as install.sh does.
#
# Installs the PACKAGE layer only: worker accounts (dscl), the FHS skeleton, the binary
# (root-owned, never setuid), and the root LaunchDaemon plist. It deliberately does NOT
# place config, secrets, or plugin code — operator data; see the runbook for cutover.
set -euo pipefail

BIN_SRC=${BIN_SRC:-./ductile}               # the built ductile binary to install
BIN_DST=${BIN_DST:-/usr/local/bin/ductile}  # root-owned 0755, never setuid
HERE=$(cd "$(dirname "$0")" && pwd)
PLIST_DST=/Library/LaunchDaemons/com.mattjoyce.ductile.plist

# Worker accounts (drop targets). uids mirror the Linux defaults so ONE config
# `accounts:` map works on both OSes. Override to dodge a collision.
DEFAULT_UID=${DEFAULT_UID:-1001}
UNTRUSTED_UID=${UNTRUSTED_UID:-1002}

[ "$(id -u)" -eq 0 ] || { echo "install-macos.sh must run as root" >&2; exit 1; }
[ "$(uname -s)" = "Darwin" ] || { echo "macOS installer — use deploy/install.sh on Linux" >&2; exit 1; }
[ -x "$BIN_SRC" ] || { echo "no executable binary at BIN_SRC=$BIN_SRC (set BIN_SRC=/path/to/ductile)" >&2; exit 1; }

# 1. worker service accounts via dscl (the sysusers.d analog) — hidden, no login, no home.
make_worker() {
  local name=$1 uid=$2
  if dscl . -read "/Users/$name" >/dev/null 2>&1; then
    echo "  account $name already exists — leaving as-is"; return
  fi
  # refuse a uid collision — never silently co-opt another account's uid
  if dscl . -list /Users UniqueID | awk -v u="$uid" '$2==u{f=1} END{exit !f}'; then
    echo "uid $uid already in use — set DEFAULT_UID/UNTRUSTED_UID to free ids" >&2; exit 1
  fi
  dscl . -create "/Groups/$name"
  dscl . -create "/Groups/$name" PrimaryGroupID "$uid"
  dscl . -create "/Users/$name"
  dscl . -create "/Users/$name" UserShell /usr/bin/false
  dscl . -create "/Users/$name" RealName "Ductile ${name#_ductile-} account"
  dscl . -create "/Users/$name" UniqueID "$uid"
  dscl . -create "/Users/$name" PrimaryGroupID "$uid"
  dscl . -create "/Users/$name" NFSHomeDirectory /var/empty
  dscl . -create "/Users/$name" IsHidden 1            # keep it off the login window
  echo "  created $name (uid $uid, hidden, /usr/bin/false)"
}
echo "1. worker accounts (dscl):"
make_worker _ductile-default   "$DEFAULT_UID"
make_worker _ductile-untrusted "$UNTRUSTED_UID"

# 2. FHS skeleton — owner/mode = the privsep wall. Root-owned (group wheel on macOS).
#    /etc + /var are symlinks into /private on macOS; install -d resolves through them.
echo "2. FHS skeleton:"
install -d -o root -g wheel -m0755 /usr/local/bin
install -d -o root -g wheel -m0700 /etc/ductile           # config + integrity surface (root reads; accounts walled)
install -d -o root -g wheel -m0700 /etc/ductile/secret    # secret-zero (age key) home, sshd-style
install -d -o root -g wheel -m0755 /opt/ductile           # plugin code: root-owned, world r-x (immutable to accounts)
install -d -o root -g wheel -m0755 /opt/ductile/plugins
install -d -o root -g wheel -m0711 /var/lib/ductile       # state root: traverse-only (#109), not listable
install -d -o root -g wheel -m0711 /var/lib/ductile/accounts
install -d -o root -g wheel -m0755 /var/log/ductile       # launchd stdio (no journal on macOS)
# per-worker 0700 dirs OWNED by the worker — the cross-account wall. Root chowns (the
# gateway would also reconcile these at boot, but lay them now for a clean first start).
install -d -m0700 /var/lib/ductile/accounts/default
chown "$DEFAULT_UID:$DEFAULT_UID"     /var/lib/ductile/accounts/default
install -d -m0700 /var/lib/ductile/accounts/untrusted
chown "$UNTRUSTED_UID:$UNTRUSTED_UID" /var/lib/ductile/accounts/untrusted

# 3. binary — root-owned 0755, NEVER setuid (privilege is conferred by launchd-as-root)
echo "3. binary:"
install -m0755 "$BIN_SRC" "$BIN_DST"
echo "  installed $BIN_DST"

# 4. root LaunchDaemon plist — root:wheel 0644 (launchd refuses a writable plist)
echo "4. LaunchDaemon:"
install -m0644 -o root -g wheel "$HERE/launchd/com.mattjoyce.ductile.plist" "$PLIST_DST"
echo "  installed $PLIST_DST"

cat <<EOF

macOS skeleton laid (root LaunchDaemon posture — see docs/DEPLOYMENT_POSTURES.md):
  binary   $BIN_DST           root:wheel 0755, never setuid
  config   /etc/ductile                  root 0700   <- place config.yaml + includes
  secret   /etc/ductile/secret           root 0700   <- place age.key (0600)
  state    /var/lib/ductile              root 0711   + accounts/{default,untrusted} 0700 (worker-owned)
  code     /opt/ductile/plugins          root 0755 world r-x  <- copy plugin code here
  logs     /var/log/ductile              root 0755   (launchd stdout/stderr)
  daemon   $PLIST_DST  root:wheel 0644 (runs as root)
  workers  _ductile-default ($DEFAULT_UID), _ductile-untrusted ($UNTRUSTED_UID)  hidden, nologin

NOT placed by this installer (operator data): config, age key, vault.age, plugin code.
Next: place those (config accounts: map must use uid $DEFAULT_UID/$UNTRUSTED_UID), then
  ductile config lock && ductile plugin lock --all   (BEFORE first enforce boot)
then load + verify the wall-bite:
  sudo launchctl bootstrap system $PLIST_DST
  ductile job inspect ...    # sys_exec(id) -> the worker uid; sudo -u _ductile-untrusted cat <key> -> denied
Stop: sudo launchctl bootout system/com.mattjoyce.ductile
EOF
