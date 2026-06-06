## 2026-06-06 - Fix unhandled json.Marshal errors in API handlers
**Vulnerability:** In `internal/api/handlers.go`, errors from `json.Marshal` were explicitly ignored using `_` when wrapping event payloads.
**Learning:** Ignoring marshaling errors allows empty or malformed byte slices to be enqueued, which could lead to downstream panics, silent data corruption, or denial-of-service if workers cannot process the invalid payloads. This is a common pattern when developers assume internal structs will always marshal successfully.
**Prevention:** Always check and handle the `error` returned by `json.Marshal`. If it fails, log the error securely and return a generic 500 Internal Server Error without leaking details to the client.
