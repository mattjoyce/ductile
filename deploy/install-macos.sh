#!/usr/bin/env bash
# install-macos.sh — lay the layout for an ENFORCED (privsep) ductile gateway on macOS.
# The launchd peer of deploy/install.sh. Run as root from the repo root. Idempotent.
#
# WHY THIS DIFFERS FROM THE LINUX INSTALLER:
#   1. launchd has no per-capability grant (no CAP_SETUID/SETGID), so the gateway runs as a
#      ROOT LaunchDaemon — there is NO unprivileged `ductile` gateway account on macOS, only
#      the WORKER accounts the plugins drop to. Because the gateway is root it HAS chown, so
#      its boot fs-reconcile can own the account state dirs itself (the Phase-0
#      TestReconcileAccountFilesystemAsRoot path, proven on Darwin — card #95, commit 5770b59);
#      on Linux a cap-only gateway can't chown, so tmpfiles.d must pre-create them.
#   2. EVERYTHING LIVES UNDER /opt/ductile, NOT /etc + /var. On macOS /etc and /var are
#      symlinks to /private/*, and ductile's RUNTIME refuses symlinked config paths (a path-
#      swap guard) — a config at /etc/ductile fails to start. /opt is a real dir, so we site
#      config/state/logs there (same reason Homebrew uses /usr/local/etc). The binary stays
#      at /usr/local/bin (on PATH for the CLI; not a config path, so symlink-safe).
#
# Installs the PACKAGE layer only: worker accounts (dscl), the FHS skeleton, the binary
# (root-owned, never setuid), and the root LaunchDaemon plist. It deliberately does NOT
# place config, secrets, or plugin code — operator data; see the runbook for cutover.
set -euo pipefail

BIN_SRC=${BIN_SRC:-./ductile}               # the built ductile binary to install
BIN_DST=${BIN_DST:-/usr/local/bin/ductile}  # root-owned 0755, never setuid; on PATH for the CLI
HERE=$(cd "$(dirname "$0")" && pwd)
PLIST_DST=/Library/LaunchDaemons/com.mattjoyce.ductile.plist
BASE=/opt/ductile                           # real, non-symlinked base (NOT /etc or /var on macOS)

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

# 2. FHS skeleton under /opt/ductile (real path) — owner/mode = the privsep wall.
#    Root-owned (group wheel on macOS).
echo "2. FHS skeleton under $BASE:"
install -d -o root -g wheel -m0755 /usr/local/bin
install -d -o root -g wheel -m0755 "$BASE"
install -d -o root -g wheel -m0700 "$BASE/etc"            # config + integrity surface (root reads; accounts walled)
install -d -o root -g wheel -m0700 "$BASE/etc/secret"     # secret-zero (age key) home, sshd-style
install -d -o root -g wheel -m0755 "$BASE/plugins"        # plugin code: root-owned, world r-x (immutable to accounts)
install -d -o root -g wheel -m0711 "$BASE/var"            # state root: traverse-only (#109), not listable
install -d -o root -g wheel -m0711 "$BASE/var/accounts"
install -d -o root -g wheel -m0755 "$BASE/log"            # launchd stdio (no journal on macOS)
# per-worker 0700 dirs OWNED by the worker — the cross-account wall. Root chowns (the
# gateway would also reconcile these at boot, but lay them now for a clean first start).
install -d -m0700 "$BASE/var/accounts/default"
chown "$DEFAULT_UID:$DEFAULT_UID"     "$BASE/var/accounts/default"
install -d -m0700 "$BASE/var/accounts/untrusted"
chown "$UNTRUSTED_UID:$UNTRUSTED_UID" "$BASE/var/accounts/untrusted"

# 3. binary — root-owned 0755, NEVER setuid (privilege is conferred by launchd-as-root)
echo "3. binary:"
install -m0755 "$BIN_SRC" "$BIN_DST"
echo "  installed $BIN_DST"

# 4. root LaunchDaemon plist — root:wheel 0644 (launchd refuses a writable plist)
echo "4. LaunchDaemon:"
install -m0644 -o root -g wheel "$HERE/launchd/com.mattjoyce.ductile.plist" "$PLIST_DST"
echo "  installed $PLIST_DST"

cat <<EOF

macOS skeleton laid under $BASE (root LaunchDaemon posture — docs/DEPLOYMENT_POSTURES.md):
  binary   $BIN_DST           root:wheel 0755, never setuid
  config   $BASE/etc                 root 0700   <- place config.yaml + includes
  secret   $BASE/etc/secret          root 0700   <- place age.key (0600)
  state    $BASE/var                 root 0711   + accounts/{default,untrusted} 0700 (worker-owned)
  code     $BASE/plugins             root 0755 world r-x  <- copy plugin code here
  logs     $BASE/log                 root 0755   (launchd stdout/stderr)
  daemon   $PLIST_DST  root:wheel 0644 (runs as root)
  workers  _ductile-default ($DEFAULT_UID), _ductile-untrusted ($UNTRUSTED_UID)  hidden, nologin

Everything is under /opt (a REAL dir) because /etc and /var are symlinks on macOS and
ductile's runtime refuses symlinked config paths.

NOT placed by this installer (operator data): config, age key, vault.age, plugin code.
Next: place those (config accounts: map must use uid $DEFAULT_UID/$UNTRUSTED_UID), then
  ductile config lock && ductile plugin lock --all   (BEFORE first enforce boot)
then load + verify the wall-bite:
  sudo launchctl bootstrap system $PLIST_DST
  sudo -u _ductile-untrusted cat $BASE/etc/secret/age.key   # -> Permission denied
Stop: sudo launchctl bootout system/com.mattjoyce.ductile
EOF
