## 2024-05-18 - [Fix CORS Wildcard Vulnerability]
**Vulnerability:** The CORS middleware allowed cross-origin credentials (`Access-Control-Allow-Credentials: true`) to be sent even when the origin was matched via a wildcard (`*`).
**Learning:** Returning credentials alongside a reflected origin when a wildcard is conceptually intended bypasses browser restrictions, making the application vulnerable to exposing sensitive data across all origins.
**Prevention:** Always verify if a wildcard is present in `AllowedOrigins`. If it is, explicitly return `Access-Control-Allow-Origin: *` and omit the `Access-Control-Allow-Credentials` header to comply securely with the CORS specification.
