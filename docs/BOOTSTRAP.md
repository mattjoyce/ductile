# Bootstrap — from zero to a serving gateway

This is the from-scratch path: a fresh machine, no vault, no tokens, ending with a
locked, vault-native gateway serving the API. It works identically on Linux and
macOS; the platform sections at the end cover service-manager wiring. For
upgrades, operations, and privsep see [DEPLOYMENT.md](DEPLOYMENT.md); for the
secrets model see [SECRETS.md](SECRETS.md); for the design rationale see the
[credential-ladder ADR](adr/vault-credential-ladder.md).

## The shape: a credential ladder

Every credential is issued by the one above it, and nothing is ever written into
a config file:

1. The **age key** (filesystem, 0600) decrypts the vault.
2. The **vault admin token** (printed once at genesis) operates `/vault/*`.
3. The **API bearer token** (minted by the admin token) operates the gateway.

A config with the API enabled but **zero** `api.auth.tokens` is not an error when
a vault is present — it is the bootstrap state. The daemon boots into the
**management posture**: it serves only `/vault/*` on a local unix socket, with no
public listener, no webhooks, no scheduler, no dispatcher. You mint the API token
through that socket, reference it in config, and restart — the gateway activates.
If the API token is ever lost, removing the `api.auth.tokens` block returns the
daemon to the management posture to mint a new one; the same mechanism is the
recovery path.

## Steps

```bash
CFG=~/.config/ductile          # your config directory
BIN=ductile                    # the installed binary (see platform sections)

# 1. Config WITHOUT any token. Required pieces:
#      secrets:
#        age_key_file: age.key
#        vault_file: vault.age
#      api:
#        enabled: true
#        listen: "127.0.0.1:8081"
#        management_socket: /tmp/ductile-admin.sock   # EXPLICIT, short path —
#                       # the default lives beside the state DB, not in $CFG
#    Do NOT add api.auth.tokens yet. Webhooks may reference secret_refs that do
#    not exist yet — during bootstrap that is a warning, not an error.

# 2. Genesis: age key + vault. Daemon stopped. The admin token prints ONCE —
#    capture it into 0600 custody, then shred the capture file.
$BIN secrets keygen --out "$CFG/age.key"
$BIN vault init --vault "$CFG/vault.age" --key "$CFG/age.key"

# 3. Validate. Zero tokens + a genesis vault IS clean — the doctor reaches the
#    same verdict the daemon does.
$BIN config check --config "$CFG"

# 4. First boot: the management posture.
$BIN system start --config "$CFG" &
$BIN system status --config "$CFG"      # expect: boot_posture: management-only (live)

# 5. Mint the API bearer token over the unix socket (value via stdin, never argv).
printf '%s' "$API_TOKEN" | $BIN vault set --api-url "unix:///tmp/ductile-admin.sock" \
  --token "$ADMIN_TOKEN" --name core-api-token --pattern manual

# 6. Stop the daemon, then reference the minted secret in config:
#      api:
#        auth:
#          tokens:
#            - secret_ref: core-api-token
#              scopes: ["*"]

# 7. Seal config and plugins (attestation is keyed by the vault nonce, so this
#    must come after genesis).
$BIN config lock --config "$CFG"
$BIN plugin lock --all --config "$CFG"        # prints a confirm code
$BIN plugin lock --all <code> --config "$CFG"

# 8. Boot the gateway and verify.
$BIN system start --config "$CFG"             # now under your service manager
curl -s localhost:8081/healthz                # "posture": "gateway"
$BIN system vault-audit --config "$CFG"       # genesis + mint recorded
```

## macOS (launchd)

- Install: `deploy/install-macos.sh` lays out `/opt/ductile/{etc,var,log}`, copies
  the binary to `/usr/local/bin`, and loads the LaunchDaemon from
  `deploy/launchd/com.mattjoyce.ductile.plist`.
- A locally built binary is codesigned by `make build`; release artifacts are
  unsigned — either codesign them yourself or clear quarantine after download.
- Privilege separation on macOS runs in deploy+verify posture (the uid-drop
  enforcement wall is Linux-only today); see DEPLOYMENT.md §5b.

## Linux (systemd)

- Install: `deploy/install.sh` lays out FHS paths (`/etc/ductile`,
  `/var/lib/ductile`, `/opt/ductile/plugins`) and installs the units in
  `deploy/systemd/` (`ductile.service`, sysusers and tmpfiles fragments).
- For the enforced privsep posture (CAP_SETUID gateway, per-plugin worker
  accounts) follow DEPLOYMENT.md §5b before first boot.

## Sanity rules worth knowing

- Unix socket paths are capped near 104 bytes — keep `api.management_socket`
  short (`/tmp/...` or beside the state DB).
- The management socket path must not point at an existing non-socket file; the
  daemon refuses to boot rather than remove it.
- A config cannot be *activated* (gateway posture) while any configured
  `secret_ref` is unminted — mint everything you reference before the restart in
  step 6, or stage those includes until after bootstrap.
