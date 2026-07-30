package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/baggage"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
)

// maxAttemptsForPlugin resolves the configured retry max-attempts for a plugin
// at enqueue time. Returns 0 when no runtime config is available so the
// queue's own default applies. P2-02.
func (s *Server) maxAttemptsForPlugin(pluginName string) int {
	if s.config.RuntimeConfig == nil {
		return 0
	}
	return config.MaxAttemptsForPlugin(s.config.RuntimeConfig.Plugins[pluginName])
}

// handleRoot handles GET / — unauthenticated discovery index for humans and agents.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, RootResponse{
		Name:          "Ductile Gateway",
		Description:   "Automation runtime AI agents can run, debug, and build for.",
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		Discovery: map[string]string{
			"health":    "/healthz",
			"skills":    "/skills",
			"plugins":   "/plugins",
			"openapi":   "/openapi.json",
			"ai_plugin": "/.well-known/ai-plugin.json",
		},
	})
}

// handleHealthz handles GET /healthz (no auth).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	depth, err := s.queue.Depth(r.Context())
	if err != nil {
		s.logger.Error("failed to compute queue depth", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to compute queue depth")
		return
	}

	resp := HealthzResponse{
		Status:             "ok",
		UptimeSeconds:      int64(time.Since(s.startedAt).Seconds()),
		QueueDepth:         depth,
		PluginsLoaded:      len(s.registry.All()),
		PluginsCircuitOpen: 0,
		ConfigPath:         strings.TrimSpace(s.config.ConfigPath),
		BinaryPath:         strings.TrimSpace(s.config.BinaryPath),
		Version:            strings.TrimSpace(s.config.Version),
		Posture:            strings.TrimSpace(s.config.BootPosture),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSystemReload(w http.ResponseWriter, r *http.Request) {
	if s.reloadFunc == nil {
		s.writeError(w, http.StatusNotImplemented, "reload not supported")
		return
	}

	// Reload replaces the runtime that owns this handler's server. Do not bind
	// that lifecycle to a client disconnect once the protected operation starts.
	resp, err := s.reloadFunc(context.Background())
	if err != nil {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, resp)
}

// handlePipelineTrigger handles POST /pipeline/{pipeline}
// Explicitly triggers a named pipeline.
func (s *Server) handlePipelineTrigger(w http.ResponseWriter, r *http.Request) {
	pipelineName := chi.URLParam(r, "pipeline")

	pipeline := s.router.GetPipelineByName(pipelineName)
	if pipeline == nil {
		s.writeError(w, http.StatusNotFound, "pipeline not found")
		return
	}

	principal, _ := auth.PrincipalFromContext(r.Context())
	if !auth.HasAnyScope(principal, "plugin:rw", "*") {
		s.writeError(w, http.StatusForbidden, "insufficient scope to trigger pipeline")
		return
	}

	var req TriggerRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	eventType := strings.TrimSpace(pipeline.Trigger)
	if eventType == "" {
		eventType = "api.pipeline.trigger"
	}

	// Prepare event for pipeline entry.
	event := protocol.Event{
		Type:    eventType,
		Payload: make(map[string]any),
	}
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &event.Payload); err != nil {
			s.writeError(w, http.StatusBadRequest, "payload must be a JSON object")
			return
		}
	}

	// Resolve entry dispatches
	dispatches, err := s.router.GetEntryDispatches(pipelineName, event)
	if err != nil {
		s.logger.Error("failed to resolve pipeline entry", "pipeline", pipelineName, "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to resolve pipeline entry")
		return
	}
	if len(dispatches) == 0 {
		s.writeError(w, http.StatusInternalServerError, "pipeline has no entry points")
		return
	}

	startTime := time.Now()
	forceAsync := r.URL.Query().Get("async") == "true"
	if pipeline.ExecutionMode == "synchronous" && !forceAsync && len(dispatches) != 1 {
		s.writeError(w, http.StatusBadRequest, "synchronous pipeline trigger requires exactly one entry dispatch; use ?async=true for fan-out entry pipelines")
		return
	}

	type entryContextPlan struct {
		stepID  string
		updates json.RawMessage
	}

	jobIDs := make([]string, 0, len(dispatches))
	contextPlans := make([]entryContextPlan, len(dispatches))
	if s.contextStore != nil {
		for i, d := range dispatches {
			stepID := d.StepID
			if stepID == "" {
				stepID = pipeline.EntryStepID
			}
			updates, err := s.pipelineEntryContextUpdates(pipeline.Name, stepID, event.Payload)
			if err != nil {
				if errors.Is(err, errRootBaggageClaims) {
					s.writeError(w, http.StatusBadRequest, err.Error())
					return
				}
				s.logger.Error("failed to prepare event context for pipeline entry", "pipeline", pipeline.Name, "step_id", stepID, "error", err)
				s.writeError(w, http.StatusInternalServerError, "failed to create event context")
				return
			}
			contextPlans[i] = entryContextPlan{
				stepID:  stepID,
				updates: updates,
			}
		}
	}

	// Admission gate (P2-04): reject oversized baggage with HTTP 413 BEFORE
	// any event_context row is written. The previous code path raised the
	// size-cap error inside ContextStore.Create and surfaced it as an opaque
	// 500 with a half-written pipeline-instance context.
	if s.admitter != nil && s.contextStore != nil {
		for i, plan := range contextPlans {
			admission, err := s.admitter.Admit(r.Context(), state.AdmissionInput{
				AccumulatedJSON: plan.updates,
			})
			if err != nil {
				s.logger.Error("admission check failed for pipeline entry", "pipeline", pipeline.Name, "step_id", plan.stepID, "error", err)
				s.writeError(w, http.StatusInternalServerError, "failed to admit pipeline entry")
				return
			}
			if admission.Decision == state.AdmissionRejectOverlimit {
				s.logger.Warn("pipeline entry baggage rejected: exceeds max context size",
					"pipeline", pipeline.Name,
					"step_id", plan.stepID,
					"dispatch_index", i,
					"bytes_actual", admission.BytesActual,
					"bytes_limit", admission.BytesLimit,
				)
				respondJSON(w, http.StatusRequestEntityTooLarge, BaggageOverlimitResponse{
					Error:       "pipeline baggage exceeds maximum context size",
					BytesActual: admission.BytesActual,
					BytesLimit:  admission.BytesLimit,
				})
				return
			}
		}
	}

	var pipelineRootContextID *string
	if s.contextStore != nil {
		root, err := s.createPipelineInstanceContext(r.Context(), pipeline.Name, dispatches[0].RouteMaxDepth)
		if err != nil {
			s.logger.Error("failed to create pipeline instance context", "pipeline", pipeline.Name, "error", err)
			s.writeError(w, http.StatusInternalServerError, "failed to create pipeline context")
			return
		}
		pipelineRootContextID = &root.ID
	}

	for i, d := range dispatches {
		var eventContextID *string
		if s.contextStore != nil {
			root, err := s.createPipelineEntryContext(r.Context(), pipelineRootContextID, pipeline.Name, contextPlans[i].stepID, contextPlans[i].updates, d.RouteDepth, d.RouteMaxDepth)
			if err != nil {
				s.logger.Error("failed to create event context for pipeline entry", "pipeline", pipeline.Name, "step_id", contextPlans[i].stepID, "error", err)
				s.writeError(w, http.StatusInternalServerError, "failed to create event context")
				return
			}
			eventContextID = &root.ID
		}

		// Wrap payload for handle command
		enqueuePayload := req.Payload
		if d.Command == "handle" {
			var err error
			enqueuePayload, err = json.Marshal(event)
			if err != nil {
				s.logger.Error("failed to marshal handle event payload", "error", err)
				s.writeError(w, http.StatusInternalServerError, "failed to marshal handle event payload")
				return
			}
		}

		jobID, err := s.queue.Enqueue(r.Context(), queue.EnqueueRequest{
			Plugin:         d.Plugin,
			Command:        d.Command,
			Payload:        enqueuePayload,
			SubmittedBy:    "api",
			EventContextID: eventContextID,
			MaxAttempts:    s.maxAttemptsForPlugin(d.Plugin),
		})
		if err != nil {
			s.logger.Error("failed to enqueue pipeline job", "pipeline", pipelineName, "plugin", d.Plugin, "error", err)
			if i == 0 {
				s.writeError(w, http.StatusInternalServerError, "failed to enqueue job")
			} else {
				s.writeError(w, http.StatusInternalServerError, "pipeline partially enqueued; check prior job status and retry")
			}
			return
		}
		jobIDs = append(jobIDs, jobID)
	}

	if len(jobIDs) == 0 {
		s.writeError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}
	firstJobID := jobIDs[0]

	s.events.Publish("pipeline.enqueued", map[string]any{
		"at":           time.Now().UTC().Format(time.RFC3339Nano),
		"job_id":       firstJobID,
		"job_ids":      append([]string(nil), jobIDs...),
		"job_count":    len(jobIDs),
		"pipeline":     pipelineName,
		"submitted_by": "api",
	})

	s.logger.Info("pipeline enqueued via API", "job_id", firstJobID, "pipeline", pipelineName)

	if pipeline.ExecutionMode == "synchronous" && !forceAsync {
		select {
		case s.syncSemaphore <- struct{}{}:
			defer func() { <-s.syncSemaphore }()
		default:
			s.writeError(w, http.StatusServiceUnavailable, "too many concurrent synchronous requests")
			return
		}

		waitTimeout := pipeline.Timeout
		if s.config.MaxSyncTimeout > 0 && waitTimeout > s.config.MaxSyncTimeout {
			waitTimeout = s.config.MaxSyncTimeout
		}

		results, err := s.waiter.WaitForJobTree(r.Context(), firstJobID, waitTimeout)
		if err != nil {
			if err == context.DeadlineExceeded || strings.Contains(strings.ToLower(err.Error()), "timeout") {
				respondJSON(w, http.StatusAccepted, TimeoutResponse{
					JobID:           firstJobID,
					Status:          "running",
					TimeoutExceeded: true,
					Message:         "Pipeline still running after timeout.",
				})
				return
			}
			s.logger.Error("wait failed", "pipeline", pipelineName, "error", err)
			s.writeError(w, http.StatusInternalServerError, "wait failed")
			return
		}

		// Process results for synchronous pipeline responses.
		var tree []JobResultData
		var rootResult json.RawMessage
		finalStatus := string(queue.StatusSucceeded)
		var terminalResult json.RawMessage
		terminalSteps := terminalStepSet(s.router.GetCompiledRoutes(pipelineName), pipeline.TerminalStepIDs)

		for _, res := range results {
			if res.JobID == firstJobID {
				rootResult = res.Result
			}
			// If any job failed, the overall tree status is failed (for the purpose of the response)
			if res.Status == queue.StatusFailed || res.Status == queue.StatusTimedOut || res.Status == queue.StatusDead {
				finalStatus = string(res.Status)
			}

			if _, ok := terminalSteps[res.StepID]; ok {
				terminalResult = res.Result
			}
			tree = append(tree, JobResultData{
				JobID:       res.JobID,
				Plugin:      res.Plugin,
				Command:     res.Command,
				Status:      string(res.Status),
				Result:      res.Result,
				LastError:   res.LastError,
				StartedAt:   res.StartedAt,
				CompletedAt: res.CompletedAt,
			})
		}

		finalResult := terminalResult
		if finalResult == nil {
			finalResult = rootResult
		}

		respondJSON(w, http.StatusOK, SyncResponse{
			JobID:      firstJobID,
			Status:     finalStatus,
			DurationMs: time.Since(startTime).Milliseconds(),
			Result:     finalResult,
			Tree:       tree,
		})
		return
	}

	respondJSON(w, http.StatusAccepted, TriggerResponse{
		JobID:   firstJobID,
		Status:  "queued",
		Plugin:  "pipeline",
		Command: pipelineName,
	})
}

