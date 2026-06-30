package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/queue"
)

// enqueueTimeout bounds the durable enqueue once a webhook has been accepted.
// It is intentionally decoupled from the HTTP request context so a client
// hang-up or a keep-alive connection-reuse race cannot cancel the enqueue
// transaction mid-commit (see #152).
const enqueueTimeout = 10 * time.Second

// Server represents the webhook HTTP server.
type Server struct {
	config Config
	queue  JobQueuer
	logger *slog.Logger
	server *http.Server
	done   chan struct{}

	// endpoints maps URL paths to their configurations
	endpoints map[string]*EndpointConfig
}

// New creates a new webhook server instance.
func New(config Config, queue JobQueuer, logger *slog.Logger) *Server {
	// Build endpoint lookup map
	endpoints := make(map[string]*EndpointConfig)
	for i := range config.Endpoints {
		ep := &config.Endpoints[i]

		// Apply defaults
		if ep.MaxBodySize == 0 {
			ep.MaxBodySize = DefaultMaxBodySize
		}
		if ep.Command == "" {
			ep.Command = DefaultCommand
		}

		endpoints[ep.Path] = ep
	}

	return &Server{
		config:    config,
		queue:     queue,
		logger:    logger,
		endpoints: endpoints,
		done:      make(chan struct{}),
	}
}

// Start starts the webhook HTTP server (blocking).
func (s *Server) Start(ctx context.Context) error {
	router := s.setupRoutes()

	s.server = &http.Server{
		Addr:         s.config.Listen,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("webhook server starting", "listen", s.config.Listen, "endpoints", len(s.endpoints))

	// Run server in goroutine
	errCh := make(chan error, 1)
	go func() {
		defer close(s.done)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		s.logger.Info("webhook server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("webhook server shutdown failed: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("webhook server error: %w", err)
	}
}

// WaitServeStopped waits until the server's listener has stopped serving.
func (s *Server) WaitServeStopped(ctx context.Context) error {
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setupRoutes configures the HTTP router.
func (s *Server) setupRoutes() *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.loggingMiddleware)
	r.Use(middleware.Recoverer)

	// Register webhook endpoints
	for path := range s.endpoints {
		r.Post(path, s.handleWebhook)
	}

	return r
}

// loggingMiddleware logs HTTP requests (excludes sensitive payloads).
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		// Log request (no body content for security)
		s.logger.Info("webhook request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// handleWebhook handles incoming webhook POST requests.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Look up endpoint configuration
	endpoint, ok := s.endpoints[r.URL.Path]
	if !ok {
		s.respondError(w, http.StatusNotFound, "endpoint not found")
		return
	}

	// Enforce body size limit
	limitedReader := io.LimitReader(r.Body, endpoint.MaxBodySize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		s.respondError(w, http.StatusInternalServerError, "failed to read request body")
		return
	}

	// Check if body exceeded limit
	if int64(len(body)) > endpoint.MaxBodySize {
		s.respondError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}

	// Extract signature from header
	signature := r.Header.Get(endpoint.SignatureHeader)
	if signature == "" {
		s.logger.Warn("webhook signature missing",
			"path", r.URL.Path,
			"header", endpoint.SignatureHeader,
		)
		s.respondError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Verify HMAC signature (constant-time comparison)
	if err := verifyHMACSignature(body, signature, endpoint.Secret); err != nil {
		s.logger.Warn("webhook signature verification failed",
			"path", r.URL.Path,
			"error", err,
		)
		s.respondError(w, http.StatusForbidden, "forbidden")
		return
	}

	// Enqueue job. The HMAC is verified and the body fully read by now, so the
	// durable enqueue must survive the request connection: a client hang-up or a
	// keep-alive reuse race would otherwise cancel the request context mid-commit,
	// auto-rolling-back the tx so tx.Commit() fails ("transaction has already been
	// committed or rolled back") and a valid webhook 500s. Detach from request
	// cancellation, but keep it bounded. (#152)
	enqCtx, cancelEnq := context.WithTimeout(context.WithoutCancel(ctx), enqueueTimeout)
	defer cancelEnq()
	jobID, err := s.queue.Enqueue(enqCtx, queue.EnqueueRequest{
		Plugin:      endpoint.Plugin,
		Command:     endpoint.Command,
		Payload:     json.RawMessage(body),
		SubmittedBy: "webhook:" + r.URL.Path,
		MaxAttempts: config.MaxAttemptsForPlugin(s.config.Plugins[endpoint.Plugin]),
	})
	if err != nil {
		s.logger.Error("failed to enqueue webhook job",
			"path", r.URL.Path,
			"plugin", endpoint.Plugin,
			"error", err,
		)
		s.respondError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}

	s.logger.Info("webhook job enqueued",
		"path", r.URL.Path,
		"plugin", endpoint.Plugin,
		"command", endpoint.Command,
		"job_id", jobID,
	)

	// Respond with 202 Accepted
	s.respondJSON(w, http.StatusAccepted, TriggerResponse{JobID: jobID})
}

// respondJSON sends a JSON response.
func (s *Server) respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		s.logger.Error("failed to write response", "error", err)
	}
}

// respondError sends a JSON error response.
func (s *Server) respondError(w http.ResponseWriter, status int, message string) {
	s.respondJSON(w, status, ErrorResponse{Error: message})
}
