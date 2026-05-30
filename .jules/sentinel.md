## 2025-05-30 - Silent JSON Marshal failure in API Handlers
**Vulnerability:** Unhandled \`json.Marshal\` errors in \`internal/api/handlers.go\` could lead to silent failures and null/empty payloads being enqueued when encoding handle command payloads.
**Learning:** Ignored errors from \`json.Marshal\` (e.g., \`enqueuePayload, _ = json.Marshal(event)\`) bypass error handling and fail securely principles.
**Prevention:** Always check and handle errors returned by \`json.Marshal\`, especially in critical code paths like API handlers.