// handlePluginTrigger handles POST /plugin/{plugin}/{command}
// Bypasses router and executes the plugin directly.
func (s *Server) handlePluginTrigger(w http.ResponseWriter, r *http.Request) {
	pluginName := chi.URLParam(r, "plugin")
	commandName := chi.URLParam(r, "command")

	plug, ok := s.registry.Get(pluginName)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "plugin not found")
		return
	}

	if !plug.SupportsCommand(commandName) {
		s.writeError(w, http.StatusBadRequest, "command not supported by plugin")
		return
	}

	principal, _ := auth.PrincipalFromContext(r.Context())
	cmdType, _ := plug.CommandTypeFor(commandName)
	// P2-07: the route middleware already required plugin:invoke:ro / rw / *
	// so a plain plugin:ro token cannot reach this handler. Write commands
	// additionally require plugin:rw or wildcard — narrower read-only
	// invocation tokens cannot trigger mutating commands.
	if cmdType != plugin.CommandTypeRead {
		if !auth.HasAnyScope(principal, "plugin:rw", "*") {
			s.writeError(w, http.StatusForbidden, "insufficient scope")
			return
		}
	}

	var req TriggerRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	enqueuePayload := req.Payload
	if commandName == "handle" {
		event := protocol.Event{
			Type:    "api.trigger",
			Payload: make(map[string]any),
		}
		if len(req.Payload) > 0 {
			if err := json.Unmarshal(req.Payload, &event.Payload); err != nil {
				s.writeError(w, http.StatusBadRequest, "payload must be a JSON object")
				return
			}
		}
		var err error
		enqueuePayload, err = json.Marshal(event)
		if err != nil {
			s.logger.Error("failed to marshal handle event payload", "error", err)
			s.writeError(w, http.StatusInternalServerError, "failed to marshal handle event payload")
			return
		}
	}

	jobID, err := s.queue.Enqueue(r.Context(), queue.EnqueueRequest{
		Plugin:      pluginName,
		Command:     commandName,
		Payload:     enqueuePayload,
		SubmittedBy: "api",
		MaxAttempts: s.maxAttemptsForPlugin(pluginName),
	})
	if err != nil {
		s.logger.Error("failed to enqueue job", "plugin", pluginName, "command", commandName, "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}

	s.events.Publish("job.enqueued", map[string]any{
		"at":           time.Now().UTC().Format(time.RFC3339Nano),
		"job_id":       jobID,
		"plugin":       pluginName,
		"command":      commandName,
		"submitted_by": "api",
		"direct":       true,
	})

	s.logger.Info("plugin job enqueued directly via API", "job_id", jobID, "plugin", pluginName, "command", commandName)

	respondJSON(w, http.StatusAccepted, TriggerResponse{
		JobID:   jobID,
		Status:  "queued",
		Plugin:  pluginName,
		Command: commandName,
	})
}

// handleGetJob handles GET /job/{jobID}
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	// Retrieve job from queue
	job, err := s.queue.GetJobByID(r.Context(), jobID)
	if err != nil {
		if err == queue.ErrJobNotFound {
			s.writeError(w, http.StatusNotFound, "job not found")
			return
		}
		s.logger.Error("failed to retrieve job", "job_id", jobID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to retrieve job")
		return
	}

	// Build response (using Codex's JobResult type with result field)
	resp := JobStatusResponse{
		JobID:       job.JobID,
		Status:      string(job.Status),
		Plugin:      job.Plugin,
		Command:     job.Command,
		Result:      job.Result,
		StartedAt:   job.StartedAt,
		CompletedAt: job.CompletedAt,
	}

	// D1: hide result-class fields from principals lacking jobs:result:ro.
	if !s.canSeeJobResultsFromCtx(r.Context()) {
		resp.Result = nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("failed to write job response", "error", err)
	}
}

// canSeeJobResults reports whether the principal may receive result-class
// payloads in job-related responses. "Result-class" covers:
//   - Result (json.RawMessage, the plugin's structured return value)
//   - LastError (plugin error message, often contains failing payload echo)
//   - Stderr (plugin diagnostic output)
//
// All three are gated under jobs:result:ro because they all carry data the
// plugin produced — distinct from status/metadata fields (job_id, status,
// timestamps, attempt count) which ride on jobs:status:ro / jobs:logs:ro.
//
// jobs:ro and jobs:rw imply jobs:result:ro via the auth normalize map;
// wildcard always passes. Used by handleGetJob, handleGetJobTree, and
// handleListJobLogs.
func (s *Server) canSeeJobResults(p auth.Principal) bool {
	return auth.HasAnyScope(p, "jobs:result:ro", "jobs:rw", "*")
}

// canSeeJobResultsFromCtx is a context-aware wrapper that consolidates the
// "extract principal then ask" pattern used by three handlers. HasAnyScope
// returns false on a zero-value Principal (empty scope map), so a missing
// principal naturally maps to "cannot see results" — the strict default.
func (s *Server) canSeeJobResultsFromCtx(ctx context.Context) bool {
	p, _ := auth.PrincipalFromContext(ctx)
	return s.canSeeJobResults(p)
}

var errRootBaggageClaims = errors.New("root baggage claims failed")

func (s *Server) createPipelineInstanceContext(
	ctx context.Context,
	pipelineName string,
	routeMaxDepth int,
) (*state.EventContext, error) {
	if s.contextStore == nil {
		return nil, nil
	}

	updates, err := state.WithPipelineInstanceID(nil, uuid.NewString())
	if err != nil {
		return nil, fmt.Errorf("seed pipeline instance context: %w", err)
	}
	updates, err = state.WithRouteDepth(updates, 0)
	if err != nil {
		return nil, fmt.Errorf("seed pipeline route depth: %w", err)
	}
	if routeMaxDepth > 0 {
		updates, err = state.WithRouteMaxDepth(updates, routeMaxDepth)
		if err != nil {
			return nil, fmt.Errorf("seed pipeline route max depth: %w", err)
		}
	}
	return s.contextStore.Create(ctx, nil, pipelineName, "", updates)
}

func (s *Server) createPipelineEntryContext(
	ctx context.Context,
	parentID *string,
	pipelineName string,
	stepID string,
	updates json.RawMessage,
	routeDepth int,
	routeMaxDepth int,
) (*state.EventContext, error) {
	var err error
	if parentID != nil && s.contextStore != nil {
		parentCtx, loadErr := s.contextStore.Get(ctx, *parentID)
		if loadErr != nil {
			return nil, fmt.Errorf("load parent pipeline context: %w", loadErr)
		}
		if instanceID := state.PipelineInstanceIDFromAccumulated(parentCtx.AccumulatedJSON); instanceID != "" {
			updates, err = state.WithPipelineInstanceID(updates, instanceID)
			if err != nil {
				return nil, fmt.Errorf("seed pipeline entry instance id: %w", err)
			}
		}
	}
	if routeDepth > 0 {
		updates, err = state.WithRouteDepth(updates, routeDepth)
		if err != nil {
			return nil, fmt.Errorf("seed pipeline entry route depth: %w", err)
		}
	}
	if routeMaxDepth > 0 {
		updates, err = state.WithRouteMaxDepth(updates, routeMaxDepth)
		if err != nil {
			return nil, fmt.Errorf("seed pipeline entry route max depth: %w", err)
		}
	}
	return s.contextStore.Create(ctx, parentID, pipelineName, stepID, updates)
}

func (s *Server) pipelineEntryContextUpdates(
	pipelineName string,
	stepID string,
	payload map[string]any,
) (json.RawMessage, error) {
	if node, ok := s.router.GetNode(pipelineName, stepID); ok && node.Baggage != nil && !node.Baggage.Empty() {
		updates, err := baggage.ApplyClaims(payload, node.Baggage, nil)
		if err != nil {
			return nil, fmt.Errorf("%w: apply baggage claims for %s:%s: %v", errRootBaggageClaims, pipelineName, stepID, err)
		}
		raw, err := json.Marshal(updates)
		if err != nil {
			return nil, fmt.Errorf("marshal root baggage updates: %w", err)
		}
		return raw, nil
	}
	return json.RawMessage(`{}`), nil
}

func terminalStepSet(routes []dsl.CompiledRoute, fallback []string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, route := range routes {
		if route.Destination.Kind != dsl.CompiledRouteDestinationTerminal {
			continue
		}
		if stepID := strings.TrimSpace(route.Source.StepID); stepID != "" {
			out[stepID] = struct{}{}
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, stepID := range fallback {
		if stepID = strings.TrimSpace(stepID); stepID != "" {
			out[stepID] = struct{}{}
		}
	}
	return out
}

// handleListJobs handles GET /jobs.
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := queue.ListJobsFilter{
		Plugin:  strings.TrimSpace(q.Get("plugin")),
		Command: strings.TrimSpace(q.Get("command")),
		Limit:   50,
	}

	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 {
			s.writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		// Symmetric cap with /job-logs (handleListJobLogs below). Live
		// audit observed /jobs?limit=100000 returning 200 against a
		// ~17k-row DB, while /job-logs already rejected the same shape
		// with 400. Asymmetric pagination behavior is a foot-gun on
		// large stores; rejecting over-cap is preferable to silent
		// clamping because the caller knows immediately rather than
		// silently getting fewer rows than they asked for.
		if limit > 200 {
			s.writeError(w, http.StatusBadRequest, "limit must be <= 200")
			return
		}
		filter.Limit = limit
	}

	if rawStatus := strings.TrimSpace(q.Get("status")); rawStatus != "" {
		status, ok := parseJobStatusFilter(rawStatus)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "invalid status filter")
			return
		}
		filter.Status = &status
	}

	jobs, total, err := s.queue.ListJobs(r.Context(), filter)
	if err != nil {
		s.logger.Error("failed to list jobs", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}

	resp := JobListResponse{
		Jobs:  make([]JobListItem, 0, len(jobs)),
		Total: total,
	}
	for _, job := range jobs {
		resp.Jobs = append(resp.Jobs, JobListItem{
			JobID:       job.JobID,
			Plugin:      job.Plugin,
			Command:     job.Command,
			Status:      string(job.Status),
			CreatedAt:   job.CreatedAt,
			StartedAt:   job.StartedAt,
			CompletedAt: job.CompletedAt,
			Attempt:     job.Attempt,
		})
	}

	respondJSON(w, http.StatusOK, resp)
}

// handleListJobLogs handles GET /job-logs.
func (s *Server) handleListJobLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter := queue.JobLogFilter{
		JobID:       strings.TrimSpace(q.Get("job_id")),
		Plugin:      strings.TrimSpace(q.Get("plugin")),
		Command:     strings.TrimSpace(q.Get("command")),
		SubmittedBy: strings.TrimSpace(q.Get("submitted_by")),
		Query:       strings.TrimSpace(q.Get("query")),
		Limit:       50,
	}

	if rawLimit := strings.TrimSpace(q.Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 {
			s.writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if limit > 200 {
			s.writeError(w, http.StatusBadRequest, "limit must be <= 200")
			return
		}
		filter.Limit = limit
	}

	if rawStatus := strings.TrimSpace(q.Get("status")); rawStatus != "" {
		status, ok := parseJobStatusFilter(rawStatus)
		if !ok {
			s.writeError(w, http.StatusBadRequest, "invalid status filter")
			return
		}
		filter.Status = &status
	}

	if rawFrom := strings.TrimSpace(q.Get("from")); rawFrom != "" {
		parsed, err := parseTimeParam(rawFrom)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid from timestamp")
			return
		}
		filter.Since = &parsed
	}

	if rawTo := strings.TrimSpace(q.Get("to")); rawTo != "" {
		parsed, err := parseTimeParam(rawTo)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid to timestamp")
			return
		}
		filter.Until = &parsed
	}

	if strings.EqualFold(strings.TrimSpace(q.Get("include_result")), "true") {
		filter.IncludeResult = true
	}

	logs, total, err := s.queue.ListJobLogs(r.Context(), filter)
	if err != nil {
		s.logger.Error("failed to list job logs", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to list job logs")
		return
	}

	// D1: hide result-class fields (Result, LastError, Stderr) when the
	// caller lacks jobs:result:ro. include_result=true is still accepted
	// for back-compat but produces a shaped response for narrower scopes.
	canSeeResults := s.canSeeJobResultsFromCtx(r.Context())

	resp := JobLogListResponse{
		Logs:  make([]JobLogItem, 0, len(logs)),
		Total: total,
	}
	for _, logEntry := range logs {
		item := JobLogItem{
			JobID:       logEntry.JobID,
			LogID:       logEntry.LogID,
			Plugin:      logEntry.Plugin,
			Command:     logEntry.Command,
			Status:      string(logEntry.Status),
			Attempt:     logEntry.Attempt,
			SubmittedBy: logEntry.SubmittedBy,
			CreatedAt:   logEntry.CreatedAt,
			CompletedAt: logEntry.CompletedAt,
			LastError:   logEntry.LastError,
			Stderr:      logEntry.Stderr,
			Result:      logEntry.Result,
		}
		if !canSeeResults {
			item.Result = nil
			item.LastError = nil
			item.Stderr = nil
		}
		resp.Logs = append(resp.Logs, item)
	}

	respondJSON(w, http.StatusOK, resp)
}

