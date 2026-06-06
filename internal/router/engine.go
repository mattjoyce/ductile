package router

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/router/conditions"
	"github.com/mattjoyce/ductile/internal/router/dsl"
)

// Router is a concrete routing engine backed by compiled pipeline DSL.
type Router struct {
	set             *dsl.Set
	triggerIndex    map[string][]dsl.CompiledRoute // event type -> entry routes
	hookIndex       map[string][]dsl.CompiledRoute // signal -> hook entry routes
	transitionIndex map[string][]dsl.CompiledRoute // pipeline+step -> downstream routes
	logger          *slog.Logger
}

var _ Engine = (*Router)(nil)

// LoadFromConfigFiles loads pipelines from the provided config files and builds a Router.
func LoadFromConfigFiles(paths []string, registry *plugin.Registry, logger *slog.Logger) (Engine, error) {
	set, err := dsl.LoadAndCompileFiles(paths)
	if err != nil {
		return nil, err
	}
	if registry != nil {
		if err := validateUsesNodesExist(set, registry); err != nil {
			return nil, err
		}
		if err := validateFromPluginExists(set, registry); err != nil {
			return nil, err
		}
	}
	return New(set, logger), nil
}

// New creates a Router from a compiled pipeline set.
func New(set *dsl.Set, logger *slog.Logger) *Router {
	if set == nil {
		set = &dsl.Set{Pipelines: map[string]*dsl.Pipeline{}}
	}
	if logger == nil {
		logger = slog.Default()
	}

	r := &Router{
		set:             set,
		triggerIndex:    make(map[string][]dsl.CompiledRoute),
		hookIndex:       make(map[string][]dsl.CompiledRoute),
		transitionIndex: make(map[string][]dsl.CompiledRoute),
		logger:          logger.With("component", "router"),
	}

	for _, pipeline := range set.Pipelines {
		routes := pipeline.CompiledRoutes
		if len(routes) == 0 {
			routes = dsl.BuildCompiledRoutes(pipeline)
			pipeline.CompiledRoutes = append([]dsl.CompiledRoute(nil), routes...)
		}
		for _, route := range routes {
			switch {
			case route.Source.Trigger != "":
				r.triggerIndex[route.Source.Trigger] = append(r.triggerIndex[route.Source.Trigger], route)
			case route.Source.HookSignal != "":
				r.hookIndex[route.Source.HookSignal] = append(r.hookIndex[route.Source.HookSignal], route)
			case route.Source.Pipeline != "" && route.Source.StepID != "":
				key := compiledRouteKey(route.Source.Pipeline, route.Source.StepID)
				r.transitionIndex[key] = append(r.transitionIndex[key], route)
			}
		}
	}
	for trigger, routes := range r.triggerIndex {
		dsl.SortCompiledRoutes(routes)
		r.triggerIndex[trigger] = routes
	}
	for signal, routes := range r.hookIndex {
		dsl.SortCompiledRoutes(routes)
		r.hookIndex[signal] = routes
	}
	for key, routes := range r.transitionIndex {
		dsl.SortCompiledRoutes(routes)
		r.transitionIndex[key] = routes
	}

	return r
}

// PipelineCount returns the number of loaded pipelines.
func (r *Router) PipelineCount() int {
	if r.set == nil {
		return 0
	}
	return len(r.set.Pipelines)
}

