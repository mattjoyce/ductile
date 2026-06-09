---
id: 132
status: backlog
priority: Medium
tags: [api, security, tls, transport, fail-closed]
---

# Gateway TCP listener: guard cleartext bearer tokens on a network bind

> Raised 2026-06-09 during #129/#130 review. **Parked** to finish the credential ladder (#131) first.
> This is **pre-existing and orthogonal** to #129/#130 — those only added the unix management socket.

## The concern
The public gateway listener is plain **HTTP** (`internal/api/server.go` → `ListenAndServe()`, no TLS
anywhere in the server or config). API bearer tokens (#94 vault secrets) ride as
`Authorization: Bearer …` in **cleartext** on every request. On the shipped loopback default
(`listen: 127.0.0.1:80xx`) that is fine. But nothing stops `listen: 0.0.0.0:8080`, which silently ships
those tokens across the LAN in the clear — and there is no guardrail.

Standing decision (`docs/ARCHITECTURE.md:859`): TLS is the **reverse proxy's** job
(Caddy/nginx/Traefik terminate TLS; ductile serves HTTP on loopback behind it). Legitimate, but it is a
convention with no enforcement.

**NOT in scope:** the unix management socket (#129) stays HTTP — TLS does not apply to a unix domain
socket (no wire to protect; the boundary is filesystem perms, which is stronger than loopback TCP).

## Recommended (from the review): B + A
- **B — fail-closed guardrail (primary):** a boot gate that refuses to open the public listener on a
  **non-loopback** bind unless TLS is configured OR an explicit cleartext override is set. Same shape as
  the #94/#119/#111 gates — kills the silent-cleartext-on-LAN footgun. Anchor the gate near the existing
  listener seam (`cmd/ductile/runtime.go` API block) / admission policy.
- **A — document the proxy stance** as the blessed path for network exposure.

## Alternative considered
- **C — native TLS** (`api.tls.cert_file`/`key_file` → `ListenAndServeTLS`): lets ductile serve HTTPS
  without a proxy, but adds cert provisioning/rotation as ductile's responsibility. Optional add-on; not
  required if B+A land.

## Done when
A non-loopback bind without TLS (and without an explicit cleartext-acknowledged override) refuses to
boot with a clear, actionable error; the reverse-proxy TLS path is documented.
