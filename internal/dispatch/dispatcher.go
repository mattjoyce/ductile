package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mattjoyce/ductile/internal/baggage"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/log"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/relay"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/conditions"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/stopwatch"
)

const (
	// maxStderrBytes caps the amount of stderr captured from plugin execution.
	maxStderrBytes = 64 * 1024

	// terminationGracePeriod is the time we wait after SIGTERM before sending SIGKILL.
	terminationGracePeriod = 5 * time.Second

	nonRetryableExitCode = 78
)

// Dispatcher dequeues jobs and executes them by spawning plugin subprocesses.
type Dispatcher struct {
	queue    *queue.Queue
	state    *state.Store
	contexts *state.ContextStore
	admitter AdmissionGate
	router   router.Engine
	registry *plugin.Registry
	cfg      *config.Config
	events   *events.Hub
	logger   *slog.Logger

	// secretComposer resolves per-plugin vault secrets delivered over stdin at
	// spawn. Nil when no vault is wired, in which case no secrets are delivered.
	secretComposer SecretComposer

	// pluginVerifier re-verifies a plugin's live bytes against its recorded keyed
	// fingerprint right before its secrets are delivered (§3.3). Nil disables the
	// gate. Only consulted for plugins that ARE vault principals.
	pluginVerifier PluginVerifier

	// completions tracks jobs being waited on for synchronous execution.
	// Map key is root job ID. Value is a channel that is closed when the tree is complete.
	completions    map[string]chan struct{}
	pollLifecycles map[string]pollLifecycleJob
	mu             sync.RWMutex
}

// AdmissionGate decides whether a routed dispatch should proceed to durable
// context creation. Implemented by *state.Admitter. Defined locally so the
// dispatch package depends on the small interface, not the concrete admitter.
type AdmissionGate interface {
	Admit(ctx context.Context, in state.AdmissionInput) (state.AdmissionResult, error)
}

// Option configures optional Dispatcher behaviour at construction time.
type Option func(*Dispatcher)

// WithAdmitter wires the admission boundary that decides whether a routed
// dispatch should proceed to durable event_context creation (P2-09). The
// production runtime supplies state.NewAdmitter(queue, state.DefaultMaxContextBytes).
// Tests that do not exercise replay paths may omit this option; the admit
// check no-ops when the admitter is nil.
func WithAdmitter(a AdmissionGate) Option {
	return func(d *Dispatcher) {
		d.admitter = a
	}
}

// WithSecretComposer wires the vault read-path that composes a plugin's
// authorized secrets for stdin delivery at spawn. When omitted (nil), no
// secrets are delivered and plugins resolve as before — so this is additive and
// back-compatible. The production runtime supplies the loaded *vault.Store.
func WithSecretComposer(c SecretComposer) Option {
	return func(d *Dispatcher) {
		d.secretComposer = c
	}
}

// WithPluginVerifier wires compose-time plugin re-verification (§3.3). When set,
// a plugin that is a vault principal has its live bytes re-checked against its
// recorded keyed fingerprint right before secrets are delivered; a mismatch fails
// the spawn closed. Omitted (nil) leaves delivery gated only by authorization, as
// before. The production runtime supplies an adapter over the registry +
// .checksums + vault nonce.
func WithPluginVerifier(v PluginVerifier) Option {
	return func(d *Dispatcher) {
		d.pluginVerifier = v
	}
}

type pollLifecycleJob struct {
	Plugin      string
	Command     string
	ScheduleID  string
	SubmittedBy string
	Attempt     int
}

// New creates a new Dispatcher.
//
// As of Sprint 18 the dispatcher does not provision filesystem workspaces for
// jobs. Plugins that require an FS path manage it themselves.
func New(
	q *queue.Queue,
	st *state.Store,
	contexts *state.ContextStore,
	rt router.Engine,
	reg *plugin.Registry,
	hub *events.Hub,
	cfg *config.Config,
	opts ...Option,
) *Dispatcher {
	d := &Dispatcher{
		queue:          q,
		state:          st,
		contexts:       contexts,
		router:         rt,
		registry:       reg,
		events:         hub,
		cfg:            cfg,
		logger:         log.WithComponent("dispatch"),
		completions:    make(map[string]chan struct{}),
		pollLifecycles: make(map[string]pollLifecycleJob),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

// Start runs the main dispatch loop with bounded concurrency. It tracks active
// workers globally and per-plugin, filling available slots each tick or when a
// worker completes. Operators can set cfg.Service.MaxWorkers == 1 to force
// whole-system serial dispatch; otherwise the worker pool runs up to the
// configured global cap while plugin lane eligibility is controlled by each
// plugin's effective parallelism.
//
// This is a blocking call that runs until ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) error {
	maxWorkers := d.cfg.Service.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 1
	}

	d.logger.Info("dispatch loop started", "max_workers", maxWorkers)
	defer d.logger.Info("dispatch loop stopped")

	// activeTotal is local worker lifecycle coordination. Queue-backed running
	// rows are the source of truth for plugin lanes and dedupe-key eligibility.
	activeTotal := 0

	workerDone := make(chan struct{}, maxWorkers)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// fillWorkers tries to dequeue eligible jobs until all worker slots are full
	// or no eligible work remains.
	fillWorkers := func() {
		for activeTotal < maxWorkers {
			runningByPlugin, err := d.queue.RunningCountsByPlugin(ctx)
			if err != nil {
				d.logger.Error("failed to load running plugin counts", "error", err)
				return
			}

			// Build skip list: plugins at their parallelism cap.
			var skipPlugins []string
			for pluginName, count := range runningByPlugin {
				limit := d.pluginParallelism(pluginName)
				if count >= limit {
					skipPlugins = append(skipPlugins, pluginName)
				}
			}

			job, err := d.queue.DequeueEligible(ctx, skipPlugins, nil)
			if err != nil {
				d.logger.Error("failed to dequeue eligible job", "error", err)
				return
			}
			if job == nil {
				return // No eligible work
			}

			// Track this worker
			activeTotal++

			d.logger.Debug("dispatching job",
				"job_id", job.ID,
				"plugin", job.Plugin,
				"active_total", activeTotal,
				"running_plugin", runningByPlugin[job.Plugin]+1,
			)

			// Launch worker goroutine
			go func(j *queue.Job) {
				d.executeJob(ctx, j)
				workerDone <- struct{}{}
			}(job)
		}
	}

	for {
		select {
		case <-ctx.Done():
			// Drain active workers before returning
			d.logger.Info("dispatch loop shutting down, draining workers", "active", activeTotal)
			for activeTotal > 0 {
				<-workerDone
				activeTotal--
			}
			return ctx.Err()

		case <-workerDone:
			activeTotal--
			// A slot freed up — try to fill
			fillWorkers()

		case <-ticker.C:
			fillWorkers()
		}
	}
}

// pluginParallelism returns the effective parallelism limit for a plugin.
//
// If a plugin manifest declares concurrency_safe: false, it runs serial by
// default (limit=1). Operators can still explicitly override via config by
// setting plugins.<name>.parallelism > 1.
func (d *Dispatcher) pluginParallelism(pluginName string) int {
	limit := 1
	if pc, ok := d.cfg.Plugins[pluginName]; ok && pc.Parallelism > 0 {
		limit = pc.Parallelism
	}

	if plug, ok := d.registry.Get(pluginName); ok && !plug.ConcurrencySafe {
		if limit <= 1 {
			return 1
		}
		// Explicit operator override via config (>1) is honored.
	}

	return limit
}

// ExecuteJob runs a single job by spawning the plugin subprocess.
func (d *Dispatcher) ExecuteJob(ctx context.Context, job *queue.Job) {
	d.executeJob(ctx, job)
}

