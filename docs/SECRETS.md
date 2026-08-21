# Secrets, Vault & Spawn Hygiene

This guide covers the security features for hardening a Ductile deployment:

1. **Encryption at rest** — store `tokens.yaml` (and other include files) age-encrypted on disk, decrypted in memory at load.
2. **The Vault** — a daemon-owned, age-encrypted store of *dynamic* secrets delivered to plugins per-principal at dispatch time (distinct from the static config-bundle encryption above).
3. **Spawn hygiene** — plugin child processes receive a minimal, allowlisted environment rather than the gateway's full environment.

It assumes the directory model and integrity preflight described in [CONFIG_REFERENCE.md](CONFIG_REFERENCE.md).

> **Encryption at rest vs. the Vault — which do I want?** Encryption at rest protects *static config files* you author (a `tokens.yaml` you edit and re-lock). The Vault is a *running store* the daemon owns: secrets are set/rolled/revoked through its API, granted to named principals, and delivered to plugins over stdin — never sitting in a file you edit. Use config encryption for operator-managed static tokens; use the Vault for secrets with a lifecycle (rotation, revocation, per-plugin scoping).

---

## 1. Encryption at Rest

Ductile can read [age](https://age-encryption.org)-encrypted config include files. The gateway detects encryption by content (the age header), decrypts in memory at config load — **before** `${ENV}` interpolation — and never writes plaintext to disk. Because detection is content-based, an encrypted file keeps its normal name (e.g. `tokens.yaml`); nothing else in your config has to change.

### What to encrypt

- **Encrypt** the high-security YAML includes — typically `tokens.yaml`, and any `webhooks.yaml` or plugin include that carries secrets. (Scope files loaded by reference, e.g. `scopes/*.json`, are read on a separate path and are not decrypted at load — keep them plaintext for now.)
- **Keep the root `config.yaml` plaintext.** It names the key file, so it has to be readable before any decryption can happen.

### Workflow: keygen → encrypt → deploy key → load

```bash
# 1. Generate an age identity for a host. The private identity goes to the
#    key file at mode 0600; the public recipient (age1...) prints to stderr.
ductile secrets keygen --out ~/.config/ductile/age.key
# age1qz9... (public recipient — copy this)

# 2. Encrypt tokens.yaml to that recipient (output is armored).
ductile secrets encrypt --recipient age1qz9... \
  --in tokens.yaml --out tokens.yaml.enc
mv tokens.yaml.enc tokens.yaml

# 3. Re-lock — the encrypted file's hash differs from the plaintext.
ductile config lock

# 4. Start normally. The gateway finds the key, decrypts in memory, then
#    interpolates ${ENV} and parses YAML as usual.
ductile system start
```

### Multi-host recipients

age supports multiple recipients in one bundle. Encrypt to **one public key per host** so that a leaked key on one machine does not decrypt the bundle everywhere. Each host keeps only its own private key file.

```bash
ductile secrets encrypt \
  --recipient age1homeprimary... \
  --recipient age1lab... \
  --recipient age1vpsbackup... \
  --in tokens.yaml --out tokens.yaml
```

A recipients file (one `age1...` per line) keeps long lists out of the command:

```bash
ductile secrets encrypt --recipients-file recipients.txt \
  --in tokens.yaml --out tokens.yaml
```

### Key file resolution order

The age identity (private key) is read from the first source that resolves:

1. `DUCTILE_AGE_KEY_FILE` environment variable
2. `secrets.age_key_file` in `config.yaml` (relative paths resolve against the config dir)
3. Default locations, in order: `<configdir>/age.key`, then `~/.config/ductile/age.key`

```yaml
# config.yaml (stays plaintext)
secrets:
  age_key_file: age.key   # relative → resolved against the config dir
```

### Key file permissions

The key file **must** have mode `0600` (no group or other access) or load fails. Set it explicitly after generating or copying a key to another host:

```bash
chmod 600 ~/.config/ductile/age.key
```

### Failure modes

| Situation | Behavior |
| :--- | :--- |
| Explicitly-named key (env var or `age_key_file`) is missing or has loose permissions | **Hard fail** — the gateway refuses to start. |
| No key configured, no default key file exists | Encryption at rest is **off**; plaintext config loads normally. |
| Encrypted file present but key cannot decrypt it | **Hard fail** at load. |

### Rotating recipients

`secrets rotate` decrypts an encrypted file with the current key and re-encrypts it in place under a new recipient set. The write is atomic, and the original input is preserved on failure.

```bash
ductile secrets rotate --key ~/.config/ductile/age.key \
  --recipient age1homeprimary... \
  --recipient age1newlab... \
  --file tokens.yaml
ductile config lock   # the file changed; re-lock
```

Use rotation when adding or removing a host, or after retiring a compromised host key.

---

## 2. `ductile secrets` Command Reference

| Command | Purpose |
| :--- | :--- |
| `secrets keygen` | Generate a new age identity. |
| `secrets encrypt` | Encrypt plaintext to one or more recipients. |
| `secrets rotate` | Re-encrypt an existing file under a new recipient set. |

### `ductile secrets keygen`

```
ductile secrets keygen [--out PATH]
```

- Writes the private identity to `--out` at mode `0600`, or to stdout if `--out` is omitted.
- Prints the public recipient (`age1...`) to **stderr**, so you can capture the identity on stdout while still seeing the recipient.

### `ductile secrets encrypt`

```
ductile secrets encrypt --recipient age1... [--recipient ...] \
  [--recipients-file PATH] [--in PATH] [--out PATH]
```

- Encrypts plaintext from `--in` (or stdin) to the given recipients; output is armored.
- `--recipient` may be repeated; `--recipients-file` reads one `age1...` per line. Both may be combined.
- Writes to `--out` (or stdout).

### `ductile secrets rotate`

```
ductile secrets rotate --key PATH --recipient age1... [...] \
  [--recipients-file PATH] --file PATH
```

- Decrypts `--file` with the identity in `--key`, then re-encrypts it **in place** under the new recipient set.
- Atomic write; the input file is preserved if anything fails.

> **Note on `config show`:** the age key path shown in config views is a filesystem path, not the key material, so it is **not** redacted. The actual token values in `tokens.yaml` **are** redacted in `config show` output and in backup snapshots.

---

## 3. The Vault

The **Vault** is Ductile's owned secret store: a single whole-store age blob, held
decrypted in memory at runtime by the daemon. Unlike the encrypted config bundles
above (static files you author and re-lock), the Vault is a *running store* — secrets
are created, rolled, and revoked through its API and delivered to plugins at dispatch
time. It is the home for secrets that have a lifecycle.

### Credential ladder

Three credentials, three planes, each strictly scoped — and each **issues the one below it**. This is
the spine of the whole vault story (full rationale: [ADR: Vault credential ladder](adr/vault-credential-ladder.md)).

| Credential          | Verb                  | Plane                  | Cannot                            |
|---------------------|-----------------------|------------------------|-----------------------------------|
| **vault key** (age) | **open** the vault    | offline, root of trust | — (it is the floor)               |
| **admin token**     | **operate** the vault | online `/vault/*`      | open the vault; operate ductile   |
| **api token**       | **operate** ductile   | online gateway         | operate the vault; open the vault |

```
vault key  ──mints──▶  admin token  ──mints──▶  api token
 (genesis, offline)   (vault init / rotate)     (vault set, online)
```

Isolation runs upward-blocking only: an api token can't reach the vault, an admin token can't reach the
key — but the key subsumes all (`api ⊂ admin-reachable-state ⊂ key`). The **admin token is mint-only**:
its single writer is `RotateAdminToken` (age-key, daemon stopped), the value is always machine-minted,
and `set` is refused on every plane. **api tokens** are ordinary secrets — settable to a chosen value via
`vault set`. Bootstrap is just the ladder: genesis (key) mints the admin token, which operates the vault
to mint the api token (see [DEPLOYMENT.md § 11](DEPLOYMENT.md)).

### Mental model

- **Principal** — a registered deliver-to identity: a `plugin`, a `consumer`, or the
  `gateway`. Secrets are granted to principals by name.
- **Secret** — a named value with `authorized_principals` (who may receive it), a
  `pattern` (`manual` = operator-supplied, `auto` = daemon-minted from a CSPRNG), and
  an immutable value (a `roll` supersedes it; there is no version history). Revocation
  is terminal and clears the value.
- **`set` is a partial update, not a value editor.** On an existing secret, `set`
  updates only metadata/grants: omit `--principal` to **leave** grants untouched, pass
  an empty `--principal ""` to **clear** them, or pass a list to replace them. A value
  *change* is refused — the value is roll-only, so `roll` is the single audited
  (`roll_count`) path and `set` can't be a side door around it. (An active `manual`
  secret also cannot be created with an empty value; `auto` secrets are minted on first
  `roll`.)