// Next resolves downstream dispatches for one emitted event.
func (r *Router) Next(ctx context.Context, req Request) ([]Dispatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	eventType := strings.TrimSpace(req.Event.Type)
	if eventType == "" {
		return nil, fmt.Errorf("event.type is required")
	}

	r.logger.Debug("resolving routes for event", "type", eventType, "source_plugin", req.SourcePlugin)

	var out []Dispatch

	// Intra-pipeline transitions: if current step/pipeline are known, match downstream routes.
	if req.SourcePipeline != "" && req.SourceStepID != "" {
		routes := r.transitionIndex[compiledRouteKey(req.SourcePipeline, req.SourceStepID)]
		if len(routes) > 0 {
			r.logger.Debug("matched intra-pipeline transition",
				"pipeline", req.SourcePipeline,
				"from_step", req.SourceStepID,
				"routes", len(routes))

			for _, route := range routes {
				if !matchesCompiledRouteSource(route.Source, req, compiledRouteMatchRequireInstance) {
					r.logger.Debug("skipping transition route without matching pipeline instance",
						"route_id", route.ID,
						"pipeline", req.SourcePipeline,
						"from_step", req.SourceStepID,
					)
					continue
				}
				dispatches, err := r.resolveCompiledRoute(route, req, false)
				if err != nil {
					return nil, err
				}
				out = append(out, dispatches...)
			}
		}
	}

	// Root triggers: event type can start one or more pipelines.
	if routes, ok := r.triggerIndex[eventType]; ok {
		pipelineInstanceIDs := make(map[string]string, len(routes))
		r.logger.Debug("matched root trigger pipelines", "event_type", eventType, "count", len(routes))
		for _, route := range routes {
			if !sourcePluginMatches(route.Source.SourcePlugin, req.SourcePlugin) {
				r.logger.Debug("trigger from_plugin skipped pipeline",
					"pipeline", route.Pipeline,
					"event_type", eventType,
					"want_source_plugin", route.Source.SourcePlugin,
					"got_source_plugin", req.SourcePlugin)
				continue
			}
			if route.Source.If != nil {
				ok, err := conditions.Eval(route.Source.If, conditions.Scope{Payload: req.Event.Payload, Context: req.SourceContext})
				if err != nil {
					// Fault isolation: a poison predicate on one route must not
					// abort routing for every other pipeline that matched this
					// event. Fail safe — skip this route without matching — and
					// make it loud rather than silent.
					r.logger.Warn("trigger predicate errored; skipping route (fail-safe no match)",
						"route_id", route.ID,
						"pipeline", route.Pipeline,
						"event_type", eventType,
						"error", err)
					continue
				}
				if !ok {
					r.logger.Debug("trigger predicate skipped pipeline",
						"pipeline", route.Pipeline,
						"event_type", eventType)
					continue
				}
			}
			pipelineInstanceID := pipelineInstanceIDs[route.Pipeline]
			if pipelineInstanceID == "" {
				pipelineInstanceID = uuid.NewString()
				pipelineInstanceIDs[route.Pipeline] = pipelineInstanceID
			}
			r.logger.Info("triggering pipeline", "name", route.Pipeline, "event_type", eventType)
			rootReq := req
			rootReq.SourcePipelineInstanceID = pipelineInstanceID
			dispatches, err := r.resolveCompiledRoute(route, rootReq, false)
			if err != nil {
				return nil, err
			}
			out = append(out, dispatches...)
		}
	}

	if len(out) > 0 {
		r.logger.Info("routing event resulting in child jobs", "type", eventType, "jobs", len(out))
	}

	return dedupeDispatches(out), nil
}

