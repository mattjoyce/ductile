## 2026-06-19 - [json.Marshal Unhandled Errors in API Handlers]
**Vulnerability:** Unhandled errors from json.Marshal in internal/api/handlers.go.
**Learning:** json.Marshal errors are not always handled resulting in potential silent failures during marshalling.
**Prevention:** Always check error values returned by json.Marshal to ensure proper error reporting.