- **Compose** — at dispatch, the daemon resolves the calling principal's authorized,
  active secrets and delivers them in the plugin's request **`secrets`** map (a field
  distinct from `config`) over **stdin** — never via the environment or argv. Compose
  is **fail-closed**: an unknown principal, a revoked secret, or a failed plugin
  attestation yields no delivery. **Freshness asymmetry:** plugin secrets compose
  **fresh per spawn** (a `roll` is visible on the next dispatch); webhook/relay
  `secret_ref`s instead **freeze at boot** and need `ductile system reload` to pick up
  a roll (see [OPERATOR_GUIDE.md](OPERATOR_GUIDE.md)).

### Sole-writer model

The daemon alone holds the key and the in-memory model, so it is the **sole writer**.
That splits the CLI into two classes (see `ductile vault --help`):

- **Keyless API clients** — `set`, `roll`, `revoke`, `revoke-principal`,
  `purge-principal`, `roll-principal`, `register-principal`. These hold no age key and
  decrypt nothing; they POST to the daemon's authenticated management API with the
  vault admin token (`--token` or `DUCTILE_VAULT_TOKEN`). They can run any time.
- **Local, key-touching ops** — `init`, `rotate-key`, `rotate-admin-token`.
  These read the age key directly and operate on the blob, so the daemon must be
  **stopped**: `rotate-key` and `rotate-admin-token` refuse via the PID lock if it
  is running; `init` runs before any daemon exists and instead refuses to overwrite
  an existing vault. There is no offline
  bulk `import`: the daemon is the sole writer, so secrets enter the vault through
  it — `vault set` over the management API (`--api-url`, incl. `unix://` in the
  vault-operable bootstrap posture).