// NextHook resolves hook pipeline dispatches for a lifecycle signal on a plugin.
// Dispatches are root-level (no pipeline/step context) so hook jobs run independently.
//
// sourceContext is the upstream job's accumulated durable context, when available.
// Entry-route predicates evaluate against payload + sourceContext so authors can
// gate hook fan-out on baggage already claimed upstream.
func (r *Router) NextHook(ctx context.Context, plugin, signal string, payload map[string]any, sourceContext map[string]any) ([]Dispatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	signal = strings.TrimSpace(signal)
	if signal == "" {
		return nil, fmt.Errorf("signal is required")
	}

	routes, ok := r.hookIndex[signal]
	if !ok || len(routes) == 0 {
		return nil, nil
	}

	r.logger.Debug("resolving hook pipelines", "signal", signal, "plugin", plugin, "count", len(routes))

	ev := protocol.Event{
		Type:    signal,
		Payload: payload,
	}

	var out []Dispatch
	for _, route := range routes {
		if !sourcePluginMatches(route.Source.SourcePlugin, plugin) {
			r.logger.Debug("hook from_plugin skipped pipeline",
				"pipeline", route.Pipeline,
				"signal", signal,
				"want_source_plugin", route.Source.SourcePlugin,
				"got_source_plugin", plugin)
			continue
		}
		if route.Source.If != nil {
			ok, err := conditions.Eval(route.Source.If, conditions.Scope{Payload: payload, Context: sourceContext})
			if err != nil {
				// Fault isolation: one hook route's poison predicate must not
				// suppress the other hook pipelines bound to this signal. Fail
				// safe (skip, no match) and loud.
				r.logger.Warn("hook trigger predicate errored; skipping route (fail-safe no match)",
					"route_id", route.ID,
					"pipeline", route.Pipeline,
					"signal", signal,
					"source_plugin", plugin,
					"error", err)
				continue
			}
			if !ok {
				r.logger.Debug("hook trigger predicate skipped pipeline",
					"pipeline", route.Pipeline,
					"signal", signal,
					"source_plugin", plugin)
				continue
			}
		}
		r.logger.Info("triggering hook pipeline", "name", route.Pipeline, "signal", signal, "source_plugin", plugin)
		dispatches, err := r.resolveCompiledRoute(route, Request{Event: ev, SourcePlugin: plugin, SourceContext: sourceContext}, true)
		if err != nil {
			return nil, err
		}
		out = append(out, dispatches...)
	}

	if len(out) > 0 {
		r.logger.Info("hook signal resulting in jobs", "signal", signal, "jobs", len(out))
	}

	return dedupeDispatches(out), nil
}

// sourcePluginMatches returns true when the route either has no from_plugin
// selector or the selector matches the request's source plugin.
//
// An empty have when the selector is set never matches — the route's selector
// is a positive assertion. Trim spaces on both sides so YAML quoting noise does
// not silently mismatch.
func sourcePluginMatches(want, have string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	return want == strings.TrimSpace(have)
}

func (r *Router) resolveCompiledRoute(route dsl.CompiledRoute, req Request, rootHook bool) ([]Dispatch, error) {
	switch route.Destination.Kind {
	case dsl.CompiledRouteDestinationUses:
		routeMaxDepth := req.SourceMaxDepth
		if routeMaxDepth == 0 {
			routeMaxDepth = route.Source.DepthLT
		}
		dispatch := Dispatch{
			Plugin:             route.Destination.Plugin,
			Command:            route.Destination.Command,
			Event:              cloneEvent(req.Event),
			PipelineInstanceID: req.SourcePipelineInstanceID,
			RouteDepth:         req.SourceDepth + 1,
			RouteMaxDepth:      routeMaxDepth,
			ParentJobID:        req.SourceJobID,
			ParentContextID:    req.SourceContextID,
			SourceEventID:      req.SourceEventID,
		}
		if !rootHook {
			dispatch.PipelineName = route.Pipeline
			dispatch.StepID = route.Destination.StepID
		}
		return []Dispatch{dispatch}, nil

	case dsl.CompiledRouteDestinationCall:
		called := strings.TrimSpace(route.Destination.CallPipeline)
		if called == "" {
			return nil, fmt.Errorf("compiled route %q has empty call target", route.ID)
		}
		entryRoutes := r.entryRoutesForPipeline(called)
		if len(entryRoutes) == 0 {
			return nil, fmt.Errorf("compiled route %q references pipeline %q with no entry routes", route.ID, called)
		}
		var out []Dispatch
		for _, entryRoute := range entryRoutes {
			dispatches, err := r.resolveCompiledRoute(entryRoute, req, rootHook)
			if err != nil {
				return nil, err
			}
			out = append(out, dispatches...)
		}
		return out, nil

	case dsl.CompiledRouteDestinationTerminal:
		return nil, nil
	}

	return nil, fmt.Errorf("unsupported compiled route destination kind %q", route.Destination.Kind)
}

