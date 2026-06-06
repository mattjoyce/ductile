package router

import (
	"context"
	"time"

	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/router/dsl"
)

// PipelineInfo describes a loaded pipeline's execution properties.
type PipelineInfo struct {
	Name            string
	Trigger         string
	EntryStepID     string
	TerminalStepIDs []string
	ExecutionMode   string
	Timeout         time.Duration
}

// Request captures the control-plane inputs needed to calculate fan-out in the
// Governance Hybrid model.
//
// The router works only with metadata and event envelopes. Filesystem artifact
// handling is plugin-owned; the core does not provision per-job workspaces.
//
// SourceContext is the upstream job's accumulated durable context (Sprint 17).
// Entry-route trigger predicates evaluate against payload + this context so
// authors can gate fan-out on baggage already claimed upstream. Nil means
// today's behavior — predicates see only the immediate event payload.
type Request struct {
	SourcePlugin             string
	SourceJobID              string
	SourceContextID          string
	SourcePipeline           string
	SourceStepID             string
	SourcePipelineInstanceID string
	SourceDepth              int
	SourceMaxDepth           int
	SourceEventID            string
	SourceContext            map[string]any
	Event                    protocol.Event
}

// Dispatch describes one downstream job to enqueue from a routing decision.
type Dispatch struct {
	Plugin             string
	Command            string
	Event              protocol.Event
	PipelineName       string
	StepID             string
	PipelineInstanceID string
	RouteDepth         int
	RouteMaxDepth      int
	ParentJobID        string
	ParentContextID    string
	SourceEventID      string
}

// Engine maps an emitted event to downstream dispatches.
type Engine interface {
	Next(ctx context.Context, req Request) ([]Dispatch, error)
	// NextHook resolves hook pipeline dispatches for a lifecycle signal on a plugin.
	// Hook pipelines declared with on-hook: are matched by signal name.
	// plugin may be empty when no source plugin is known; routes that declare
	// from_plugin: never match in that case.
	// Hook entry-route predicates evaluate against the lifecycle payload only;
	// there is no durable context at hook time (context.* in a hook if: is
	// rejected at config load).
	NextHook(ctx context.Context, plugin, signal string, payload map[string]any) ([]Dispatch, error)
	// GetPipelineByTrigger returns the first pipeline matched by a trigger event.
	GetPipelineByTrigger(trigger string) *PipelineInfo
	// GetPipelineByName returns a pipeline by its unique name.
	GetPipelineByName(name string) *PipelineInfo
	// GetEntryDispatches returns the initial jobs to enqueue for a named pipeline.
	GetEntryDispatches(pipelineName string, event protocol.Event) ([]Dispatch, error)
	// GetNode returns the pipeline node for the given pipeline and step IDs.
	GetNode(pipelineName string, stepID string) (dsl.Node, bool)
	// GetCompiledRoutes returns the compiled route manifest for a named pipeline.
	GetCompiledRoutes(pipelineName string) []dsl.CompiledRoute
	// PipelineSummary lists every loaded pipeline. Used by inspection surfaces
	// that need to enumerate pipelines without re-reading config.
	PipelineSummary() []PipelineInfo
}