### Genesis and lifecycle

> **Deploying onto a real instance?** This section is the lifecycle model. The full,
> ordered first-time deploy procedure — backup, `vault_audit` migration, genesis, reconcile
> config.yaml, mint secrets over the admin socket (vault-operable posture), `config lock`
> **and** `plugin lock --all`, cutover, verify — is the how-to in
> [DEPLOYMENT.md § 11](DEPLOYMENT.md).

```bash
# 1. Genesis: create a new vault. Seeds the core principal, the fingerprint nonce,
#    and a one-time admin token (printed once — store it; it is the API credential).
ductile vault init --vault vault.age --key ~/.config/ductile/age.key

# 2. Register a plugin as a deliver-to principal.
ductile vault register-principal --api-url http://127.0.0.1:8080 \
  --token "$DUCTILE_VAULT_TOKEN" --name withings --kind plugin

# 3. Set a secret and grant it to that principal (value from stdin, never argv).
printf '%s' "$API_TOKEN" | ductile vault set --api-url http://127.0.0.1:8080 \
  --token "$DUCTILE_VAULT_TOKEN" --name withings_api --pattern manual --principal withings

# 4. Roll (supersede the value) or revoke (terminal) over the same API.
ductile vault roll   --api-url ... --token ... --name withings_api
ductile vault revoke --api-url ... --token ... --name withings_api
```

The plugin receives `withings_api` in its request `secrets` map at dispatch — it never sees
the vault, the key, or other principals' secrets.

### Rotating the vault key

`ductile vault rotate-key` rotates the daemon's age identity (mints a fresh key,
re-encrypts the store, retires the old key). It is local and key-touching — the daemon
must be down. The full crash-safe procedure and the **back-up-the-key** discipline are
in [OPERATOR_GUIDE.md](OPERATOR_GUIDE.md) § "Rotating the vault key". Note: `secrets
rotate` (above) is for config bundles and must **not** be pointed at `vault.age`.

### Rotating the admin token

The genesis admin token (`core-admin-token`) is the management-API credential — it is
printed **once** at `vault init` and authenticates every `/vault/*` write. If it is
exposed (captured from `genesis.out`, leaked to a client log), roll it **in place** with:

```bash
# Stop the daemon via its service manager (there is no `system stop` command):
#   systemd: systemctl --user stop ductile-local
#   launchd: launchctl bootout gui/$UID/<label>    (foreground runs: Ctrl-C)
ductile vault rotate-admin-token --config "$CFG"     # mints + prints the NEW token once
ductile system start                                 # or restart via the service manager
```

It mints a fresh CSPRNG token, persists the blob, and prints the new value once to
**stdout** (notices go to stderr; capture it: `T=$(ductile vault rotate-admin-token …)`).
The **old token stops authenticating immediately** — update `DUCTILE_VAULT_TOKEN` and any
API client before the next write. The op is recorded in `vault_audit`
(`op=rotate-admin-token`, **value never logged**); it does **not** re-genesis the vault or
touch the age key, and the token is never grantable to a principal.