func cloneEvent(ev protocol.Event) protocol.Event {
	cloned := protocol.Event{
		Type:      ev.Type,
		DedupeKey: ev.DedupeKey,
		Source:    ev.Source,
		Timestamp: ev.Timestamp,
		EventID:   ev.EventID,
	}
	if ev.Payload != nil {
		cloned.Payload = make(map[string]any, len(ev.Payload))
		maps.Copy(cloned.Payload, ev.Payload)
	}
	return cloned
}

func dedupeDispatches(in []Dispatch) []Dispatch {
	seen := make(map[string]struct{}, len(in))
	out := make([]Dispatch, 0, len(in))
	for _, d := range in {
		key := d.PipelineName + "\x00" + d.StepID + "\x00" + d.PipelineInstanceID + "\x00" + d.Plugin + "\x00" + d.Command + "\x00" + d.SourceEventID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, d)
	}
	return out
}

func compiledRouteKey(pipelineName, stepID string) string {
	return pipelineName + "\x00" + stepID
}

type compiledRouteMatchMode int

const (
	compiledRouteMatchDefault compiledRouteMatchMode = iota
	compiledRouteMatchRequireInstance
)

func matchesCompiledRouteSource(source dsl.CompiledRouteSource, req Request, mode compiledRouteMatchMode) bool {
	switch {
	case source.Trigger != "":
		return strings.TrimSpace(source.Trigger) == strings.TrimSpace(req.Event.Type)

	case source.HookSignal != "":
		return strings.TrimSpace(source.HookSignal) == strings.TrimSpace(req.Event.Type)

	case source.Pipeline != "" && source.StepID != "":
		if strings.TrimSpace(source.Pipeline) != strings.TrimSpace(req.SourcePipeline) {
			return false
		}
		if strings.TrimSpace(source.StepID) != strings.TrimSpace(req.SourceStepID) {
			return false
		}
		if source.EventType != "" && strings.TrimSpace(source.EventType) != strings.TrimSpace(req.Event.Type) {
			return false
		}
		if mode == compiledRouteMatchRequireInstance && strings.TrimSpace(req.SourcePipelineInstanceID) == "" {
			return false
		}
		maxDepth := req.SourceMaxDepth
		if maxDepth == 0 {
			maxDepth = source.DepthLT
		}
		if maxDepth > 0 && req.SourceDepth >= maxDepth {
			return false
		}
		return true
	}

	return false
}

// GetPipelineByTrigger returns the first pipeline matched by a trigger event.
func (r *Router) GetPipelineByTrigger(trigger string) *PipelineInfo {
	r.logger.Debug("looking up pipeline by trigger", "trigger", trigger)
	pipelines, ok := r.triggerIndex[trigger]
	if !ok || len(pipelines) == 0 {
		return nil
	}

	// For now, return the first one found.
	pipeline := r.set.Pipelines[pipelines[0].Pipeline]
	if pipeline == nil {
		return nil
	}

	return &PipelineInfo{
		Name:            pipeline.Name,
		Trigger:         pipeline.Trigger,
		EntryStepID:     firstEntryStepID(pipeline.EntryNodeIDs),
		TerminalStepIDs: append([]string(nil), pipeline.TerminalNodeIDs...),
		ExecutionMode:   pipeline.ExecutionMode,
		Timeout:         pipeline.Timeout,
	}
}

// GetPipelineByName returns info about a pipeline by its name.
func (r *Router) GetPipelineByName(name string) *PipelineInfo {
	r.logger.Debug("looking up pipeline by name", "name", name)
	pipeline, ok := r.set.Pipelines[name]
	if !ok {
		return nil
	}

	return &PipelineInfo{
		Name:            pipeline.Name,
		Trigger:         pipeline.Trigger,
		EntryStepID:     firstEntryStepID(pipeline.EntryNodeIDs),
		TerminalStepIDs: append([]string(nil), pipeline.TerminalNodeIDs...),
		ExecutionMode:   pipeline.ExecutionMode,
		Timeout:         pipeline.Timeout,
	}
}

