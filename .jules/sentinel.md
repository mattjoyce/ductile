## 2026-05-21 - [Insecure CORS Configuration - AllowedOrigins Wildcard with Credentials]
**Vulnerability:** The API server's CORS configuration allowed the wildcard `*` in `AllowedOrigins` while simultaneously setting `Access-Control-Allow-Credentials: true`.
**Learning:** This pattern is a significant security risk because it allows any website to make authenticated cross-origin requests, potentially exposing sensitive data or actions to unauthorized domains. Modern browsers block this configuration, but it should still be prevented server-side to enforce strict security boundaries.
**Prevention:** When implementing CORS middleware, if the wildcard `*` is used to allow all origins, `Access-Control-Allow-Credentials` must explicitly be set to `false`.
