package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/relay"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
)

// JobQueuer defines the interface for job queue operations
type JobQueuer interface {
	Enqueue(ctx context.Context, req queue.EnqueueRequest) (string, error)
	GetJobByID(ctx context.Context, jobID string) (*queue.JobResult, error)
	GetJobTree(ctx context.Context, rootJobID string) ([]*queue.JobResult, error)
	ListJobs(ctx context.Context, filter queue.ListJobsFilter) ([]*queue.JobSummary, int, error)
	ListJobLogs(ctx context.Context, filter queue.JobLogFilter) ([]*queue.JobLogEntry, int, error)
	Depth(ctx context.Context) (int, error)
	Metrics(ctx context.Context) (queue.QueueMetrics, error)
}

// TreeWaiter defines the interface for waiting on job tree completion
type TreeWaiter interface {
	WaitForJobTree(ctx context.Context, rootJobID string, timeout time.Duration) ([]*queue.JobResult, error)
}

// PipelineRouter defines the interface for looking up pipelines
type PipelineRouter interface {
	GetPipelineByTrigger(trigger string) *router.PipelineInfo
	GetPipelineByName(name string) *router.PipelineInfo
	GetEntryDispatches(pipelineName string, event protocol.Event) ([]router.Dispatch, error)
	GetNode(pipelineName string, stepID string) (dsl.Node, bool)
	GetCompiledRoutes(pipelineName string) []dsl.CompiledRoute
	PipelineSummary() []router.PipelineInfo
}

// EventContextStore defines the interface for creating event context lineage.
type EventContextStore interface {
	Create(ctx context.Context, parentID *string, pipelineName string, stepID string, updates json.RawMessage) (*state.EventContext, error)
	Get(ctx context.Context, id string) (*state.EventContext, error)
}

// AdmissionGate decides whether to admit a pipeline ingress dispatch before
// durable context creation. Implemented by *state.Admitter. Used to surface
// baggage-overlimit conditions as HTTP 413 (P2-04) rather than the previous
// generic 500.
type AdmissionGate interface {
	Admit(ctx context.Context, in state.AdmissionInput) (state.AdmissionResult, error)
}

// PluginRegistry defines the interface for plugin operations
type PluginRegistry interface {
	Get(name string) (*plugin.Plugin, bool)
	All() map[string]*plugin.Plugin
}

// Config holds API server configuration
type Config struct {
	Listen string
	// Tokens is a list of scoped bearer tokens.
	Tokens            []auth.TokenConfig
	MaxConcurrentSync int
	MaxSyncTimeout    time.Duration
	ConfigPath        string
	BinaryPath        string
	Version           string
	RuntimeConfig     *config.Config
	ReloadFunc        func(context.Context) (ReloadResponse, error)
	// DoctorFunc backs GET /system/doctor. nil disables the endpoint
	// (handler responds 503). Runtime wires a closure that runs the
	// same validation pipeline as `ductile config check`.
	DoctorFunc DoctorFunc
	// SelfcheckFunc backs GET /system/selfcheck. nil disables the endpoint
	// (handler responds 503). Runtime wires a closure that runs the
	// same six invariants as `ductile system selfcheck`.
	SelfcheckFunc SelfcheckFunc
	RelayReceiver *relay.Receiver
	// Vault is the management surface for the daemon-owned secret store. nil
	// disables the /vault routes (no vault loaded / keyless). Authenticated by
	// the vault's own resident admin token, not these config Tokens.
	Vault VaultManager
	// AllowedOrigins lists the origins that may receive credentialed CORS
	// headers. An empty list disables cross-origin credential sharing entirely.
	AllowedOrigins []string
}

// Server represents the HTTP API server
type Server struct {
	config        Config
	queue         JobQueuer
	registry      PluginRegistry
	router        PipelineRouter
	waiter        TreeWaiter
	contextStore  EventContextStore
	admitter      AdmissionGate
	stopwatch     StopwatchReader
	logger        *slog.Logger
	server        *http.Server
	startedAt     time.Time
	events        *events.Hub
	syncSemaphore chan struct{}
	reloadFunc    func(context.Context) (ReloadResponse, error)
	serveDone     chan struct{}
	relayReceiver *relay.Receiver
	vault         VaultManager
}