// executeJob runs a single job by spawning the plugin subprocess.
func (d *Dispatcher) executeJob(ctx context.Context, job *queue.Job) {
	jobLogger := d.logger.With("job_id", job.ID, "plugin", job.Plugin, "command", job.Command)
	jobLogger.Info("job started", "attempt", job.Attempt)
	d.publishPollStarted(job)
	d.events.Publish("job.started", map[string]any{
		"job_id":        job.ID,
		"plugin":        job.Plugin,
		"command":       job.Command,
		"attempt":       job.Attempt,
		"parent_job_id": deref(job.ParentJobID),
		"submitted_by":  job.SubmittedBy,
	})

	preflight, err := d.preflightJob(ctx, job)
	if err != nil {
		errMsg := fmt.Sprintf("job preflight failed: %v", err)
		jobLogger.Error(errMsg)
		d.events.Publish("job.preflight", map[string]any{
			"job_id":   job.ID,
			"plugin":   job.Plugin,
			"command":  job.Command,
			"decision": "fail",
			"reason":   errMsg,
		})
		d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	d.events.Publish("job.preflight", map[string]any{
		"job_id":   job.ID,
		"plugin":   job.Plugin,
		"command":  job.Command,
		"decision": string(preflight.decision),
		"reason":   preflight.reason,
	})
	if preflight.decision == preflightDecisionSkip {
		d.skipJob(ctx, jobLogger, job, preflight.requestContext, preflight.reason)
		return
	}

	requestContext := preflight.requestContext

	if isCoreSwitchJob(job) {
		d.executeCoreSwitch(ctx, job, requestContext, jobLogger)
		return
	}
	if isCoreRelayJob(job) {
		d.executeCoreRelay(ctx, job, requestContext, jobLogger)
		return
	}

	// Get plugin from registry
	plug, ok := d.registry.Get(job.Plugin)
	if !ok {
		errMsg := fmt.Sprintf("plugin %q not found in registry", job.Plugin)
		jobLogger.Error(errMsg)
		d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	// Check if plugin supports this command
	if !plug.SupportsCommand(job.Command) {
		errMsg := fmt.Sprintf("plugin %q does not support command %q", job.Plugin, job.Command)
		jobLogger.Error(errMsg)
		d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	// Get plugin configuration
	pluginCfg := d.cfg.Plugins[job.Plugin]
	if pluginCfg.Config == nil {
		pluginCfg.Config = make(map[string]any)
	}

	// Get plugin state
	pluginState, err := d.state.Get(ctx, job.Plugin)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get plugin state: %v", err)
		jobLogger.Error(errMsg)
		d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	// Unmarshal state into map
	var stateMap map[string]any
	if err := json.Unmarshal(pluginState, &stateMap); err != nil {
		errMsg := fmt.Sprintf("failed to unmarshal plugin state: %v", err)
		jobLogger.Error(errMsg)
		d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	// Determine timeout for this command
	timeout := d.getTimeout(pluginCfg.Timeouts, job.Command)
	deadline := time.Now().Add(timeout)

	// Build protocol request
	req := &protocol.Request{
		Protocol:   2,
		JobID:      job.ID,
		Command:    job.Command,
		Config:     pluginCfg.Config,
		State:      stateMap,
		Context:    requestContext,
		DeadlineAt: deadline,
	}

	// Compose the plugin's authorized vault secrets for stdin delivery. A revoked
	// principal (or any non-opt-out composer error) fails the job closed — the
	// plugin must not run when an explicit authorization signal is in error.
	secrets, err := composePluginSecrets(d.secretComposer, d.pluginVerifier, job.Plugin, jobLogger)
	if err != nil {
		errMsg := fmt.Sprintf("secret composition failed: %v", err)
		// Classify the fail-closed failure. A fingerprint mismatch is a possible
		// plugin swap — a SECURITY event escalated loudly and distinctly from a
		// benign denial (#25), via a SECURITY-marked log and a live hub event so
		// `system watch`/SSE subscribers and any external alerting catch it.
		op, outcome, security := composeFailureEscalation(err)
		if security {
			jobLogger.Error("SECURITY: plugin fingerprint mismatch at spawn — possible plugin swap; secrets withheld and spawn failed closed",
				"event", eventPluginFingerprintMismatch, "plugin", job.Plugin, "job_id", job.ID, "detail", err.Error())
			d.events.Publish(eventPluginFingerprintMismatch, map[string]any{
				"plugin": job.Plugin, "job_id": job.ID, "detail": err.Error(),
			})
		} else {
			jobLogger.Error(errMsg)
		}
		// Record the fail-closed outcome as a vault audit fact. The error text is
		// about principal identity/status, never a secret value. Best-effort: a
		// lost audit row warn-logs, it does not change the (already failed) job.
		if auditErr := d.state.AppendVaultAudit(ctx, state.VaultAuditEvent{
			Op: op, Principal: job.Plugin, Actor: "core", Outcome: outcome, Detail: err.Error(),
		}); auditErr != nil {
			jobLogger.Error("vault audit write failed", "op", op, "error", auditErr)
		}
		d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	req.Secrets = secrets

	// Include payload if present
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &req.Payload); err != nil {
			// If it's a 'handle' command, it might be a protocol.Event wrapper
			// We try to unmarshal into Payload first, if it's just a JSON object.
			jobLogger.Debug("payload is not a simple JSON object, attempting event unmarshal for handle")
		}
	}

	// For handle command, parse and include event payload
	if job.Command == "handle" && len(job.Payload) > 0 {
		var event protocol.Event
		if err := json.Unmarshal(job.Payload, &event); err != nil {
			errMsg := fmt.Sprintf("failed to unmarshal event payload: %v", err)
			jobLogger.Error(errMsg)
			d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
			return
		}

		// GOVERNANCE HYBRID MERGE:
		// Automatically merge accumulated context into the event payload
		// so plugins only have to look in one place. Immediate event data wins.
		if event.Payload == nil {
			if event.Type == "" {
				// Raw payload (e.g. webhook body) — not a protocol.Event wrapper.
				// Promote the raw JSON into event.Payload so switch/pipeline plugins
				// can address fields via payload.* without special-casing.
				var raw map[string]any
				if err := json.Unmarshal(job.Payload, &raw); err == nil {
					event.Payload = raw
				}
			}
			if event.Payload == nil {
				event.Payload = make(map[string]any)
			}
		}
		for k, v := range requestContext {
			if _, exists := event.Payload[k]; !exists {
				event.Payload[k] = v
			}
		}

		// WITH: Apply pipeline step's with: remap to override/add payload keys.
		// requestContext["ductile_pipeline"] and ["ductile_step_id"] are set by loadRequestContext.
		if d.router != nil {
			if pName, ok1 := requestContext["ductile_pipeline"].(string); ok1 {
				if sID, ok2 := requestContext["ductile_step_id"].(string); ok2 {
					if node, found := d.router.GetNode(pName, sID); found && len(node.With) > 0 {
						remappedPayload, err := applyWithRemap(event.Payload, node.With, requestContext)
						if err != nil {
							errMsg := fmt.Sprintf("apply with remap for %s/%s: %v", pName, sID, err)
							jobLogger.Error(errMsg)
							d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
							return
						}
						event.Payload = remappedPayload
					}
				}
			}
		}

		req.Event = &event
	}

	// Stopwatch: supervisor-side timing wraps the spawn. The dispatcher is
	// the sole producer of these records; they persist to the ductile DB
	// (job_stopwatch table) — not via baggage, because telemetry is system
	// data, not domain data (Hickey decomplecting).
	stepName, _ := requestContext["ductile_step_id"].(string)
	pipelineName, _ := requestContext["ductile_pipeline"].(string)
	pipelineInstanceID, _ := requestContext["ductile_pipeline_instance_id"].(string)
	sw := stopwatch.New(job.Plugin, stepName, job.Attempt)
	sw.MarkSpawn()

	// Spawn plugin and execute
	resp, respCompat, rawResp, _, stderr, exitCode, err := d.spawnPlugin(ctx, job.Plugin, plug.Entrypoint, req, timeout, jobLogger)
	swExitTime := time.Now()

	// Classify the spawn outcome into a closed-set status token, then
	// persist the Record to the supervisor's ledger. Writer failures are
	// logged but never propagate — losing a telemetry row must not crash
	// the job (Armstrong: keep the supervisor alive even when its ledger
	// stumbles).
	var swStatus string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		swStatus = stopwatch.StatusTimeout
	case err != nil:
		swStatus = stopwatch.StatusError
	case resp == nil:
		swStatus = stopwatch.StatusError
	case resp.Status == "error":
		swStatus = stopwatch.StatusError
	default:
		swStatus = stopwatch.StatusOK
	}
	var swSubs []map[string]any
	if resp != nil {
		// Plugin manifest may override the default sub-span cap up to
		// stopwatch.MaxSubsHardUpper. Look up the plugin's declared cap
		// (zero / nil means "use default") and pass it through.
		manifestMaxSubs := 0
		if pluginDef, ok := d.registry.Get(job.Plugin); ok && pluginDef.Stopwatch != nil {
			manifestMaxSubs = pluginDef.Stopwatch.MaxSubs
		}
		swSubs = stopwatch.SubsFromResponse(resp.StopwatchSubs, manifestMaxSubs, jobLogger)
	}
	swRec := sw.Finish(swExitTime, swStatus, 0, swSubs)
	// Detach the write from the dispatch context: the rows we most want
	// (timeout, error, shutdown-canceled) are exactly the ones whose
	// parent ctx may already be dead by the time we get here. Persisting
	// telemetry must not depend on the supervised work's ctx still being
	// live. context.WithoutCancel preserves any context values while
	// ignoring cancellation/deadline.
	writeCtx := context.WithoutCancel(ctx)
	if writeErr := d.state.RecordStopwatch(writeCtx, job.ID, swRec, pipelineName, pipelineInstanceID); writeErr != nil {
		jobLogger.Warn("stopwatch record write failed", "error", writeErr, "job_id", job.ID)
	}

	// Handle timeout (check if error is context.DeadlineExceeded)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			errMsg := fmt.Sprintf("plugin execution timed out after %v", timeout)
			jobLogger.Warn(errMsg)
			decision := decideRetryPolicy(nil, protocol.ResponseCompat{}, exitCode, job, pluginCfg, retryReasonTimeout)
			d.failOrRetry(ctx, jobLogger, job, pluginCfg, queue.StatusTimedOut, nil, &errMsg, &stderr, decision)
			return
		}

		// Handle other spawn errors
		errMsg := fmt.Sprintf("plugin spawn failed: %v", err)
		jobLogger.Error(errMsg)
		decision := decideRetryPolicy(resp, respCompat, exitCode, job, pluginCfg, retryReasonSpawnError)
		d.failOrRetry(ctx, jobLogger, job, pluginCfg, queue.StatusFailed, rawResp, &errMsg, &stderr, decision)
		return
	}

	// Handle protocol errors
	if resp == nil {
		errMsg := "plugin returned nil response"
		jobLogger.Error(errMsg)
		decision := decideRetryPolicy(nil, protocol.ResponseCompat{}, exitCode, job, pluginCfg, retryReasonNilResponse)
		d.failOrRetry(ctx, jobLogger, job, pluginCfg, queue.StatusFailed, rawResp, &errMsg, &stderr, decision)
		return
	}

	// Log plugin logs
	for _, entry := range resp.Logs {
		jobLogger.Info("plugin log", "level", entry.Level, "message", entry.Message)
	}

	// Handle response status
	if resp.Status == "error" {
		jobLogger.Warn("plugin returned error", "error", resp.Error)
		errMsg := resp.Error
		decision := decideRetryPolicy(resp, respCompat, exitCode, job, pluginCfg, retryReasonPluginError)
		d.failOrRetry(ctx, jobLogger, job, pluginCfg, queue.StatusFailed, rawResp, &errMsg, &stderr, decision)
		return
	}

	// Apply state updates
	if len(resp.StateUpdates) > 0 {
		updatesJSON, err := json.Marshal(resp.StateUpdates)
		if err != nil {
			errMsg := fmt.Sprintf("failed to marshal state updates: %v", err)
			jobLogger.Error(errMsg)
			d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, rawResp, &errMsg, &stderr)
			return
		}

		handledByFacts := false
		declaredFacts, err := pluginFactsFromStateUpdates(job, d.registry, updatesJSON)
		if err != nil {
			errMsg := fmt.Sprintf("failed to build plugin fact: %v", err)
			jobLogger.Error(errMsg)
			d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, rawResp, &errMsg, &stderr)
			return
		}
		for _, declaredFact := range declaredFacts {
			if _, _, err := d.state.RecordFact(ctx, declaredFact.Fact, declaredFact.CompatibilityView); err != nil {
				errMsg := fmt.Sprintf("failed to record plugin fact: %v", err)
				jobLogger.Error(errMsg)
				d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, rawResp, &errMsg, &stderr)
				return
			}
			handledByFacts = true
			jobLogger.Debug("recorded plugin fact", "fact_type", declaredFact.Fact.FactType, "job_id", declaredFact.Fact.JobID)
		}

		if !handledByFacts {
			if _, err := d.state.ShallowMerge(ctx, job.Plugin, updatesJSON); err != nil {
				errMsg := fmt.Sprintf("failed to apply state updates: %v", err)
				jobLogger.Error(errMsg)
				d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, rawResp, &errMsg, &stderr)
				return
			}
			jobLogger.Debug("applied state updates", "updates", resp.StateUpdates)
		}
	}

	// Process events (routing + orchestration)
	if len(resp.Events) > 0 {
		if strings.TrimSpace(resp.Result) != "" {
			for i := range resp.Events {
				if resp.Events[i].Payload == nil {
					resp.Events[i].Payload = make(map[string]any)
				}
				if _, exists := resp.Events[i].Payload["result"]; !exists {
					resp.Events[i].Payload["result"] = resp.Result
				}
			}
		}
		if err := d.routeEvents(ctx, job, resp.Events, jobLogger); err != nil {
			errMsg := fmt.Sprintf("failed to route events: %v", err)
			jobLogger.Error(errMsg)
			d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, rawResp, &errMsg, &stderr)
			return
		}
	} else if job.EventContextID != nil {
		// Plugin emitted no events but is running within a pipeline step — emit a
		// synthetic step-succeeded event so sequential successors can still advance.
		// Symmetric with routeSkippedStepSuccessors for skipped steps.
		synthetic := []protocol.Event{{
			Type:    "ductile.step.succeeded",
			Payload: map[string]any{"result": resp.Result},
		}}
		if err := d.routeEvents(ctx, job, synthetic, jobLogger); err != nil {
			errMsg := fmt.Sprintf("failed to route step-succeeded event: %v", err)
			jobLogger.Error(errMsg)
			d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, rawResp, &errMsg, &stderr)
			return
		}
	}

	// Mark job as succeeded
	d.maybeFireHooks(ctx, job, hookSignalForStatus(queue.StatusSucceeded), map[string]any{
		"job_id":  job.ID,
		"plugin":  job.Plugin,
		"command": job.Command,
		"status":  "succeeded",
		"result":  resp.Result,
	})
	d.completeJob(ctx, jobLogger, job.ID, job.Plugin, job.StartedAt, queue.StatusSucceeded, rawResp, nil, &stderr)
}

