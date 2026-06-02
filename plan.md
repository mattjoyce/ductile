1. **Fix CORS Vulnerability**: In `internal/api/server.go`, update `corsMiddleware` to securely handle the wildcard `*` origin.
   - Detect if `*` is present in `allowedOrigins`.
   - If `*` is present, accept any `Origin` header.
   - Set `Access-Control-Allow-Origin: *` to properly handle the wildcard.
   - Most importantly, do not set `Access-Control-Allow-Credentials: "true"` when `*` is configured, preventing credential sharing across all origins.
2. **Add Tests**: Update `internal/api/server_test.go` to test wildcard origin functionality and ensure `Access-Control-Allow-Credentials` is not present when wildcard is used.
3. **Pre-commit Checks**: Run pre-commit instructions to ensure testing and formatting are correct.
4. **Submit**: Create PR with title "🛡️ Sentinel: [HIGH] Fix overly permissive CORS configuration".