Rotation surfaces a secret value, so — like `vault get` and `init` — it is a **local,
key-touching** op, never over the API: the management API stays value-free (it never
emits a secret value over HTTP). There is deliberately no API route for this.

### Plugin attestation (secret delivery is gated)

Before a plugin receives secrets it must be **attested**: `ductile plugin lock <name>`
records a keyed-BLAKE3 fingerprint of the plugin's bytes, keyed by the vault nonce. At
compose time the daemon re-verifies the live plugin against that fingerprint and
**refuses delivery (and raises a security event) on a mismatch** — so a swapped or
tampered plugin cannot receive secrets. Attestation is decoupled from `config lock`
(which locks config files only). See [OPERATOR_GUIDE.md](OPERATOR_GUIDE.md) and
`ductile plugin lock --help`.

### Backup and restore

`ductile system backup` (scope `config` or higher) includes the encrypted vault blob
(`vault.age`) in the archive, so a restore is not secret-less. The age key that decrypts
it is **deliberately excluded** — the archive already carries the `api.yaml` bearer token
and env secrets, so shipping the key alongside it would make the archive a single-file
compromise. The blob and its key are a **pair**, custodied apart:

- **`vault.age` → in the archive** (an opaque encrypted file).
- **the age key → out-of-band**, saved by you (e.g. a password manager). The
  `BACKUP_MANIFEST.txt` records the key as excluded with this pairing note.

Each backup also stamps a short **pairing UID** (5 chars, e.g. `K7P2Q`): printed
on completion, written to `uid.txt` at the archive root, and recorded as
`backup_uid` in `BACKUP_MANIFEST.txt`. Save this UID **next to the age key** in
your password manager — once `vault rotate-key` has produced more than one key,
the UID is how you tell which archive a given key unlocks.

Restore:

```bash
# 1. Unpack the archive (vault.age lands back in the config dir).
tar -xzf ductile-backup.tar.gz -C /path/to/restore
# 2. Write the age key file back from out-of-band custody (mode 0600), to the
#    path secrets.age_key_file / DUCTILE_AGE_KEY_FILE resolves to.
install -m600 /dev/stdin "$DUCTILE_AGE_KEY_FILE" <<< "AGE-SECRET-KEY-1..."
# 3. Start the daemon. It finds the key, decrypts vault.age in memory, and serves.
```

A vault backup is restorable **only** with the key that was current when it was taken —
and `rotate-key` destroys the old key, so save the freshly minted key immediately after
each rotation, or a pre-rotation backup becomes unreadable.

---

## 4. Spawn Hygiene: Plugin Environment Allowlist

Plugin child processes **no longer inherit the gateway's full environment.** They receive only a fixed allowlist:

```
PATH  HOME  TZ  LANG  LANGUAGE  LC_ALL  LC_*  TMPDIR
```

plus any names the operator explicitly grants. Secrets reach plugins **only** over the stdin protocol request (the request's `secrets` map, distinct from `config`) — never via the environment, never via argv.

### Granting extra variables

Add variable names to `service.plugin_env_passthrough` in `config.yaml`:

```yaml
service:
  plugin_env_passthrough:
    - HTTPS_PROXY
    - MY_TOOL_HOME
```

Only the listed names are passed through, and only if they are set in the gateway's environment.

### Behavior change to be aware of

!!! warning "A plugin that read a secret or value from an inherited environment variable will no longer see it"
    Before spawn hygiene, plugins inherited the gateway's full environment. Now a variable that is not on the allowlist (and not granted via `plugin_env_passthrough`) is **invisible** to the plugin. If a plugin previously relied on an inherited env var, add that name to `service.plugin_env_passthrough`, or move the value into the plugin's `config` map (delivered over stdin).

In this repo, `plugins/sys_exec` is the one plugin that reads the process environment, for its own `$VAR` command-string expansion. Under the allowlist, that expansion sees only allowlisted and operator-granted variables — which is the intended containment: untrusted command strings can no longer expand arbitrary gateway secrets.

---

## See also

- [CONFIG_REFERENCE.md](CONFIG_REFERENCE.md) — directory model, integrity preflight, env interpolation order.
- [OPERATOR_GUIDE.md](OPERATOR_GUIDE.md) — `config lock`/`check`, strict mode, day-to-day operations.
- [PLUGIN_DEVELOPMENT.md](PLUGIN_DEVELOPMENT.md) — how secrets reach plugins via the request's `secrets` map over stdin.