// spawnPlugin spawns the plugin subprocess, writes the request to stdin, and reads the response from stdout.
// Returns the parsed response, raw response bytes, raw stdout bytes, stderr output, exit code, and any error.
func (d *Dispatcher) spawnPlugin(
	ctx context.Context,
	pluginName string,
	entrypoint string,
	req *protocol.Request,
	timeout time.Duration,
	logger *slog.Logger,
) (*protocol.Response, protocol.ResponseCompat, json.RawMessage, []byte, string, int, error) {
	executor := newSubprocessExecutor(d.events, d.cfg.Service.PluginEnvPassthrough)
	return executor.execute(ctx, pluginName, entrypoint, req, timeout, logger)
}

func (d *Dispatcher) failOrRetry(
	ctx context.Context,
	logger *slog.Logger,
	job *queue.Job,
	pluginCfg config.PluginConf,
	status queue.Status,
	result json.RawMessage,
	lastError,
	stderr *string,
	decision retryDecision,
) {
	reason := decision.Reason
	if decision.Retryable && !decision.AttemptsExhausted {
		backoff := d.computeRetryDelay(pluginCfg.Retry, job.Attempt)
		nextRetryAt := time.Now().UTC().Add(backoff)
		nextAttempt := job.Attempt + 1

		if err := d.queue.UpdateJobForRecovery(ctx, job.ID, queue.StatusQueued, nextAttempt, &nextRetryAt, deref(lastError)); err != nil {
			logger.Error("failed to schedule retry; marking job terminal", "error", err)
			d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, status, result, lastError, stderr)
			return
		}

		logger.Warn(
			"retry scheduled",
			"job_id", job.ID,
			"attempt", job.Attempt,
			"max_attempts", job.MaxAttempts,
			"backoff_ms", backoff.Milliseconds(),
			"next_retry_at", nextRetryAt.Format(time.RFC3339Nano),
			"reason", reason,
		)

		d.events.Publish("job.retry_scheduled", map[string]any{
			"job_id":             job.ID,
			"attempt":            job.Attempt,
			"max_attempts":       job.MaxAttempts,
			"retry_count":        job.Attempt,
			"max_retries":        job.MaxAttempts,
			"retry_policy_owner": retryPolicyOwner,
			"plugin_retry_field": pluginRetryField,
			"backoff_ms":         backoff.Milliseconds(),
			"backoff_schedule": []int64{
				backoff.Milliseconds(),
			},
			"next_retry_at": nextRetryAt.Format(time.RFC3339Nano),
			"reason":        reason,
		})
		return
	}

	retryExhaustedReason := reason
	payload := map[string]any{
		"job_id":             job.ID,
		"attempts":           job.Attempt,
		"final_error":        deref(lastError),
		"reason":             retryExhaustedReason,
		"retry_policy_owner": retryPolicyOwner,
		"plugin_retry_field": pluginRetryField,
	}
	if decision.Retryable {
		retryExhaustedReason = retryReasonAttemptsExhausted
		payload["reason"] = retryExhaustedReason
		payload["retry_decision_reason"] = reason
	}
	d.events.Publish("job.retry_exhausted", payload)

	hookPayload := map[string]any{
		"job_id":  job.ID,
		"plugin":  job.Plugin,
		"command": job.Command,
		"status":  string(status),
		"reason":  reason,
	}
	if lastError != nil {
		hookPayload["error"] = *lastError
	}
	d.maybeFireHooks(ctx, job, hookSignalForStatus(status), hookPayload)
	d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, status, result, lastError, stderr)
}