func parseJobStatusFilter(raw string) (queue.Status, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pending":
		return queue.StatusQueued, true
	case "ok":
		return queue.StatusSucceeded, true
	case "error":
		return queue.StatusFailed, true
	case string(queue.StatusQueued), string(queue.StatusRunning), string(queue.StatusSucceeded), string(queue.StatusFailed), string(queue.StatusTimedOut), string(queue.StatusDead):
		return queue.Status(strings.ToLower(strings.TrimSpace(raw))), true
	default:
		return "", false
	}
}

func parseTimeParam(raw string) (time.Time, error) {
	layouts := []string{time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time")
}

// handleListPlugins handles GET /plugins.
func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	plugins := s.registry.All()
	var pNames []string
	for name := range plugins {
		pNames = append(pNames, name)
	}
	sort.Strings(pNames)

	resp := PluginListResponse{
		Plugins: make([]PluginSummary, 0, len(pNames)),
	}

	for _, name := range pNames {
		p := plugins[name]
		summary := PluginSummary{
			Name:        p.Name,
			Version:     p.Version,
			Description: p.Description,
			Commands:    make([]string, 0, len(p.Commands)),
		}
		for _, cmd := range p.Commands {
			summary.Commands = append(summary.Commands, cmd.Name)
		}
		resp.Plugins = append(resp.Plugins, summary)
	}

	respondJSON(w, http.StatusOK, resp)
}