// GetEntryDispatches returns the initial jobs to enqueue when a pipeline is explicitly triggered.
func (r *Router) GetEntryDispatches(pipelineName string, event protocol.Event) ([]Dispatch, error) {
	pipeline, ok := r.set.Pipelines[pipelineName]
	if !ok {
		return nil, fmt.Errorf("pipeline %q not found", pipelineName)
	}

	var out []Dispatch
	req := Request{
		Event: event,
	}

	for _, route := range r.entryRoutesForPipeline(pipeline.Name) {
		dispatches, err := r.resolveCompiledRoute(route, req, false)
		if err != nil {
			return nil, err
		}
		out = append(out, dispatches...)
	}

	return out, nil
}

// PipelineSummary returns info about all loaded pipelines.
func (r *Router) PipelineSummary() []PipelineInfo {
	var out []PipelineInfo
	for name, pipeline := range r.set.Pipelines {
		out = append(out, PipelineInfo{
			Name:            name,
			Trigger:         pipeline.Trigger,
			EntryStepID:     firstEntryStepID(pipeline.EntryNodeIDs),
			TerminalStepIDs: append([]string(nil), pipeline.TerminalNodeIDs...),
			ExecutionMode:   pipeline.ExecutionMode,
			Timeout:         pipeline.Timeout,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func firstEntryStepID(entryNodeIDs []string) string {
	if len(entryNodeIDs) == 0 {
		return ""
	}
	return entryNodeIDs[0]
}

// GetNode returns the compiled node for the given pipeline and step IDs.
func (r *Router) GetNode(pipelineName string, stepID string) (dsl.Node, bool) {
	if strings.TrimSpace(pipelineName) == "" || strings.TrimSpace(stepID) == "" {
		return dsl.Node{}, false
	}
	pipeline, ok := r.set.Pipelines[pipelineName]
	if !ok {
		return dsl.Node{}, false
	}
	node, ok := pipeline.Nodes[stepID]
	return node, ok
}

// GetCompiledRoutes returns a copy of the compiled route manifest for a pipeline.
func (r *Router) GetCompiledRoutes(pipelineName string) []dsl.CompiledRoute {
	if strings.TrimSpace(pipelineName) == "" {
		return nil
	}
	pipeline, ok := r.set.Pipelines[pipelineName]
	if !ok || len(pipeline.CompiledRoutes) == 0 {
		return nil
	}
	return append([]dsl.CompiledRoute(nil), pipeline.CompiledRoutes...)
}

func (r *Router) entryRoutesForPipeline(pipelineName string) []dsl.CompiledRoute {
	pipeline, ok := r.set.Pipelines[pipelineName]
	if !ok || len(pipeline.CompiledRoutes) == 0 {
		return nil
	}
	out := make([]dsl.CompiledRoute, 0)
	for _, route := range pipeline.CompiledRoutes {
		if route.Pipeline != pipelineName {
			continue
		}
		if route.Source.Trigger == "" && route.Source.HookSignal == "" {
			continue
		}
		out = append(out, route)
	}
	return out
}

// validateFromPluginExists rejects any pipeline whose from_plugin: selector names
// a plugin not in the registry. Without this an upstream-scoping typo compiles
// clean and then silently matches nothing for every event — the dead-route trap
// validateUsesNodesExist already closes for uses: steps.
func validateFromPluginExists(set *dsl.Set, registry *plugin.Registry) error {
	for pipelineName, pipeline := range set.Pipelines {
		if pipeline.FromPlugin == "" {
			continue
		}
		if _, ok := registry.Get(pipeline.FromPlugin); !ok {
			return fmt.Errorf("pipeline %q: from_plugin references unknown plugin %q", pipelineName, pipeline.FromPlugin)
		}
	}
	return nil
}

func validateUsesNodesExist(set *dsl.Set, registry *plugin.Registry) error {
	for pipelineName, pipeline := range set.Pipelines {
		for _, node := range pipeline.Nodes {
			if node.Kind != dsl.NodeKindUses {
				continue
			}
			if _, ok := registry.Get(node.Uses); !ok {
				return fmt.Errorf("pipeline %q step %q references unknown plugin %q", pipelineName, node.ID, node.Uses)
			}
		}
	}
	return nil
}
