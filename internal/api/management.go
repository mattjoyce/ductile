package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// setupManagementRoutes builds the router for the vault-operable / ductile-closed
// posture. It mounts ONLY health and the admin-token-gated /vault/* surface — the
// gateway plane (plugins, jobs, pipelines, topology, reload) is deliberately
// absent, not merely unauthenticated. Same vault routes as the gateway router,
// from the same mountVaultRoutes definition, so the two postures cannot diverge.
func (s *Server) setupManagementRoutes() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(s.loggingMiddleware)
	r.Use(middleware.Recoverer)

	// Liveness only — no config, no plugin/job surface. Lets an operator/AI
	// confirm the management listener is up without any credential.
	r.Get("/healthz", s.handleHealthz)

	s.mountVaultRoutes(r)
	return r
}

// StartManagement serves the management-only posture on a unix-domain socket and
// blocks until ctx is cancelled (mirrors Start, which serves the gateway plane on
// TCP). The public gateway listener is never opened here — the posture is reached
// by serving ONLY this local surface, exactly as the credential-ladder ADR §5
// invariant requires (never by opening the public listener unauthenticated).
//
// The socket is created owner-only (0600): the ADR mandates a same-host
// filesystem boundary, not just a network one. A stale socket left by a crash is
// removed first (net.Listen refuses to bind over an existing path).
func (s *Server) StartManagement(ctx context.Context) error {
	socket := s.config.ManagementSocket
	if socket == "" {
		return fmt.Errorf("management socket path is empty")
	}
	if s.vault == nil {
		// The management posture exists only to operate a vault. Refuse to come
		// up serving an empty surface rather than sit there looking healthy.
		return fmt.Errorf("management posture requires a vault owner, none configured")
	}
	// The kernel's sockaddr_un.sun_path is ~104 bytes on Darwin / ~108 on Linux;
	// an over-long path fails bind() with an opaque "invalid argument". Catch it
	// here with an actionable message (configure a shorter api.management_socket).
	if len(socket) >= 104 {
		return fmt.Errorf("management socket path is too long (%d bytes, max 103): %q", len(socket), socket)
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale management socket %q: %w", socket, err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on management socket %q: %w", socket, err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod management socket %q: %w", socket, err)
	}

	s.server = &http.Server{
		Handler:      s.setupManagementRoutes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}

	s.logger.Info("vault management API serving (management-only posture)", "socket", socket)

	errCh := make(chan error, 1)
	go func() {
		defer close(s.serveDone)
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("vault management API shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("management server shutdown failed: %w", err)
		}
		return ctx.Err()
	case err := <-errCh:
		return fmt.Errorf("management server error: %w", err)
	}
}