func hookSignalForStatus(status queue.Status) string {
	switch status {
	case queue.StatusSucceeded:
		return "job.completed"
	case queue.StatusTimedOut:
		return "job.timed_out"
	case queue.StatusFailed, queue.StatusDead:
		return "job.failed"
	default:
		return ""
	}
}

func (d *Dispatcher) computeRetryDelay(retryCfg *config.RetryConfig, attempt int) time.Duration {
	base := config.DefaultPluginConf().Retry.BackoffBase
	if retryCfg != nil && retryCfg.BackoffBase > 0 {
		base = retryCfg.BackoffBase
	}

	jitter := time.Duration(0)
	maxJitter := base / 4
	if maxJitter > 0 {
		// #nosec G404 -- jitter is non-cryptographic scheduling noise.
		jitter = time.Duration(rand.Int63n(int64(maxJitter) + 1))
	}

	return calculateBackoffDelay(base, attempt, jitter)
}

func calculateBackoffDelay(base time.Duration, attempt int, jitter time.Duration) time.Duration {
	if base <= 0 {
		base = 30 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}

	factor := max(int64(math.Pow(2, float64(attempt-1))), 1)

	delay := time.Duration(factor) * base
	if delay < 0 {
		delay = base
	}
	delay += jitter
	if delay < 0 {
		delay = base
	}
	return delay
}

func (d *Dispatcher) loadRequestContext(ctx context.Context, job *queue.Job) (map[string]any, error) {
	out := make(map[string]any)
	out["ductile_plugin"] = job.Plugin

	if d.contexts == nil || job.EventContextID == nil {
		return out, nil
	}

	eventCtx, err := d.contexts.Get(ctx, *job.EventContextID)
	if err != nil {
		return nil, err
	}
	if len(eventCtx.AccumulatedJSON) > 0 {
		if err := json.Unmarshal(eventCtx.AccumulatedJSON, &out); err != nil {
			return nil, err
		}
	}
	if eventCtx.PipelineName != "" {
		out["ductile_pipeline"] = eventCtx.PipelineName
	}
	if eventCtx.StepID != "" {
		out["ductile_step_id"] = eventCtx.StepID
	}
	cp := state.RouteControlPlaneFromAccumulated(eventCtx.AccumulatedJSON)
	if cp.PipelineInstanceID != "" {
		out["ductile_pipeline_instance_id"] = cp.PipelineInstanceID
	}
	if cp.RouteDepth > 0 {
		out["ductile_route_depth"] = cp.RouteDepth
	}
	if cp.RouteMaxDepth > 0 {
		out["ductile_route_max_depth"] = cp.RouteMaxDepth
	}
	return out, nil
}

type routeEventsOptions struct {
	allowMissingExplicitBaggage bool
}

func (d *Dispatcher) routeEvents(ctx context.Context, job *queue.Job, events []protocol.Event, logger *slog.Logger) error {
	return d.routeEventsWithOptions(ctx, job, events, logger, routeEventsOptions{})
}

func (d *Dispatcher) routeEventsWithOptions(
	ctx context.Context,
	job *queue.Job,
	events []protocol.Event,
	logger *slog.Logger,
	opts routeEventsOptions,
) error {
	if d.router == nil {
		logger.Info("plugin emitted events (router not configured)", "count", len(events))
		return nil
	}

	rootContextIDs := make(map[string]string)
	var sourcePipeline, sourceStepID, sourcePipelineInstanceID string
	var sourceDepth, sourceMaxDepth int
	var sourceContextScope map[string]any
	if d.contexts != nil && job.EventContextID != nil {
		currentCtx, err := d.contexts.Get(ctx, *job.EventContextID)
		if err != nil {
			return fmt.Errorf("load current context %q: %w", *job.EventContextID, err)
		}
		sourcePipeline = currentCtx.PipelineName
		sourceStepID = currentCtx.StepID
		cp := state.RouteControlPlaneFromAccumulated(currentCtx.AccumulatedJSON)
		sourcePipelineInstanceID = cp.PipelineInstanceID
		sourceDepth = cp.RouteDepth
		sourceMaxDepth = cp.RouteMaxDepth
		if len(currentCtx.AccumulatedJSON) > 0 {
			scope := make(map[string]any)
			if err := json.Unmarshal(currentCtx.AccumulatedJSON, &scope); err != nil {
				return fmt.Errorf("decode source context accumulated json: %w", err)
			}
			sourceContextScope = scope
		}
	}

	for i := range events {
		if events[i].EventID == "" {
			events[i].EventID = uuid.NewString()
		}
		if events[i].Timestamp.IsZero() {
			events[i].Timestamp = time.Now().UTC()
		}
		if events[i].Source == "" {
			events[i].Source = job.Plugin
		}
		ev := events[i]

		req := router.Request{
			SourcePlugin:             job.Plugin,
			SourceJobID:              job.ID,
			SourceContextID:          deref(job.EventContextID),
			SourcePipeline:           sourcePipeline,
			SourceStepID:             sourceStepID,
			SourcePipelineInstanceID: sourcePipelineInstanceID,
			SourceDepth:              sourceDepth,
			SourceMaxDepth:           sourceMaxDepth,
			SourceEventID:            ev.EventID,
			SourceContext:            sourceContextScope,
			Event:                    ev,
		}

		nextDispatches, err := d.router.Next(ctx, req)
		if err != nil {
			return fmt.Errorf("resolve routes for event %q: %w", ev.Type, err)
		}
		if len(nextDispatches) == 0 {
			logger.Debug("no route matched for event", "event_type", ev.Type, "source_plugin", job.Plugin)
			continue
		}

		for _, next := range nextDispatches {
			if next.Event.Payload == nil {
				next.Event.Payload = make(map[string]any)
			}
			if _, exists := next.Event.Payload["ductile_upstream_plugin"]; !exists {
				next.Event.Payload["ductile_upstream_plugin"] = job.Plugin
			}
			if sourcePipeline != "" {
				if _, exists := next.Event.Payload["ductile_upstream_pipeline"]; !exists {
					next.Event.Payload["ductile_upstream_pipeline"] = sourcePipeline
				}
			}
			if sourceStepID != "" {
				if _, exists := next.Event.Payload["ductile_upstream_step_id"]; !exists {
					next.Event.Payload["ductile_upstream_step_id"] = sourceStepID
				}
			}

			// Admission boundary (P2-09): decide BEFORE writing routed
			// event_context rows whether this dispatch is a replay the queue
			// would dedupe. On AdmissionReplay we skip context creation AND
			// enqueue entirely; the queue's dedupe path remains authoritative
			// on job-side idempotency but we avoid leaking orphan lineage rows.
			if d.admitter != nil {
				admission, err := d.admitter.Admit(ctx, state.AdmissionInput{
					DedupeKey: next.Event.DedupeKey,
					Plugin:    next.Plugin,
					Command:   next.Command,
				})
				if err != nil {
					return fmt.Errorf("admit routed dispatch (%s:%s): %w", next.Plugin, next.Command, err)
				}
				if admission.Decision == state.AdmissionReplay {
					logger.Info("skipping routed job (admission: replay)",
						"plugin", next.Plugin,
						"command", next.Command,
						"dedup_key", strings.TrimSpace(next.Event.DedupeKey),
						"existing_job_id", admission.ExistingJobID,
					)
					continue
				}
			}

			payload, err := json.Marshal(next.Event)
			if err != nil {
				return fmt.Errorf("marshal routed event payload: %w", err)
			}

			var contextID *string
			if d.contexts != nil {
				updates, err := d.contextUpdatesForDispatch(ctx, next, opts)
				if err != nil {
					return err
				}
				parentCtxID, err := d.parentContextIDForDispatch(ctx, rootContextIDs, next)
				if err != nil {
					return err
				}
				var createdCtx *state.EventContext
				createdCtx, err = d.contexts.Create(ctx, parentCtxID, next.PipelineName, next.StepID, updates)
				if err != nil {
					return fmt.Errorf("create routed context (%s:%s): %w", next.PipelineName, next.StepID, err)
				}
				contextID = &createdCtx.ID
			}

			sourceEventID := next.SourceEventID
			enqueueReq := queue.EnqueueRequest{
				Plugin:         next.Plugin,
				Command:        next.Command,
				Payload:        payload,
				SubmittedBy:    "route",
				ParentJobID:    strPtrOrNil(next.ParentJobID),
				EventContextID: contextID,
				SourceEventID:  strPtrOrNil(sourceEventID),
				MaxAttempts:    config.MaxAttemptsForPlugin(d.cfg.Plugins[next.Plugin]),
			}
			if dedupeKey := strings.TrimSpace(next.Event.DedupeKey); dedupeKey != "" {
				enqueueReq.DedupeKey = &dedupeKey
			}
			childJobID, err := d.queue.Enqueue(ctx, enqueueReq)
			if err != nil {
				if errors.Is(err, queue.ErrDedupeDrop) {
					logger.Info("skipping routed job (deduplicated)",
						"plugin", next.Plugin,
						"dedup_key", strings.TrimSpace(next.Event.DedupeKey),
					)
					continue
				}
				if errors.Is(err, queue.ErrSourceEventConflict) {
					logger.Info("skipping routed job (source-event redelivery dedupe)",
						"plugin", next.Plugin,
						"command", next.Command,
						"source_event_id", sourceEventID,
					)
					continue
				}
				return fmt.Errorf("enqueue routed job for plugin %q: %w", next.Plugin, err)
			}

			d.events.Publish("router.enqueued", map[string]any{
				"job_id":           childJobID,
				"parent_job_id":    job.ID,
				"plugin":           next.Plugin,
				"command":          next.Command,
				"pipeline":         next.PipelineName,
				"step_id":          next.StepID,
				"event_context_id": deref(contextID),
			})

			logger.Info(
				"enqueued routed job",
				"event_type", ev.Type,
				"to_plugin", next.Plugin,
				"to_command", next.Command,
				"child_job_id", childJobID,
				"pipeline", next.PipelineName,
				"step_id", next.StepID,
				"event_context_id", deref(contextID),
			)
		}
	}

	return nil
}