// New creates a new API server instance. admitter decides whether ingress
// requests are admitted before durable event_context creation; production
// runtime supplies state.NewAdmitter(queue, state.DefaultMaxContextBytes).
// A nil admitter disables the admission check (size cap then surfaces via the
// existing inner ContextStore.Create defensive check, as a 500).
// New creates a new API server instance. admitter decides whether ingress
// requests are admitted before durable event_context creation; production
// runtime supplies state.NewAdmitter(queue, state.DefaultMaxContextBytes).
// A nil admitter disables the admission check (size cap then surfaces via the
// existing inner ContextStore.Create defensive check, as a 500).
//
// stopwatch backs the /stopwatch/{plugin} endpoint. A nil stopwatch leaves
// the endpoint registered but responding 503 — callers that don't care about
// latency observability (tests, lightweight harnesses) can pass nil.
func New(config Config, queue JobQueuer, registry PluginRegistry, router PipelineRouter, waiter TreeWaiter, contextStore EventContextStore, admitter AdmissionGate, stopwatch StopwatchReader, hub *events.Hub, logger *slog.Logger) *Server {
	if config.MaxConcurrentSync <= 0 {
		config.MaxConcurrentSync = 10
	}
	return &Server{
		config:        config,
		queue:         queue,
		registry:      registry,
		router:        router,
		waiter:        waiter,
		contextStore:  contextStore,
		admitter:      admitter,
		stopwatch:     stopwatch,
		logger:        logger,
		startedAt:     time.Now(),
		events:        hub,
		syncSemaphore: make(chan struct{}, config.MaxConcurrentSync),
		reloadFunc:    config.ReloadFunc,
		serveDone:     make(chan struct{}),
		relayReceiver: config.RelayReceiver,
		vault:         config.Vault,
	}
}

