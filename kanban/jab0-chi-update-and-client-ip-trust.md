---
id: jab0
status: todo
priority: High
tags: [dependencies, security, api, webhook]
---

# Update chi and make client-IP trust explicit

Retain chi, as agreed when closing card 148. Ductile currently pins v5.2.5 and uses middleware.RealIP in internal/api/server.go and internal/webhook/server.go. Published upstream advisories describe spoofing through forwarded headers. Confirm the affected behavior and actual exposure in our configuration before choosing the fix.

## Acceptance Criteria

- Review current upstream security advisories and select a supported release that addresses applicable findings; do not assume an upgrade alone fixes proxy trust.
- Trace consumers of request.RemoteAddr, including logging, rate limits and any authorization decisions, and document the effect of a forged forwarded header.
- Decide and document whether forwarded headers are accepted. Default to the transport peer unless an explicitly trusted proxy establishes the client address; keep the solution appropriate to Ductile's deployments.
- Upgrade chi and adjust client-IP handling as required. Preserve existing routing and authorization behavior.
- Exercise requests with forged X-Forwarded-For and X-Real-IP headers, plus legitimate proxy traffic if supported, using real HTTP handlers.
- Run the relevant Go quality gates and record results.

## Sources

- https://github.com/go-chi/chi/security/advisories
- https://github.com/go-chi/chi/security/advisories/GHSA-3fxj-6jh8-hvhx
- https://github.com/go-chi/chi/security/advisories/GHSA-9g5q-2w5x-hmxf

## Narrative

- 2026-11-01: Principal agreed to keep chi and address the concrete dependency and client-IP risks instead of replacing the router. This card follows up that decision from 148. No runtime change has been made yet. (by @assistant)