func (d *Dispatcher) ensurePipelineInstanceRootContext(ctx context.Context, cache map[string]string, next router.Dispatch) (string, error) {
	key := next.PipelineName + "\x00" + next.PipelineInstanceID
	if rootContextID, ok := cache[key]; ok {
		return rootContextID, nil
	}

	updates, err := state.WithPipelineInstanceID(nil, next.PipelineInstanceID)
	if err != nil {
		return "", fmt.Errorf("seed pipeline instance context for %s: %w", next.PipelineName, err)
	}
	updates, err = state.WithRouteDepth(updates, 0)
	if err != nil {
		return "", fmt.Errorf("seed pipeline route depth context for %s: %w", next.PipelineName, err)
	}
	if next.RouteMaxDepth > 0 {
		updates, err = state.WithRouteMaxDepth(updates, next.RouteMaxDepth)
		if err != nil {
			return "", fmt.Errorf("seed pipeline max depth context for %s: %w", next.PipelineName, err)
		}
	}
	rootCtx, err := d.contexts.Create(ctx, nil, next.PipelineName, "", updates)
	if err != nil {
		return "", fmt.Errorf("create pipeline instance root context (%s): %w", next.PipelineName, err)
	}
	cache[key] = rootCtx.ID
	return rootCtx.ID, nil
}

func (d *Dispatcher) parentContextIDForDispatch(ctx context.Context, cache map[string]string, next router.Dispatch) (*string, error) {
	parentCtxID := strPtrOrNil(next.ParentContextID)
	if d.contexts == nil || strings.TrimSpace(next.PipelineName) == "" || strings.TrimSpace(next.PipelineInstanceID) == "" {
		return parentCtxID, nil
	}
	if parentCtxID == nil {
		rootContextID, err := d.ensurePipelineInstanceRootContext(ctx, cache, next)
		if err != nil {
			return nil, err
		}
		return &rootContextID, nil
	}

	parentCtx, err := d.contexts.Get(ctx, *parentCtxID)
	if err != nil {
		return nil, fmt.Errorf("load parent context %q for routed dispatch: %w", *parentCtxID, err)
	}
	parentInstanceID := state.PipelineInstanceIDFromAccumulated(parentCtx.AccumulatedJSON)
	if parentCtx.PipelineName == next.PipelineName && parentInstanceID == next.PipelineInstanceID {
		return parentCtxID, nil
	}

	rootContextID, err := d.ensurePipelineInstanceRootContext(ctx, cache, next)
	if err != nil {
		return nil, err
	}
	return &rootContextID, nil
}

type preflightDecision string

const (
	preflightDecisionRun  preflightDecision = "run"
	preflightDecisionSkip preflightDecision = "skip"
)

type jobPreflight struct {
	decision       preflightDecision
	reason         string
	requestContext map[string]any
}

type conditionEvaluation struct {
	shouldSkip bool
	reason     string
}

func (d *Dispatcher) preflightJob(ctx context.Context, job *queue.Job) (jobPreflight, error) {
	requestContext, err := d.loadRequestContext(ctx, job)
	if err != nil {
		return jobPreflight{}, fmt.Errorf("load event context: %w", err)
	}

	if d.router != nil {
		evaluation, err := d.evaluateStepCondition(ctx, job, requestContext)
		if err != nil {
			return jobPreflight{}, fmt.Errorf("evaluate step condition: %w", err)
		}
		if evaluation.shouldSkip {
			return jobPreflight{
				decision:       preflightDecisionSkip,
				reason:         evaluation.reason,
				requestContext: requestContext,
			}, nil
		}
	}

	return jobPreflight{
		decision:       preflightDecisionRun,
		requestContext: requestContext,
	}, nil
}

func (d *Dispatcher) evaluateStepCondition(_ context.Context, job *queue.Job, requestContext map[string]any) (conditionEvaluation, error) {
	if d.router == nil {
		return conditionEvaluation{}, nil
	}

	pipelineName, _ := requestContext["ductile_pipeline"].(string)
	stepID, _ := requestContext["ductile_step_id"].(string)
	if pipelineName == "" || stepID == "" {
		return conditionEvaluation{}, nil
	}

	node, ok := d.router.GetNode(pipelineName, stepID)
	if !ok || node.Condition == nil || node.Kind == dsl.NodeKindSwitch {
		return conditionEvaluation{}, nil
	}

	scope := conditions.Scope{
		Payload: eventPayloadForCondition(job.Payload),
		Context: requestContext,
		Config:  pluginConfigMap(d.cfg, job.Plugin),
	}
	matched, err := conditions.Eval(node.Condition, scope)
	if err != nil {
		return conditionEvaluation{}, err
	}
	if matched {
		return conditionEvaluation{}, nil
	}
	return conditionEvaluation{shouldSkip: true, reason: "if condition evaluated false"}, nil
}

func pluginConfigMap(cfg *config.Config, pluginName string) map[string]any {
	if cfg == nil {
		return map[string]any{}
	}
	pluginCfg, ok := cfg.Plugins[pluginName]
	if !ok || pluginCfg.Config == nil {
		return map[string]any{}
	}
	return pluginCfg.Config
}

func eventPayloadForCondition(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}

	var event protocol.Event
	if err := json.Unmarshal(raw, &event); err == nil && (event.Type != "" || event.Payload != nil) {
		if event.Payload != nil {
			return event.Payload
		}
		return map[string]any{}
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err == nil {
		return payload
	}
	return map[string]any{}
}

func eventFromJobPayload(raw json.RawMessage) (protocol.Event, error) {
	var event protocol.Event
	if err := json.Unmarshal(raw, &event); err == nil && (event.Type != "" || event.Payload != nil || event.EventID != "") {
		if event.Payload == nil {
			event.Payload = map[string]any{}
		}
		return event, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return protocol.Event{}, err
	}
	return protocol.Event{Payload: payload}, nil
}