// Start starts the HTTP server (blocking)
func (s *Server) Start(ctx context.Context) error {
	router := s.setupRoutes()

	s.server = &http.Server{
		Addr:         s.config.Listen,
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Minute, // Increased to support synchronous pipelines
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("API server starting", "listen", s.config.Listen)

	// Run server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		defer close(s.serveDone)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		s.logger.Info("API server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server shutdown failed: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}
}

// WaitServeStopped waits until the server's listener has stopped serving.
func (s *Server) WaitServeStopped(ctx context.Context) error {
	select {
	case <-s.serveDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// setupRoutes configures the HTTP router
func (s *Server) setupRoutes() *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.loggingMiddleware)
	r.Use(middleware.Recoverer)

	r.Use(corsMiddleware(s.config.AllowedOrigins))

	// Routes
	// Always unauthenticated.
	r.Get("/", s.handleRoot)
	r.Get("/healthz", s.handleHealthz)
	if s.relayReceiver != nil {
		r.Post(s.relayReceiver.RoutePattern(), s.relayReceiver.HandleHTTP)
	}

	// Protected API — header bearer token only.
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)

		// Discovery — any valid token; no scope restriction beyond authentication.
		r.Get("/plugins", s.handleListPlugins)
		r.Get("/skills", s.handleListSkills)
		r.Get("/openapi.json", s.handleOpenAPIAll)
		r.Get("/.well-known/ai-plugin.json", s.handleWellKnownPlugin)
		r.Get("/plugin/{plugin}/openapi.json", s.handleOpenAPIPlugin)

		// P2-07: plugin:ro is now catalog-only. Invocation of read-class
		// commands requires plugin:invoke:ro (or plugin:rw / *). Catalog
		// reads accept plugin:catalog:ro (implied by both plugin:ro and
		// plugin:rw) so existing tokens continue to discover plugins.
		r.With(s.requireScopes("plugin:invoke:ro", "plugin:rw", "*")).Post("/plugin/{plugin}/{command}", s.handlePluginTrigger)
		r.With(s.requireScopes("plugin:catalog:ro", "plugin:rw", "*")).Get("/plugin/{plugin}", s.handleGetPlugin)
		// Topology surfaces operational metadata about how plugins are
		// wired together via the compiled router routes — strictly more
		// than plugin catalog metadata (a single plugin's manifest in
		// isolation). Gated on system:ro alongside /config/view,
		// /system/doctor, /system/selfcheck. Originally shipped under
		// plugin:catalog:ro which plugin:ro implies via the P2-07
		// normalize map; live audit caught that this gave plugin:ro
		// tokens visibility into the full automation graph including
		// pipeline edges, which exceeds the catalog-only intent.
		r.With(s.requireScopes("system:ro", "system:rw", "*")).Get("/topology", s.handleTopology)
		r.With(s.requireScopes("plugin:rw", "*")).Post("/pipeline/{pipeline}", s.handlePipelineTrigger)
		// D1: each endpoint accepts its narrower scope as well as the back-compat
		// super-scopes (jobs:ro / jobs:rw / wildcard). jobs:ro implies all
		// four narrower scopes via the normalize map so existing tokens pass
		// through unchanged. Operators who grant only a narrower scope (e.g.
		// jobs:status:ro alone) reach the relevant endpoint and receive a
		// shaped response — Result payloads are omitted unless they ALSO
		// hold jobs:result:ro.
		r.With(s.requireScopes("jobs:status:ro", "jobs:rw", "*")).Get("/job/{jobID}", s.handleGetJob)
		r.With(s.requireScopes("jobs:tree:ro", "jobs:rw", "*")).Get("/job/{jobID}/tree", s.handleGetJobTree)
		r.With(s.requireScopes("jobs:status:ro", "jobs:rw", "*")).Get("/jobs", s.handleListJobs)
		r.With(s.requireScopes("jobs:logs:ro", "jobs:rw", "*")).Get("/job-logs", s.handleListJobLogs)
		r.With(s.requireScopes("jobs:status:ro", "jobs:rw", "*")).Get("/scheduler/jobs", s.handleSchedulerJobs)
		r.With(s.requireScopes("jobs:status:ro", "*")).Get("/analytics/summary", s.handleAnalyticsSummary)
		r.With(s.requireScopes("jobs:status:ro", "*")).Get("/analytics/queue", s.handleQueueMetrics)
		// Stopwatch percentile aggregation. Base latency stats need only
		// jobs:status:ro (or its supersets); sub-span exposure via
		// ?include_subs=true is gated INSIDE the handler on jobs:result:ro
		// because subs_json carries plugin-supplied unvalidated content.
		r.With(s.requireScopes("jobs:status:ro", "jobs:rw", "*")).Get("/stopwatch/{plugin}", s.handleStopwatch)
		r.With(s.requireScopes("system:rw", "*")).Post("/system/reload", s.handleSystemReload)
		r.With(s.requireScopes("system:ro", "system:rw", "*")).Get("/config/view", s.handleConfigView)
		r.With(s.requireScopes("system:ro", "system:rw", "*")).Get("/system/doctor", s.handleDoctor)
		r.With(s.requireScopes("system:ro", "system:rw", "*")).Get("/system/selfcheck", s.handleSelfcheck)
	})

	// SSE endpoint — also accepts ?token= because EventSource cannot send
	// Authorization headers. Scope check is otherwise identical.
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate(true))
		r.With(s.requireScopes("events:ro", "events:rw", "*")).Get("/events", s.handleEvents)
	})

	// Vault management — gated by the vault's OWN resident admin token, not the
	// config API tokens above (a separate group, separate authenticator). The
	// daemon is the sole writer; value-dump and genesis stay local, never here.
	if s.vault != nil {
		r.Group(func(r chi.Router) {
			r.Use(s.authenticateVaultAdmin)
			r.Post("/vault/secret", s.handleVaultSet)
		})
	}

	return r
}

// corsMiddleware returns a middleware that sets CORS headers for requests whose
// Origin matches an entry in allowedOrigins. Credentialed headers are only sent
// for listed origins; an empty list disables cross-origin credential sharing.
func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	const (
		allowedMethods = "GET, POST, PUT, DELETE, OPTIONS"
		allowedHeaders = "Accept, Authorization, Content-Type, X-CSRF-Token"
		exposedHeaders = "Link"
		maxAge         = "300"
	)

	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		if o != "" {
			allowed[o] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			if _, ok := allowed[origin]; !ok {
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", exposedHeaders)

			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", allowedMethods)
				h.Set("Access-Control-Allow-Headers", allowedHeaders)
				h.Set("Access-Control-Max-Age", maxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// loggingMiddleware logs HTTP requests
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}
