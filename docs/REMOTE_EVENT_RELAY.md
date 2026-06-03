# Remote Event Relay

Remote Event Relay lets one Ductile instance deliver an event to another Ductile instance over authenticated HTTP.

Phase 1 is intentionally narrow:
- point-to-point relay between named instances
- HMAC-authenticated HTTP ingress
- receiver-side local enqueue and local exact-match routing
- at-least-once delivery

It is not:
- clustering
- shared queueing
- shared state
- remote route discovery
- pub/sub or broker semantics

---

## What Happens

1. Instance `home-primary` sends an event to named instance `lab`.
2. `lab` validates the trusted peer, timestamp, key id, signature, and envelope.
3. `lab` accepts the event as a fresh local root ingress event.
4. `lab` enqueues local work and applies its own local routing.

The important boundary is step 3. After acceptance, the receiver owns all further processing.

---

## Config Layout

Recommended files:

```text
~/.config/ductile/
├── config.yaml
├── tokens.yaml
├── relay-instances.yaml
├── relay-ingress.yaml
└── pipelines.yaml
```

The shared HMAC secrets referenced by `secret_ref` resolve against the **vault** (the source of truth — `ductile vault set`), or a legacy `tokens.yaml` entry during migration. See `docs/SECRETS.md`.

---

## Sender Example

`config.yaml`

```yaml
include:
  - tokens.yaml
  - relay-instances.yaml
  - pipelines.yaml

service:
  name: home-primary
  tick_interval: 60s
  log_level: info

plugin_roots:
  - /opt/ductile/plugins

api:
  enabled: true
  listen: 127.0.0.1:8080

state:
  path: ./data/state.db
```

`tokens.yaml`

```yaml
tokens:
  - name: relay-lab-v1
    key: ${RELAY_LAB_V1_SECRET}
    scopes_file: scopes/relay-admin.json
    scopes_hash: blake3:1111111111111111111111111111111111111111111111111111111111111111
```

`relay-instances.yaml`

```yaml
instances:
  - name: lab
    enabled: true
    base_url: https://lab.example
    ingress_path: /ingest/peer/home-primary
    secret_ref: relay-lab-v1
    key_id: v1
    timeout: 10s
    allow:
      - backup.ready
```

---

## Receiver Example

`config.yaml`

```yaml
include:
  - tokens.yaml
  - relay-ingress.yaml
  - pipelines.yaml

service:
  name: lab
  tick_interval: 60s
  log_level: info

plugin_roots:
  - /opt/ductile/plugins

api:
  enabled: true
  listen: 127.0.0.1:8080

state:
  path: ./data/state.db
```

`tokens.yaml`

```yaml
tokens:
  - name: relay-lab-v1
    key: ${RELAY_LAB_V1_SECRET}
    scopes_file: scopes/relay-admin.json
    scopes_hash: blake3:1111111111111111111111111111111111111111111111111111111111111111
```

`relay-ingress.yaml`

```yaml
remote_ingress:
  listen_path: /ingest/peer
  max_body_size: 1MB
  allowed_clock_skew: 5m
  require_key_id: true
  peers:
    - name: home-primary
      enabled: true
      secret_ref: relay-lab-v1
      key_id: v1
      accept:
        - backup.ready
      baggage:
        allow:
          - trace_id
```

`pipelines.yaml`

```yaml
pipelines:
  - name: process-offsite-backup
    on: backup.ready
    steps:
      - id: verify-backup
        uses: backup-verifier
      - id: store-backup
        uses: cold-storage-sync
```

---

## End-to-End Example

Expected flow:

1. `home-primary` emits or prepares `backup.ready`.
2. `home-primary` signs and `POST`s the relay envelope to `lab`.
3. `lab` accepts `backup.ready` from peer `home-primary`.
4. `lab` enqueues local jobs for `process-offsite-backup`.
5. `lab` runs `backup-verifier` and `cold-storage-sync` according to its own local config.

CLI example:

```bash
ductile relay send lab \
  --event backup.ready \
  --payload '{"archive_path":"/srv/backups/latest.tar.zst","archive_id":"nightly-2026-05-03"}' \
  --dedupe-key backup.ready:nightly-2026-05-03 \
  --origin-plugin backup-runner \
  --origin-job-id job-123 \
  --origin-event-id evt-456 \
  --baggage '{"trace_id":"tr-789"}'
```

Wire shape:

```json
{
  "event": {
    "type": "backup.ready",
    "payload": {
      "archive_path": "/srv/backups/latest.tar.zst",
      "archive_id": "nightly-2026-05-03"
    },
    "dedupe_key": "backup.ready:nightly-2026-05-03"
  },
  "origin": {
    "instance": "home-primary",
    "plugin": "backup-runner",
    "job_id": "job-123",
    "event_id": "evt-456"
  },
  "baggage": {
    "trace_id": "tr-789"
  }
}
```

Headers:
- `X-Ductile-Peer`
- `X-Ductile-Key-Id`
- `X-Ductile-Timestamp`
- `X-Ductile-Signature`

The signature covers:
- HTTP method
- request path
- timestamp
- raw request body

---

## Operational Notes

- Operator-facing instance and peer names should be lower-case hyphenated, for example `home-primary` or `vps-backup`.
- Event types remain lower-case dotted, for example `backup.ready`.
- `remote_ingress.listen_path` is mounted on the main HTTP server and therefore uses `api.listen`.
- `secret_ref` must resolve to a secret on both sides — held in the vault (preferred) or a legacy `tokens.yaml` entry.
- `peers[].accept` and `instances[].allow` are optional policy filters, not distributed routing rules.
- Remote baggage is not trusted wholesale. Only keys listed in `peers[].baggage.allow` may seed new local root context.

---

## Failure Semantics

- If delivery fails before acceptance, the sender owns the failure.
- If the receiver accepts the event and downstream work later fails, the receiver owns that failure.
- Delivery remains at-least-once. Duplicate safe behavior still matters.

---

## What To Check When It Fails

- `service.name` matches the sender identity used on the wire.
- `secret_ref` resolves to the same shared secret on both sides.
- `key_id` matches if `require_key_id: true`.
- `allowed_clock_skew` is large enough for the two clocks.
- `accept` includes the event type being relayed.
- `api.listen` is reachable at the receiver.