// handleListSkills handles GET /skills.
// It returns a unified capability index (the Connector Catalog) with atomic plugin operations and orchestrated pipelines.
func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	plugins := s.registry.All()
	var pNames []string
	for name := range plugins {
		pNames = append(pNames, name)
	}
	sort.Strings(pNames)

	resp := SkillsIndexResponse{
		Skills: make([]SkillSummary, 0, len(pNames)),
	}

	for _, name := range pNames {
		p := plugins[name]
		commands := append([]plugin.Command(nil), p.Commands...)
		sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })

		for _, cmd := range commands {
			tier := "WRITE"
			if cmd.Type == plugin.CommandTypeRead {
				tier = "READ"
			}
			resp.Skills = append(resp.Skills, SkillSummary{
				Name:        fmt.Sprintf("plugin.%s.%s", p.Name, cmd.Name),
				Kind:        "plugin",
				Description: cmd.Description,
				Endpoint:    fmt.Sprintf("/plugin/%s/%s", p.Name, cmd.Name),
				Tier:        tier,
				Plugin:      p.Name,
				Command:     cmd.Name,
			})
		}
	}

	type pipelineSummarizer interface {
		PipelineSummary() []router.PipelineInfo
	}
	if summarizer, ok := s.router.(pipelineSummarizer); ok && summarizer != nil {
		pipelines := summarizer.PipelineSummary()
		for _, p := range pipelines {
			mode := strings.TrimSpace(p.ExecutionMode)
			if mode == "" {
				mode = "asynchronous"
			}
			skill := SkillSummary{
				Name:          fmt.Sprintf("pipeline.%s", p.Name),
				Kind:          "pipeline",
				Endpoint:      fmt.Sprintf("/pipeline/%s", p.Name),
				Pipeline:      p.Name,
				Trigger:       p.Trigger,
				ExecutionMode: mode,
			}
			if p.Timeout > 0 {
				skill.TimeoutSecs = int64(p.Timeout.Seconds())
			}
			resp.Skills = append(resp.Skills, skill)
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// handleGetPlugin handles GET /plugin/{plugin}.
func (s *Server) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	pluginName := chi.URLParam(r, "plugin")
	p, ok := s.registry.Get(pluginName)
	if !ok {
		s.writeError(w, http.StatusNotFound, "plugin not found")
		return
	}

	resp := PluginDetailResponse{
		Name:        p.Name,
		Version:     p.Version,
		Description: p.Description,
		Protocol:    p.Protocol,
		Commands:    make([]PluginCommand, 0, len(p.Commands)),
	}

	for _, cmd := range p.Commands {
		resp.Commands = append(resp.Commands, PluginCommand{
			Name:         cmd.Name,
			Type:         string(cmd.Type),
			Description:  cmd.Description,
			InputSchema:  cmd.GetFullInputSchema(),
			OutputSchema: cmd.GetFullOutputSchema(),
		})
	}

	respondJSON(w, http.StatusOK, resp)
}

// handleOpenAPIPlugin handles GET /plugin/{plugin}/openapi.json — returns OpenAPI 3.1 doc for one plugin (no auth).
func (s *Server) handleOpenAPIPlugin(w http.ResponseWriter, r *http.Request) {
	pluginName := chi.URLParam(r, "plugin")
	p, ok := s.registry.Get(pluginName)
	if !ok {
		s.writeError(w, http.StatusNotFound, "plugin not found")
		return
	}
	doc := buildOpenAPIDoc(map[string]*plugin.Plugin{pluginName: p})
	respondJSON(w, http.StatusOK, doc)
}

// handleOpenAPIAll handles GET /openapi.json — returns OpenAPI 3.1 doc for all plugins (no auth).
func (s *Server) handleOpenAPIAll(w http.ResponseWriter, r *http.Request) {
	doc := buildOpenAPIDoc(s.registry.All())
	respondJSON(w, http.StatusOK, doc)
}

// handleWellKnownPlugin handles GET /.well-known/ai-plugin.json (no auth).
func (s *Server) handleWellKnownPlugin(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]any{
		"schema_version":        "v1",
		"name_for_human":        "Ductile Gateway",
		"name_for_model":        "ductile",
		"description_for_human": "Automation runtime AI agents can run, debug, and build for.",
		"description_for_model": "Ductile is an automation runtime designed for agent operation. Discover plugins via /openapi.json (per-plugin: /plugin/{name}/openapi.json); invoke commands via POST /plugin/{name}/{command}; query runs via /jobs; inspect health via /system/doctor and /system/selfcheck; read latency stats via /stopwatch/{plugin}; read the plugin graph via /topology. For deeper work (RCA, plugin authoring, diagnostics) load the matching skill from skills/ductile-*.",
		"auth": map[string]any{
			"type": "bearer",
		},
		"api": map[string]any{
			"type": "openapi",
			"url":  "/openapi.json",
		},
	})
}

