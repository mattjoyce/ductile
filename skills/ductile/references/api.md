# Ductile: REST API Reference

## Base URL & Auth

Default: `http://localhost:8081` (local prod) or `http://localhost:9001` (test)

```http
Authorization: Bearer <token>
```

Unauthenticated endpoints: `/healthz`, `/plugins`, `/skills`, `/openapi.json`, `/.well-known/ai-plugin.json`

**Vault routes use a separate credential.** `POST /vault/*` (below) authenticate against the **vault-resident admin token** (`DUCTILE_VAULT_TOKEN`, minted by `vault init`), NOT the `api.auth.tokens` above — config tokens (even `*`) are rejected there, and vice-versa.

## Endpoints

### Trigger Plugin Directly (bypasses routing)
```
POST /plugin/{plugin}/{command}
```
Body: `{"payload": {"key": "value"}}`

Response 202:
```json
{"job_id": "uuid", "status": "queued", "plugin": "name", "command": "poll"}
```

Required scope: `plugin:ro` (read commands) or `plugin:rw` / `*` (write commands)

### Trigger Pipeline
```
POST /pipeline/{pipeline}
```
Body: `{"payload": {"key": "value"}}`
Query: `?async=true` (force async for synchronous pipelines)

Response 202 (async):
```json
{"job_id": "uuid", "status": "queued"}
```

Response 200 (synchronous pipeline, completed):
```json
{
  "job_id": "uuid", "status": "succeeded", "duration_ms": 1250,
  "result": {"status": "ok"},
  "tree": [{"job_id": "uuid", "plugin": "name", "command": "poll", "status": "succeeded"}]
}
```

Response 202 (synchronous pipeline, timeout exceeded):
```json
{"job_id": "uuid", "status": "running", "timeout_exceeded": true}
```

### Get Job Status
```
GET /job/{job_id}
```
Scope: `jobs:ro` or `*`

Job statuses: `queued` | `running` | `succeeded` | `failed` | `timed_out` | `dead`

### List Jobs
```
GET /jobs?plugin=name&command=poll&status=succeeded&limit=50
```
Scope: `jobs:ro` or `*`. Results sorted by `created_at` desc.

Status aliases: `pending`→`queued`, `ok`→`succeeded`, `error`→`failed`

### Query Job Logs
```
GET /job-logs?plugin=name&status=failed&from=2026-01-01T00:00:00Z&limit=50
```
Scope: `jobs:ro` or `*`

Parameters: `job_id`, `plugin`, `command`, `status`, `submitted_by`, `from`, `to` (RFC3339), `query` (full-text search), `limit` (max 200), `include_result`

### Health Check
```
GET /healthz
```
No auth required.

```json
{"status": "ok", "uptime_seconds": 3600, "queue_depth": 0, "plugins_loaded": 5, "version": "0.1.0-dev"}
```

### Plugin Discovery
```
GET /plugins                    # No auth — list all plugins (name, version, commands)
GET /plugin/{name}              # Scope: plugin:ro — full manifest with input schemas
GET /plugin/{name}/openapi.json # No auth — OpenAPI 3.1 for one plugin
GET /openapi.json               # No auth — global OpenAPI 3.1 spec
```

### Skills Index (LLM discovery)
```
GET /skills                     # No auth — unified skill manifest
```

Returns both plugin commands and named pipelines with endpoints, tiers, and schemas.

### AI Plugin Discovery
```
GET /.well-known/ai-plugin.json  # No auth — OpenAI-style discovery
```

### Vault Management (admin-token auth; values never returned)
```
POST /vault/principal          # {name, kind: plugin|consumer|gateway}   register a deliver-to identity
POST /vault/secret             # {name, value, authorized_principals[], pattern: manual|auto}  upsert
POST /vault/secret/roll        # {name, value?}   supersede value (auto: minted)
POST /vault/secret/revoke      # {name}           terminal; clears the value
POST /vault/principal/revoke   # {name}           stop delivery (fail closed)
POST /vault/principal/purge    # {name}           remove + strip grants
POST /vault/principal/roll     # {name} -> {rolled[], skipped[]}   roll its auto secrets
```
All return `{name, status}` (or the roll-principal shape) — never a secret value. `503` if no vault loaded, `401` on a bad admin token. CLI clients: `ductile vault set|roll|revoke|register-principal|...`. Audit: `ductile system vault-audit`.

## Error Codes
- `401` — Missing or invalid token
- `403` — Valid token, insufficient scope
- `404` — Plugin / command / job not found
- `400` — Invalid JSON or missing fields
- `500` — Internal server error

JSON error format: `{"error": "...", "code": 78, "context": {...}}`

## curl Examples

```bash
# Health check
curl http://localhost:8081/healthz

# Trigger pipeline
curl -X POST http://localhost:8081/pipeline/discord-fabric \
  -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"payload": {"url": "https://example.com"}}'

# Trigger plugin directly
curl -X POST http://localhost:8081/plugin/echo/poll \
  -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN" \
  -d '{"payload": {"message": "hello"}}'

# List recent failed jobs
curl "http://localhost:8081/jobs?status=failed&limit=10" \
  -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN"

# Get job status
curl http://localhost:8081/job/<job_id> \
  -H "Authorization: Bearer $DUCTILE_LOCAL_TOKEN"

# Get skills manifest
curl http://localhost:8081/skills
```
