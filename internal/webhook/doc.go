// Package webhook implements secure HTTP webhook endpoints with HMAC-SHA256 verification.
//
// Webhooks provide a secure way for external services (GitHub, GitLab, etc.) to trigger
// ductile jobs. All webhook endpoints require HMAC-SHA256 signature verification
// using a pre-shared secret.
//
// # Security Model
//
// - HMAC-SHA256 signatures verified using crypto/subtle (constant-time comparison)
// - Body size limits enforced to prevent DoS attacks
// - No signature details leaked in error responses (always generic 403)
// - Request logging excludes sensitive payloads
// - Secrets loaded from environment variables (never hardcoded)
//
// # Configuration
//
// Webhooks are configured in webhooks.yaml:
//
//	webhooks:
//	  listen: "127.0.0.1:8081"
//	  endpoints:
//	    - path: /webhook/github
//	      plugin: github-handler
//	      command: handle
//	      secret_ref: github_webhook_secret  # References a vault secret
//	      signature_header: X-Hub-Signature-256
//	      max_body_size: 1048576  # 1MB
//
// # Request Flow
//
//  1. HTTP POST arrives at configured path
//  2. Body size checked (reject with 413 if too large)
//  3. Signature header extracted
//  4. HMAC-SHA256 computed over request body
//  5. Constant-time comparison of signatures (reject with 403 if mismatch)
//  6. Job enqueued with plugin/command from config
//  7. 202 Accepted returned with job_id
//
// # Error Responses
//
// - 403 Forbidden: Invalid or missing signature (no details)
// - 404 Not Found: Unknown webhook path
// - 413 Payload Too Large: Body exceeds max_body_size
// - 500 Internal Server Error: Job enqueueing failed
//
// # Example Usage
//
//	cfg := webhook.Config{
//		Listen: "127.0.0.1:8081",
//		Endpoints: []webhook.EndpointConfig{
//			{
//				Path:            "/webhook/github",
//				Plugin:          "github-handler",
//				Command:         "handle",
//				Secret:          os.Getenv("GITHUB_WEBHOOK_SECRET"),
//				SignatureHeader: "X-Hub-Signature-256",
//				MaxBodySize:     1048576,
//			},
//		},
//	}
//
//	server := webhook.New(cfg, queue, logger)
//	if err := server.Start(ctx); err != nil {
//		log.Fatal(err)
//	}
package webhook
