# CachyOS / Arch Cookbook — install an enforcing gateway

> **Diátaxis register: _how-to_.** Choose a posture in
> [Deployment Postures](DEPLOYMENT_POSTURES.md) before you read this page. This page gives only the
> steps that are different on an Arch host, and the order that satisfies the admission gates. For
> the platform-neutral procedure, see [Deployment §5b](DEPLOYMENT.md).
>
> **Language:** this page uses ASD-STE100 Simplified Technical English. Sentences are short. Each
> instruction is one sentence. The imperative mood shows an action you must do.

Validated on `cachyos-b550` on 2026-08-22, with ductile v1.0.1 and the hybrid trust-tier posture.
The result was one cap-only gateway, three accounts, and a confined `echo` plugin on the API. All
wall-bite checks passed.

An Arch host is different from a Debian host in three ways. Each difference stops the first boot.
Do the checks in section 1 before you install.

---

## 1. Preflight checks

Do all three checks. Do not skip a check.

### 1a. Check for uid and gid collisions

`deploy/systemd/ductile-accounts.sysusers.conf` sets the gateway to uid 984 and gid 984. On CachyOS,
gid 984 is the `uucp` group. The `uucp` group controls the serial ports.

Show the owner of each id:

```bash
for id in 984 1001 1002; do
  printf 'id %-5s ' "$id"
  { getent passwd "$id" || true; getent group "$id" || true; } | paste -sd' ' || echo FREE
done
```

A `u` line with one number makes a user with that uid and a group with that gid. If the gid is in
use, `systemd-sysusers` cannot make the group.

!!! danger "Do not add the gateway to the group that holds the id"
    If you add `ductile` to `uucp`, every plugin gets read access and write access to
    `/dev/ttyUSB*`. The ductile config does not show this access. Move the id instead.

Select an unused id in the system range. Then change the file:

```bash
# 951 was free on this host. Check your host. Do not copy this number.
sed -i -E 's/^(u[[:space:]]+ductile[[:space:]]+)984/\1951/' \
  deploy/systemd/ductile-accounts.sysusers.conf
mkdir -p ~/ductile-host-patches
git diff deploy/systemd/ductile-accounts.sysusers.conf > ~/ductile-host-patches/sysusers-uid.patch
```

Change only the sysusers fragment. `ductile-accounts.tmpfiles.conf` and `ductile.service` use the
account names, not the numbers.

`deploy/install.sh` installs the fragment from the working tree. Apply the patch again after each
`git pull`.

### 1b. Make the systemd drop-in directories

`deploy/install.sh` writes to `/etc/sysusers.d` and `/etc/tmpfiles.d`. A new CachyOS host does not
have `/etc/sysusers.d`. The install stops with this error:

```
install: cannot create regular file '/etc/sysusers.d/ductile-accounts.conf': No such file or directory
```

Make both directories:

```bash
sudo install -d -o root -g root -m0755 /etc/sysusers.d /etc/tmpfiles.d
```

### 1c. Check the ports and the ACL support

```bash
ss -ltn | grep -E ':8081|:8091'     # Both ports must be free.
command -v setfacl                  # The trusted tier needs setfacl.
findmnt -no FSTYPE -T "$HOME"       # btrfs and ext4 give ACL support.
```

Arch links `/usr/sbin` to `/usr/bin`. Therefore `/usr/sbin/nologin` is correct in the sysusers
fragment. Do not change the shell field.

---

## 2. Order of operations with the admission gates on

[BOOTSTRAP.md](BOOTSTRAP.md) puts `plugin lock` at step 7, after the first start. That order is
correct only when the admission gates are off. The example `config/config.yaml` sets
`verify_integrity_on_boot: true`. With that setting, the bootstrap order causes a restart loop:

```
integrity check failed (admission.verify_integrity_on_boot)
  config reload rejected: plugin fingerprints missing from .checksums;
  run 'ductile plugin lock --all' to authorize configured plugins
```

When the gates are on, do both locks before the first start. Use this order:

| Step | Command | Reason |
|---|---|---|
| 1 | `secrets keygen` | Make secret zero first. |
| 2 | `vault init` | Attestation uses the vault nonce. Genesis must come before a lock. |
| 3 | `config lock` | Writes `.checksums`. `plugin lock` adds to this file. |
| 4 | `plugin lock --all` | The integrity gate refuses to start without the fingerprints. |
| 5 | `config check` | The scope files have checksums. This step passes only after the locks. |
| 6 | Start the service | The gateway starts in the management posture. |
| 7 | `vault set core-api-token` | Mint the token on the management socket. |
| 8 | Add `auth.tokens`, `config lock`, restart | The gateway starts in the gateway posture. |

Steps 3 to 5 are safe to repeat. Do these steps again after you change the config.

---

## 3. Install the hybrid trust-tier posture

### 3a. Install the package layer

```bash
sudo install -d -m0755 /etc/sysusers.d /etc/tmpfiles.d
sudo BIN_SRC=/path/to/ductile bash deploy/install.sh
id ductile ductile-default ductile-untrusted
```

Make sure the ids are the ids you selected in step 1a.

### 3b. Install the confined plugin code

```bash
sudo cp -a plugins/echo plugins/_lib /opt/ductile/plugins/
sudo chown -R root:root /opt/ductile/plugins
sudo chmod -R a+rX,go-w /opt/ductile/plugins
```

Root owns this code. The gateway cannot change it. The accounts cannot change it.

### 3c. Write the config

Write `/etc/ductile/config.yaml`. The owner must be `ductile`. The mode must be `0600`.

```yaml
secrets:
  age_key_file: /etc/ductile/secret/age.key
  vault_file: /etc/ductile/vault.age

state:
  path: /var/lib/ductile/ductile.db

plugin_roots:
  - /opt/ductile/plugins          # Confined code.
  - /home/YOU/ductile/plugins     # Trusted code. An ACL gives access.

accounts:
  default:   { uid: 1001, gid: 1001, state_dir: /var/lib/ductile/accounts/default }
  untrusted: { uid: 1002, gid: 1002, state_dir: /var/lib/ductile/accounts/untrusted }
  trusted:   { uid: 1000, gid: 1000, home: /home/YOU }   # `home:` selects credentialed mode.
```

The same uid, gid, and state_dir values are in three files. The three files must agree. The gateway
verifies them at boot and refuses to start if they disagree.

### 3d. Set the ACL for the trusted tier

The ACL gives the gateway read access to the plugin code only. It does not give access to your
credentials. It does not give access to the confined accounts.

```bash
sudo setfacl -m  g:ductile:x  /home/YOU
sudo setfacl -R -m g:ductile:rX /home/YOU/ductile/plugins
sudo setfacl -d -m g:ductile:rX /home/YOU/ductile/plugins   # New files get the same ACL.
```

Test the ACL. The first command must pass. The other two commands must fail.

```bash
sudo -u ductile test -x /home/YOU
sudo -u ductile ls /home/YOU
sudo -u ductile cat /home/YOU/.ssh/id_ed25519
```

### 3e. Run the credential ladder

Do the steps in section 2 in that order.

---

## 4. The side-door audit on a cap-only gateway

At boot, ductile examines each account for a route to root. One test runs `sudo -n -l -U <account>`.
Only a privileged user can run this command. The gateway holds `CAP_SETUID` and `CAP_SETGID` only.
Therefore the gateway cannot run this test.

The same command gives two different results:

```bash
# As root. The result is definitive.
$ LC_ALL=C sudo -n -l -U ductile-default
User ductile-default is not allowed to run sudo on <host>.     # exit 0

# As the gateway user. This is the result the daemon gets.
$ LC_ALL=C sudo -n -l -U ductile-default
sudo: a password is required                                    # exit 1
```

`sidedoor_audit_unix.go` accepts three strings as a clean negative result: `not allowed to run
sudo`, `is not allowed`, and `Sorry, user`. The second result matches none of them. Therefore the
audit reports an inconclusive result. In strict mode, `sidedoor_audit.go` changes an inconclusive
result for a confined account into a boot refusal:

