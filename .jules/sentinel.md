## 2026-06-25 - [Medium] Fix CORS credentials and wildcard handling
**Vulnerability:** When wildcard `*` is specified in `AllowedOrigins`, the application would still set `Access-Control-Allow-Credentials: true` which is a violation of the CORS specification. Some browsers might reject it. Also, setting it to `false` is not correct, it should just be omitted.
**Learning:** Understanding the CORS spec and that `Access-Control-Allow-Credentials: true` combined with `Access-Control-Allow-Origin: *` is invalid.
**Prevention:** Implement checks in the CORS middleware to handle `*` properly, setting `Access-Control-Allow-Origin: *` and omitting `Access-Control-Allow-Credentials`.
