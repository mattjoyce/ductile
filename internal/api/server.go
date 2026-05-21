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
	RelayReceiver     *relay.Receiver
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
	logger        *slog.Logger
	server        *http.Server
	startedAt     time.Time
	events        *events.Hub
	syncSemaphore chan struct{}
	reloadFunc    func(context.Context) (ReloadResponse, error)
	serveDone     chan struct{}
	relayReceiver *relay.Receiver
}

// New creates a new API server instance
func New(config Config, queue JobQueuer, registry PluginRegistry, router PipelineRouter, waiter TreeWaiter, contextStore EventContextStore, hub *events.Hub, logger *slog.Logger) *Server {
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
		logger:        logger,
		startedAt:     time.Now(),
		events:        hub,
		syncSemaphore: make(chan struct{}, config.MaxConcurrentSync),
		reloadFunc:    config.ReloadFunc,
		serveDone:     make(chan struct{}),
		relayReceiver: config.RelayReceiver,
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

		r.With(s.requireScopes("plugin:ro", "plugin:rw", "*")).Post("/plugin/{plugin}/{command}", s.handlePluginTrigger)
		r.With(s.requireScopes("plugin:ro", "plugin:rw", "*")).Get("/plugin/{plugin}", s.handleGetPlugin)
		r.With(s.requireScopes("plugin:rw", "*")).Post("/pipeline/{pipeline}", s.handlePipelineTrigger)
		r.With(s.requireScopes("jobs:ro", "jobs:rw", "*")).Get("/job/{jobID}", s.handleGetJob)
		r.With(s.requireScopes("jobs:ro", "jobs:rw", "*")).Get("/job/{jobID}/tree", s.handleGetJobTree)
		r.With(s.requireScopes("jobs:ro", "jobs:rw", "*")).Get("/jobs", s.handleListJobs)
		r.With(s.requireScopes("jobs:ro", "jobs:rw", "*")).Get("/job-logs", s.handleListJobLogs)
		r.With(s.requireScopes("jobs:ro", "jobs:rw", "*")).Get("/scheduler/jobs", s.handleSchedulerJobs)
		r.With(s.requireScopes("jobs:ro", "*")).Get("/analytics/summary", s.handleAnalyticsSummary)
		r.With(s.requireScopes("jobs:ro", "*")).Get("/analytics/queue", s.handleQueueMetrics)
		r.With(s.requireScopes("system:rw", "*")).Post("/system/reload", s.handleSystemReload)
		r.With(s.requireScopes("system:ro", "system:rw", "*")).Get("/config/view", s.handleConfigView)
	})

	// SSE endpoint — also accepts ?token= because EventSource cannot send
	// Authorization headers. Scope check is otherwise identical.
	r.Group(func(r chi.Router) {
		r.Use(s.authenticate(true))
		r.With(s.requireScopes("events:ro", "events:rw", "*")).Get("/events", s.handleEvents)
	})

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
	allowAll := false
	for _, o := range allowedOrigins {
		if o == "*" {
			allowAll = true
		} else if o != "" {
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

			if !allowAll {
				if _, ok := allowed[origin]; !ok {
					next.ServeHTTP(w, r)
					return
				}
			}

			h := w.Header()
			h.Add("Vary", "Origin")
			if allowAll {
				h.Set("Access-Control-Allow-Origin", "*")
				h.Set("Access-Control-Allow-Credentials", "false")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
			}
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