```
privsep side-door audit: confined account(s) default, untrusted hold a host root side-door
(or could not be verified) and strict mode is on — refusing to boot a wall that cannot contain them
```

The gateway cannot satisfy this gate. The test needs privileges that the gateway does not have.

Get the result from root. Record the result. Then make the override conditional on that record.

```bash
for acct in ductile-default ductile-untrusted; do
  LC_ALL=C sudo -n -l -U "$acct" | tee -a /etc/ductile/secret/sidedoor-proof.txt
  LC_ALL=C sudo -n -l -U "$acct" | grep -q "not allowed to run sudo" \
    || { echo "ABORT: $acct is NOT provably free of a sudo side-door"; exit 1; }
done
sudo chown ductile:ductile /etc/ductile/secret/sidedoor-proof.txt
sudo chmod 0600 /etc/ductile/secret/sidedoor-proof.txt
```

If the loop aborts, do not continue. An account has a real side-door. Correct it first.

If the loop completes, set `admission.fail_on_sidedoor: false`. Add a comment that names the proof
file. The audit continues to run. The audit continues to write log entries. Only the refusal stops.

Keep all other gates on: `verify_integrity_on_boot`, `fail_on_drift`, `validate_config_on_boot`,
and `require_api_auth`.

!!! note "The credentialed tier is different"
    A `trusted` account writes an informed-consent log entry and starts. There is no wall on that
    tier. Therefore there is nothing to fail closed.

---

## 5. Verify the wall

A healthy API does not show that the wall works. Run these tests. The first four commands must fail
with `Permission denied`.

```bash
sudo -u ductile-default cat /etc/ductile/secret/age.key
sudo -u ductile-default ls /var/lib/ductile/accounts/untrusted
sudo -u ductile-default ls /home/YOU
sudo -u ductile-untrusted cat /home/YOU/.ssh/id_ed25519
```

Then check the service:

```bash
curl -s localhost:8081/healthz                                   # Shows "posture":"gateway".
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/plugins  # Shows 401.
```

Do not run `systemctl enable` until all four denials pass.

---

## 6. Known CLI behaviour

### Put the flags before the plugin name

`plugin run` uses the Go `flag` package. The parser stops at the first argument that is not a flag.
The usage message shows the plugin name first. That order does not work. The command prints the
usage message and gives no error message.

```bash
ductile plugin run echo --command=handle          # Fails. Prints usage.
ductile plugin run --command=handle echo          # Correct.
```

### Set the API URL

The default API URL is `http://localhost:8080`. The config examples use port 8081. Set the URL:

```bash
ductile plugin run --command=handle --api-url=http://127.0.0.1:8081 --api-key="$TOK" echo
```

### Read job results through the API

`job inspect` reads `/etc/ductile`. The owner is `ductile` and the mode is `0700`. Your user gets an
EACCES error. Use the API, or run the command with `sudo -u ductile`.

---

## 7. Remove or roll back

`deploy/install.sh` installs the directory structure, the binary, and the unit. It does not write
the config. It does not write the vault. It does not write the plugin code. It does not change the
owner of `/var/lib/ductile`.

To go back to the previous binary:

```bash
sudo systemctl disable --now ductile
sudo cp ~/backups/ductile-prev /usr/local/bin/ductile
sudo systemctl start ductile
```

The accounts stay. `/etc/ductile` stays.

To remove the gateway from the host:

```bash
sudo systemctl disable --now ductile
sudo rm /etc/systemd/system/ductile.service
sudo rm /etc/sysusers.d/ductile-accounts.conf /etc/tmpfiles.d/ductile-accounts.conf
sudo userdel ductile && sudo userdel ductile-default && sudo userdel ductile-untrusted
sudo rm -rf /etc/ductile /var/lib/ductile /opt/ductile
```

!!! warning "Save the vault before you remove the directories"
    `/etc/ductile` contains the age key and the vault. You cannot decrypt the vault without the age
    key. Copy both files to secure storage first.