// handleGetJobTree handles GET /job/{jobID}/tree
func (s *Server) handleGetJobTree(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")

	results, err := s.queue.GetJobTree(r.Context(), jobID)
	if err != nil {
		s.logger.Error("failed to retrieve job tree", "job_id", jobID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to retrieve job tree")
		return
	}

	// D1: shape per-job result-class fields by principal scope.
	canSeeResults := s.canSeeJobResultsFromCtx(r.Context())

	resp := make([]JobResultData, 0, len(results))
	for _, res := range results {
		item := JobResultData{
			JobID:       res.JobID,
			ParentJobID: res.ParentJobID,
			Plugin:      res.Plugin,
			Command:     res.Command,
			Status:      string(res.Status),
			Result:      res.Result,
			LastError:   res.LastError,
			StartedAt:   res.StartedAt,
			CompletedAt: res.CompletedAt,
		}
		if !canSeeResults {
			item.Result = nil
			item.LastError = nil
		}
		resp = append(resp, item)
	}

	respondJSON(w, http.StatusOK, resp)
}

// handleAnalyticsSummary handles GET /analytics/summary
func (s *Server) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	// Simple summary of job statuses in the last 24h
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)

	rows, err := s.queue.(*queue.Queue).GetDB().QueryContext(r.Context(), `
SELECT status, COUNT(*)
FROM job_log
WHERE completed_at >= ?
GROUP BY status;
`, cutoff)
	if err != nil {
		s.logger.Error("failed to query analytics summary", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to query analytics summary")
		return
	}
	defer func() { _ = rows.Close() }()

	summary := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err == nil {
			summary[status] = count
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"window": "24h",
		"stats":  summary,
	})
}

// handleQueueMetrics handles GET /analytics/queue
func (s *Server) handleQueueMetrics(w http.ResponseWriter, r *http.Request) {
	metrics, err := s.queue.Metrics(r.Context())
	if err != nil {
		s.logger.Error("failed to retrieve queue metrics", "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to retrieve queue metrics")
		return
	}

	respondJSON(w, http.StatusOK, metrics)
}

// respondJSON is a helper to write JSON responses
func respondJSON(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes a JSON error response
func (s *Server) writeError(w http.ResponseWriter, statusCode int, message string) {
	respondJSON(w, statusCode, ErrorResponse{Error: message})
}