func (d *Dispatcher) skipJob(ctx context.Context, logger *slog.Logger, job *queue.Job, requestContext map[string]any, reason string) {
	logger.Info("job skipped", "reason", reason)
	d.events.Publish("job.skipped", map[string]any{
		"job_id":  job.ID,
		"plugin":  job.Plugin,
		"command": job.Command,
		"reason":  reason,
	})

	result, err := json.Marshal(map[string]any{
		"status": "skipped",
		"reason": reason,
	})
	if err != nil {
		errMsg := fmt.Sprintf("failed to marshal skipped job result: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	// Route successors before marking the skipped job terminal.
	//
	// A skipped first-class `if` step can still expand the execution tree. For
	// synchronous callers, notifying completion before successor enqueue can make
	// the tree appear finished too early.
	if err := d.routeSkippedStepSuccessors(ctx, logger, job, reason); err != nil {
		errMsg := fmt.Sprintf("failed to route successors after skip: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusSkipped, result, &reason, nil)
}

func (d *Dispatcher) routeSkippedStepSuccessors(ctx context.Context, logger *slog.Logger, job *queue.Job, reason string) error {
	event := protocol.Event{
		Type:    "ductile.step.skipped",
		Payload: map[string]any{},
	}
	if len(job.Payload) > 0 {
		parsed, err := eventFromJobPayload(job.Payload)
		if err != nil {
			return fmt.Errorf("parse skipped job payload: %w", err)
		}
		event = parsed
		if event.Payload == nil {
			event.Payload = map[string]any{}
		}
	}
	if err := d.routeEventsWithOptions(ctx, job, []protocol.Event{event}, logger, routeEventsOptions{
		allowMissingExplicitBaggage: true,
	}); err != nil {
		return err
	}
	return nil
}

func (d *Dispatcher) contextUpdatesForDispatch(ctx context.Context, next router.Dispatch, opts routeEventsOptions) (json.RawMessage, error) {
	var (
		updates json.RawMessage
		err     error
	)
	if d.router != nil && strings.TrimSpace(next.PipelineName) != "" && strings.TrimSpace(next.StepID) != "" {
		if node, found := d.router.GetNode(next.PipelineName, next.StepID); found && node.Kind == dsl.NodeKindUses && node.Baggage != nil && !node.Baggage.Empty() {
			parentContext, err := d.contextMap(ctx, next.ParentContextID)
			if err != nil {
				return nil, err
			}
			claims, err := baggage.ApplyClaims(next.Event.Payload, node.Baggage, parentContext)
			if err != nil {
				if opts.allowMissingExplicitBaggage && errors.Is(err, baggage.ErrPathNotFound) {
					conditionFalse, conditionErr := skippedSuccessorConditionFalse(node, next.Event.Payload, parentContext)
					if conditionErr != nil {
						return nil, fmt.Errorf("evaluate skipped successor condition for %s:%s: %w", next.PipelineName, next.StepID, conditionErr)
					}
					if !conditionFalse {
						return nil, fmt.Errorf("apply baggage claims for %s:%s: %w", next.PipelineName, next.StepID, err)
					}
					d.logger.Warn("skipped-step successor missing explicit baggage source; target condition is false, inheriting parent context for skip evaluation",
						"pipeline", next.PipelineName,
						"step_id", next.StepID,
						"error", err,
					)
					updates = json.RawMessage(`{}`)
					goto controlPlane
				}
				return nil, fmt.Errorf("apply baggage claims for %s:%s: %w", next.PipelineName, next.StepID, err)
			}
			raw, err := json.Marshal(claims)
			if err != nil {
				return nil, fmt.Errorf("marshal baggage updates: %w", err)
			}
			updates = raw
		}
	}

controlPlane:
	if next.RouteMaxDepth > 0 {
		updates, err = state.WithRouteMaxDepth(updates, next.RouteMaxDepth)
		if err != nil {
			return nil, fmt.Errorf("seed routed context max depth: %w", err)
		}
	}
	if strings.TrimSpace(next.PipelineInstanceID) != "" {
		updates, err = state.WithPipelineInstanceID(updates, next.PipelineInstanceID)
		if err != nil {
			return nil, fmt.Errorf("seed routed context pipeline instance id: %w", err)
		}
	}
	if next.RouteDepth > 0 {
		updates, err = state.WithRouteDepth(updates, next.RouteDepth)
		if err != nil {
			return nil, fmt.Errorf("seed routed context depth: %w", err)
		}
	}
	return updates, nil
}

func skippedSuccessorConditionFalse(node dsl.Node, payload map[string]any, parentContext map[string]any) (bool, error) {
	if node.Condition == nil {
		return false, nil
	}
	matched, err := conditions.Eval(node.Condition, conditions.Scope{
		Payload: payload,
		Context: parentContext,
	})
	if err != nil {
		return false, err
	}
	return !matched, nil
}

func isCoreSwitchJob(job *queue.Job) bool {
	if job == nil {
		return false
	}
	return strings.TrimSpace(job.Plugin) == "core.switch" && strings.TrimSpace(job.Command) == "handle"
}

func isCoreRelayJob(job *queue.Job) bool {
	if job == nil {
		return false
	}
	return strings.TrimSpace(job.Plugin) == "core.relay" && strings.TrimSpace(job.Command) == "handle"
}

func (d *Dispatcher) executeCoreSwitch(ctx context.Context, job *queue.Job, requestContext map[string]any, logger *slog.Logger) {
	if d.router == nil {
		errMsg := "core.switch requires router"
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	pipelineName, _ := requestContext["ductile_pipeline"].(string)
	stepID, _ := requestContext["ductile_step_id"].(string)
	node, ok := d.router.GetNode(pipelineName, stepID)
	if !ok || node.Kind != dsl.NodeKindSwitch || node.Condition == nil {
		errMsg := fmt.Sprintf("core.switch node %s/%s missing condition", pipelineName, stepID)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	scope := conditions.Scope{
		Payload: eventPayloadForCondition(job.Payload),
		Context: requestContext,
	}
	matched, err := conditions.Eval(node.Condition, scope)
	if err != nil {
		errMsg := fmt.Sprintf("core.switch evaluate condition: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	event, err := eventFromJobPayload(job.Payload)
	if err != nil {
		errMsg := fmt.Sprintf("core.switch parse event: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	if matched {
		event.Type = "ductile.switch.true"
	} else {
		event.Type = "ductile.switch.false"
	}

	if err := d.routeEvents(ctx, job, []protocol.Event{event}, logger); err != nil {
		errMsg := fmt.Sprintf("core.switch route events: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	result, err := json.Marshal(map[string]any{
		"matched": matched,
		"event":   event.Type,
	})
	if err != nil {
		errMsg := fmt.Sprintf("core.switch marshal result: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusSucceeded, result, nil, nil)
}

func (d *Dispatcher) executeCoreRelay(ctx context.Context, job *queue.Job, requestContext map[string]any, logger *slog.Logger) {
	if d.router == nil {
		errMsg := "core.relay requires router"
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	pipelineName, _ := requestContext["ductile_pipeline"].(string)
	stepID, _ := requestContext["ductile_step_id"].(string)
	node, ok := d.router.GetNode(pipelineName, stepID)
	if !ok || node.Kind != dsl.NodeKindRelay || node.Relay == nil {
		errMsg := fmt.Sprintf("core.relay node %s/%s missing relay spec", pipelineName, stepID)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	event, err := eventFromJobPayload(job.Payload)
	if err != nil {
		errMsg := fmt.Sprintf("core.relay parse event: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	if event.Payload == nil {
		event.Payload = make(map[string]any)
	}

	envelope, err := d.relayEnvelopeFromNode(node.Relay, event, requestContext, job)
	if err != nil {
		errMsg := fmt.Sprintf("core.relay build envelope: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	sender, err := relay.NewSender(d.cfg)
	if err != nil {
		errMsg := fmt.Sprintf("core.relay configure sender: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	accepted, err := sender.Send(ctx, node.Relay.To, envelope)
	if err != nil {
		errMsg := fmt.Sprintf("core.relay send: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	synthetic := protocol.Event{
		Type: "ductile.step.succeeded",
		Payload: map[string]any{
			"relay_to":                  strings.TrimSpace(node.Relay.To),
			"relay_event_type":          envelope.Event.Type,
			"relay_receiver_event_id":   accepted.ReceiverEventID,
			"relay_receiver_peer":       accepted.Peer,
			"relay_receiver_job_id":     accepted.JobID,
			"relay_acceptance_status":   accepted.Status,
			"relay_acceptance_event_id": accepted.ReceiverEventID,
		},
	}
	if err := d.routeEvents(ctx, job, []protocol.Event{synthetic}, logger); err != nil {
		errMsg := fmt.Sprintf("core.relay route successors: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}

	result, err := json.Marshal(map[string]any{
		"status":   "relayed",
		"to":       strings.TrimSpace(node.Relay.To),
		"event":    envelope.Event.Type,
		"accepted": accepted,
	})
	if err != nil {
		errMsg := fmt.Sprintf("core.relay marshal result: %v", err)
		d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusFailed, nil, &errMsg, nil)
		return
	}
	d.completeJob(ctx, logger, job.ID, job.Plugin, job.StartedAt, queue.StatusSucceeded, result, nil, nil)
}

func (d *Dispatcher) relayEnvelopeFromNode(spec *dsl.RelaySpec, event protocol.Event, requestContext map[string]any, job *queue.Job) (relay.Envelope, error) {
	payload := event.Payload
	if payload == nil {
		payload = make(map[string]any)
	}
	scope := conditions.Scope{Payload: payload, Context: requestContext}

	relayPayload := clonePayloadMap(payload)
	if len(spec.With) > 0 {
		relayPayload = make(map[string]any, len(spec.With))
		for _, key := range sortedWithKeys(spec.With) {
			value, err := evalRelayExpression(spec.With[key], scope)
			if err != nil {
				return relay.Envelope{}, fmt.Errorf("relay.with.%s: %w", key, err)
			}
			relayPayload[key] = value
		}
	}

	dedupeKey := strings.TrimSpace(event.DedupeKey)
	if expr := strings.TrimSpace(spec.DedupeKey); expr != "" {
		value, err := evalRelayExpression(expr, scope)
		if err != nil {
			return relay.Envelope{}, fmt.Errorf("relay.dedupe_key: %w", err)
		}
		if value != nil {
			dedupeKey = fmt.Sprint(value)
		} else {
			dedupeKey = ""
		}
	}

	baggageClaims, err := baggage.ApplyClaims(payload, spec.Baggage, requestContext)
	if err != nil {
		return relay.Envelope{}, fmt.Errorf("relay.baggage: %w", err)
	}

	return relay.Envelope{
		Event: relay.EnvelopeEvent{
			Type:      strings.TrimSpace(spec.Event),
			Payload:   relayPayload,
			DedupeKey: dedupeKey,
		},
		Origin: relay.EnvelopeOrigin{
			Instance: strings.TrimSpace(d.cfg.Service.Name),
			Plugin:   job.Plugin,
			JobID:    job.ID,
			EventID:  event.EventID,
		},
		Baggage: baggageClaims,
	}, nil
}

func evalRelayExpression(expr string, scope conditions.Scope) (any, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("expression is empty")
	}
	if strings.Contains(trimmed, "{") || strings.Contains(trimmed, "}") {
		return evalWithTemplate(trimmed, scope)
	}
	present, value, err := conditions.ResolvePath(scope, trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", trimmed, err)
	}
	if !present {
		return nil, fmt.Errorf("resolve %q: path not found", trimmed)
	}
	return value, nil
}

func (d *Dispatcher) contextMap(ctx context.Context, contextID string) (map[string]any, error) {
	if d.contexts == nil || strings.TrimSpace(contextID) == "" {
		return map[string]any{}, nil
	}

	eventCtx, err := d.contexts.Get(ctx, contextID)
	if err != nil {
		return nil, fmt.Errorf("load parent context %q: %w", contextID, err)
	}
	out := map[string]any{}
	if len(eventCtx.AccumulatedJSON) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(eventCtx.AccumulatedJSON, &out); err != nil {
		return nil, fmt.Errorf("decode parent accumulated context: %w", err)
	}
	return out, nil
}

func strPtrOrNil(v string) *string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	out := v
	return &out
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

type declaredPluginFact struct {
	Fact              state.PluginFact
	CompatibilityView string
}

func pluginFactsFromStateUpdates(job *queue.Job, registry *plugin.Registry, updates json.RawMessage) ([]declaredPluginFact, error) {
	if job == nil || registry == nil || len(updates) == 0 {
		return nil, nil
	}

	plug, ok := registry.Get(job.Plugin)
	if !ok {
		return nil, nil
	}

	rules := plug.FactOutputRulesForCommand(job.Command)
	if len(rules) == 0 {
		return nil, nil
	}

	facts := make([]declaredPluginFact, 0, len(rules))
	for _, rule := range rules {
		if rule.From != "state_updates" {
			continue
		}
		fact, err := buildPluginFact(job, updates, rule.FactType)
		if err != nil {
			return nil, err
		}
		facts = append(facts, declaredPluginFact{
			Fact:              *fact,
			CompatibilityView: rule.CompatibilityView,
		})
	}

	return facts, nil
}

func buildPluginFact(job *queue.Job, updates json.RawMessage, factType string) (*state.PluginFact, error) {
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(updates, &snapshot); err != nil {
		return nil, fmt.Errorf("normalize %s snapshot: %w", factType, err)
	}
	if snapshot == nil {
		snapshot = map[string]json.RawMessage{}
	}
	return &state.PluginFact{
		ID:         uuid.NewString(),
		PluginName: job.Plugin,
		FactType:   factType,
		JobID:      job.ID,
		Command:    job.Command,
		FactJSON:   updates,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

// getTimeout returns the timeout for a given command, falling back to defaults.
// Operator-defined per-command timeouts under plugins.<name>.timeouts.<command>
// (anything other than the four core lifecycle commands) are honored via the
// Overrides map. P2-05.
func (d *Dispatcher) getTimeout(timeouts *config.TimeoutsConfig, command string) time.Duration {
	if timeouts == nil {
		timeouts = config.DefaultPluginConf().Timeouts
	}

	if override, ok := timeouts.Overrides[command]; ok && override > 0 {
		return override
	}

	switch command {
	case "poll":
		if timeouts.Poll > 0 {
			return timeouts.Poll
		}
		return 60 * time.Second
	case "handle":
		if timeouts.Handle > 0 {
			return timeouts.Handle
		}
		return 120 * time.Second
	case "health":
		if timeouts.Health > 0 {
			return timeouts.Health
		}
		return 10 * time.Second
	case "init":
		if timeouts.Init > 0 {
			return timeouts.Init
		}
		return 30 * time.Second
	default:
		return 60 * time.Second
	}
}

func (d *Dispatcher) publishPollStarted(job *queue.Job) {
	if job == nil || !d.isSchedulerPollJob(job) {
		return
	}

	scheduleID := scheduleIDFromDedupeKey(job.Plugin, job.Command, job.DedupeKey)
	lifecycle := pollLifecycleJob{
		Plugin:      job.Plugin,
		Command:     job.Command,
		ScheduleID:  scheduleID,
		SubmittedBy: job.SubmittedBy,
		Attempt:     job.Attempt,
	}

	d.mu.Lock()
	d.pollLifecycles[job.ID] = lifecycle
	d.mu.Unlock()

	d.events.Publish("poll.started", map[string]any{
		"job_id":       job.ID,
		"plugin":       job.Plugin,
		"command":      job.Command,
		"schedule_id":  scheduleID,
		"submitted_by": job.SubmittedBy,
		"attempt":      job.Attempt,
	})
}

func (d *Dispatcher) publishPollCompleted(jobID string, status queue.Status, jobEventData map[string]any) {
	d.mu.Lock()
	lifecycle, ok := d.pollLifecycles[jobID]
	if ok {
		delete(d.pollLifecycles, jobID)
	}
	d.mu.Unlock()
	if !ok {
		return
	}

	payload := map[string]any{
		"job_id":       jobID,
		"plugin":       lifecycle.Plugin,
		"command":      lifecycle.Command,
		"schedule_id":  lifecycle.ScheduleID,
		"submitted_by": lifecycle.SubmittedBy,
		"attempt":      lifecycle.Attempt,
		"status":       string(status),
		"outcome":      string(status),
	}
	if duration, ok := jobEventData["duration_ms"]; ok {
		payload["duration_ms"] = duration
	}
	if errText, ok := jobEventData["error"]; ok {
		payload["error"] = errText
	}

	d.events.Publish("poll.completed", payload)
}

func (d *Dispatcher) isSchedulerPollJob(job *queue.Job) bool {
	if job == nil || d.cfg == nil {
		return false
	}
	submitter := strings.TrimSpace(d.cfg.Service.Name)
	return strings.TrimSpace(submitter) != "" &&
		strings.TrimSpace(job.SubmittedBy) == submitter &&
		strings.TrimSpace(job.Command) == "poll"
}

func scheduleIDFromDedupeKey(pluginName, command string, dedupeKey *string) string {
	if dedupeKey == nil {
		return ""
	}
	raw := strings.TrimSpace(*dedupeKey)
	prefix := pluginName + ":" + command + ":"
	if raw == "" || !strings.HasPrefix(raw, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(raw, prefix)
	if rest == "" {
		return ""
	}
	parts := strings.Split(rest, ":")
	return strings.TrimSpace(parts[0])
}

// completeJob marks a job as complete with the given status and logs the outcome.
func (d *Dispatcher) completeJob(ctx context.Context, logger *slog.Logger, jobID string, plugin string, startTime *time.Time, status queue.Status, result json.RawMessage, lastError, stderr *string) {
	duration := time.Duration(0)
	if startTime != nil {
		duration = time.Since(*startTime)
	}
	logger.Info("job completed", "status", string(status), "duration", duration.String())

	eventData := map[string]any{
		"job_id":      jobID,
		"plugin":      plugin,
		"status":      string(status),
		"duration_ms": duration.Milliseconds(),
	}
	if lastError != nil {
		eventData["error"] = *lastError
		msg := fmt.Sprintf("Job failed [%s]: %s", plugin, *lastError)
		eventData["message"] = msg
		eventData["text"] = msg
	}

	switch status {
	case queue.StatusSucceeded:
		d.events.Publish("job.completed", eventData)
	case queue.StatusTimedOut:
		msg := fmt.Sprintf("Job timed out [%s]", plugin)
		eventData["message"] = msg
		eventData["text"] = msg
		d.events.Publish("job.timed_out", eventData)
	case queue.StatusFailed, queue.StatusDead:
		if _, ok := eventData["message"]; !ok {
			msg := fmt.Sprintf("Job failed [%s]", plugin)
			eventData["message"] = msg
			eventData["text"] = msg
		}
		d.events.Publish("job.failed", eventData)
	}
	d.publishPollCompleted(jobID, status, eventData)

	if err := d.queue.CompleteWithResult(ctx, jobID, status, result, lastError, stderr); err != nil {
		d.logger.Error("failed to complete job", "job_id", jobID, "error", err)
	}

	// Notify any synchronous waiters
	d.notifyCompletion(jobID)
}

// hookChainDepth walks the ParentJobID chain from `job` upward, counting how many
// "hook"-submitted jobs are in the lineage (including `job` itself if applicable).
// A pristine root job (any SubmittedBy != "hook") has depth 0; a hook job whose
// parent is a root has depth 1; a hook-of-a-hook has depth 2; and so on.
//
// P2-11: fail closed. If a parent lookup fails (e.g. the parent row was pruned
// by retention while a long-running chain was still active), we return an error
// so the caller refuses the enqueue rather than silently treating the chain as
// fresh and resetting the cap.
func (d *Dispatcher) hookChainDepth(ctx context.Context, job *queue.Job) (int, error) {
	// hookWalkSafetyCap bounds the parent walk against pathologically corrupted
	// lineage (e.g. a cycle introduced by a future schema change or external
	// mutation). Legitimate chains are bounded by HookMaxDepth which is far
	// smaller than this cap; tripping it always indicates a bug or tampering.
	const hookWalkSafetyCap = 1024
	depth := 0
	currentSubmittedBy := job.SubmittedBy
	currentParent := job.ParentJobID
	for currentSubmittedBy == "hook" {
		depth++
		if depth > hookWalkSafetyCap {
			return 0, fmt.Errorf("hook depth walk exceeded safety cap %d starting at job %s; possible lineage cycle", hookWalkSafetyCap, job.ID)
		}
		if currentParent == nil {
			return 0, fmt.Errorf("hook job %s has no parent_job_id; lineage corrupted", job.ID)
		}
		parent, err := d.queue.GetJobByID(ctx, *currentParent)
		if err != nil {
			return 0, fmt.Errorf("hook depth walk: lookup parent %s: %w", *currentParent, err)
		}
		currentSubmittedBy = parent.SubmittedBy
		currentParent = parent.ParentJobID
	}
	return depth, nil
}

// maybeFireHooks fires on-hook lifecycle pipelines for a completed root job if the plugin
// has notify_on_complete: true in its config. Pipeline steps and retried jobs are skipped.
//
// P2-11: enqueue is refused when the existing hook chain depth plus one would
// exceed cfg.Service.HookMaxDepth. The depth is computed by walking the
// ParentJobID chain at firing time (no schema change). Depth cap defaults to
// config.DefaultHookMaxDepth.
func (d *Dispatcher) maybeFireHooks(ctx context.Context, job *queue.Job, signal string, payload map[string]any) {
	if strings.TrimSpace(signal) == "" {
		return
	}
	if job.EventContextID != nil {
		return // pipeline step — not a root job
	}
	pluginCfg, ok := d.cfg.Plugins[job.Plugin]
	if !ok || pluginCfg.NotifyOnComplete == nil || !*pluginCfg.NotifyOnComplete {
		return
	}
	if d.router == nil {
		return
	}

	maxDepth := d.cfg.Service.HookMaxDepth
	if maxDepth <= 0 {
		maxDepth = config.DefaultHookMaxDepth
	}
	currentDepth, err := d.hookChainDepth(ctx, job)
	if err != nil {
		d.logger.Warn("hook enqueue refused: parent lineage unavailable",
			"source_job_id", job.ID, "source_plugin", job.Plugin, "signal", signal, "error", err)
		return
	}
	if currentDepth+1 > maxDepth {
		d.logger.Warn("hook enqueue refused: max depth reached",
			"source_job_id", job.ID, "source_plugin", job.Plugin, "signal", signal,
			"current_depth", currentDepth, "max_depth", maxDepth)
		return
	}

	// maybeFireHooks short-circuits when job.EventContextID != nil, so by the
	// time we're here the upstream job has no accumulated durable context.
	// Hook entry-route predicates evaluate against the lifecycle payload only;
	// context.* in a hook if: is rejected at config load.
	dispatches, err := d.router.NextHook(ctx, job.Plugin, signal, payload)
	if err != nil {
		d.logger.Error("hook routing error", "plugin", job.Plugin, "signal", signal, "error", err)
		return
	}
	for _, disp := range dispatches {
		payloadJSON, err := json.Marshal(disp.Event)
		if err != nil {
			d.logger.Error("failed to marshal hook event", "plugin", disp.Plugin, "signal", signal, "error", err)
			continue
		}
		// P2-11: link the hook job to its source so the chain is walkable.
		// Without this the queue has a flat list of hook jobs and operators
		// cannot tell which job spawned which — and the depth cap below
		// has nothing to walk.
		sourceID := job.ID
		enqReq := queue.EnqueueRequest{
			Plugin:      disp.Plugin,
			Command:     disp.Command,
			Payload:     payloadJSON,
			SubmittedBy: "hook",
			MaxAttempts: config.MaxAttemptsForPlugin(d.cfg.Plugins[disp.Plugin]),
			ParentJobID: &sourceID,
		}
		childID, err := d.queue.Enqueue(ctx, enqReq)
		if err != nil {
			d.logger.Error("failed to enqueue hook job", "plugin", disp.Plugin, "error", err)
			continue
		}
		d.logger.Info("enqueued hook job", "signal", signal, "source_plugin", job.Plugin, "hook_plugin", disp.Plugin, "child_job_id", childID, "parent_job_id", sourceID)
	}
}

// FireRecoveryHook is the callback the scheduler uses when crash recovery marks
// a job as dead. It delegates to maybeFireHooks with the recovered job's
// metadata. This is wired via scheduler.SetRecoveryHook(disp.FireRecoveryHook).
func (d *Dispatcher) FireRecoveryHook(ctx context.Context, job *queue.Job, signal string, payload map[string]any) {
	d.maybeFireHooks(ctx, job, signal, payload)
}

// WaitForJobTree blocks until the root job and all its descendants are complete or timeout.
func (d *Dispatcher) WaitForJobTree(ctx context.Context, rootJobID string, timeout time.Duration) ([]*queue.JobResult, error) {
	// Create completion channel
	ch := make(chan struct{}, 1)

	d.mu.Lock()
	d.completions[rootJobID] = ch
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.completions, rootJobID)
		d.mu.Unlock()
	}()

	// Initial check in case it's already done.
	//
	// We require a settled tree, not just a single "no running jobs" snapshot.
	// Structural control-flow such as a skipped `if` step can make the current job
	// terminal before all downstream child rows are visible to a concurrent waiter.
	// A second confirmation read avoids returning a truncated synchronous tree.
	results, complete, err := d.settledJobTree(ctx, rootJobID)
	if err != nil {
		return nil, fmt.Errorf("initial tree check: %w", err)
	}
	if complete {
		return results, nil
	}

	// Set up timeout
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("timeout waiting for job tree completion")
		case <-ch:
			// Something in the tree finished. Recheck until the visible tree is settled.
			results, complete, err := d.settledJobTree(ctx, rootJobID)
			if err != nil {
				return nil, fmt.Errorf("tree completion check: %w", err)
			}
			if complete {
				return results, nil
			}
		}
	}
}

func (d *Dispatcher) settledJobTree(ctx context.Context, rootJobID string) ([]*queue.JobResult, bool, error) {
	results, err := d.queue.GetJobTree(ctx, rootJobID)
	if err != nil {
		return nil, false, err
	}
	if len(results) == 0 {
		return nil, false, fmt.Errorf("job %s not found", rootJobID)
	}
	if !jobTreeComplete(results) {
		return results, false, nil
	}

	// Re-read once more before declaring success.
	// This tiny confirmation step protects synchronous callers from seeing a
	// truncated tree during structural transitions (for example, a skipped root
	// step whose successors have already been enqueued but were not visible on the
	// previous snapshot).
	confirmed, err := d.queue.GetJobTree(ctx, rootJobID)
	if err != nil {
		return nil, false, err
	}
	if len(confirmed) == 0 {
		return nil, false, fmt.Errorf("job %s not found", rootJobID)
	}
	if !jobTreeComplete(confirmed) {
		return confirmed, false, nil
	}
	if len(confirmed) != len(results) {
		return confirmed, false, nil
	}
	return confirmed, true, nil
}

func jobTreeComplete(results []*queue.JobResult) bool {
	for _, res := range results {
		switch res.Status {
		case queue.StatusQueued, queue.StatusRunning:
			return false
		}
	}
	return true
}

// notifyCompletion signals any waiters that a job has finished.
func (d *Dispatcher) notifyCompletion(jobID string) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// Currently we notify all waiters. Optimization: only notify waiters interested in this tree.
	// To do that efficiently, we'd need to know the root of every jobID.
	// For MVP, notifying all sync waiters (usually few) is acceptable.
	for _, ch := range d.completions {
		select {
		case ch <- struct{}{}:
		default:
			// Channel already has a pending notification
		}
	}
}
