package dsl

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/router/conditions"
	"github.com/zeebo/blake3"
)

// CompileSpecs compiles pipeline DSL definitions into validated DAGs.
func CompileSpecs(specs []PipelineSpec) (*Set, error) {
	out := &Set{Pipelines: make(map[string]*Pipeline)}

	for i, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return nil, fmt.Errorf("pipelines[%d]: name is required", i)
		}
		if _, exists := out.Pipelines[name]; exists {
			return nil, fmt.Errorf("duplicate pipeline name %q", name)
		}

		compiled, err := compilePipeline(spec)
		if err != nil {
			return nil, fmt.Errorf("pipeline %q: %w", name, err)
		}
		out.Pipelines[name] = compiled
	}

	if err := validatePipelineCalls(out.Pipelines); err != nil {
		return nil, err
	}
	if err := assignPipelineMaxRouteDepths(out.Pipelines); err != nil {
		return nil, err
	}
	for _, pipeline := range out.Pipelines {
		if pipeline.AuthorMaxDepth != nil {
			pipeline.MaxRouteDepth = *pipeline.AuthorMaxDepth
		}
	}
	for name, pipeline := range out.Pipelines {
		pipeline.CompiledRoutes = BuildCompiledRoutes(pipeline)
		fingerprint, err := fingerprintPipeline(pipeline)
		if err != nil {
			return nil, fmt.Errorf("pipeline %q: %w", name, err)
		}
		pipeline.Fingerprint = fingerprint
	}

	return out, nil
}

func compilePipeline(spec PipelineSpec) (*Pipeline, error) {
	name := strings.TrimSpace(spec.Name)
	trigger := strings.TrimSpace(spec.On)
	hookSignal := strings.TrimSpace(spec.OnHook)

	if trigger == "" && hookSignal == "" {
		return nil, fmt.Errorf("on or on-hook is required")
	}
	if trigger != "" && hookSignal != "" {
		return nil, fmt.Errorf("on and on-hook are mutually exclusive")
	}
	if len(spec.Steps) == 0 {
		return nil, fmt.Errorf("steps must be non-empty")
	}

	isHook := hookSignal != ""
	effectiveTrigger := trigger
	if isHook {
		effectiveTrigger = hookSignal
	}

	fromPlugin := strings.TrimSpace(spec.FromPlugin)

	pipeline := &Pipeline{
		Name:          name,
		Trigger:       effectiveTrigger,
		IsHook:        isHook,
		FromPlugin:    fromPlugin,
		ExecutionMode: spec.ExecutionMode,
		Timeout:       spec.Timeout,
		Nodes:         make(map[string]Node),
	}
	if spec.If != nil {
		if err := conditions.Validate(spec.If); err != nil {
			return nil, fmt.Errorf("if: %w", err)
		}
		clone := *spec.If
		pipeline.If = &clone
		// Hooks fire for jobs that have no accumulated durable context (the
		// dispatcher short-circuits hooks for context-bearing jobs), so a
		// context.* predicate on an on-hook: trigger can never match. Reject it
		// at load rather than letting it silently dead-route at runtime.
		if isHook && conditions.ReferencesRoot(pipeline.If, "context") {
			return nil, fmt.Errorf("on-hook trigger predicate cannot reference context.* — context is not available at hook time")
		}
	}
	if spec.MaxDepth != nil {
		if *spec.MaxDepth < 0 {
			return nil, fmt.Errorf("max_depth must be >= 0 (0 means unlimited)")
		}
		v := *spec.MaxDepth
		pipeline.AuthorMaxDepth = &v
	}
	if pipeline.ExecutionMode == "" {
		pipeline.ExecutionMode = "async"
	}
	if pipeline.Timeout == 0 {
		pipeline.Timeout = 30 * time.Second
	}
	builder := compileBuilder{
		pipeline: pipeline,
		called:   make(map[string]struct{}),
	}

	entry, terminal, err := builder.compileSteps(spec.Steps)
	if err != nil {
		return nil, err
	}
	pipeline.EntryNodeIDs = sortedUnique(entry)
	pipeline.TerminalExits = sortedTerminalExits(terminal)
	pipeline.TerminalNodeIDs = terminalNodeIDs(terminal)
	pipeline.CalledPipelines = sortedMapKeys(builder.called)
	sortEdges(pipeline.Edges)

	if err := validatePipelineDAG(pipeline); err != nil {
		return nil, err
	}
	return pipeline, nil
}

type compileBuilder struct {
	pipeline *Pipeline
	nextAuto int
	called   map[string]struct{}
}

type compileExit struct {
	NodeID    string
	EventType string
}

func (b *compileBuilder) compileSteps(steps []StepSpec) (entry []string, terminal []compileExit, err error) {
	if len(steps) == 0 {
		return nil, nil, fmt.Errorf("steps must be non-empty")
	}

	var previous []compileExit
	for i, step := range steps {
		stepEntry, stepTerminal, err := b.compileStep(step)
		if err != nil {
			return nil, nil, fmt.Errorf("steps[%d]: %w", i, err)
		}
		if len(stepEntry) == 0 || len(stepTerminal) == 0 {
			return nil, nil, fmt.Errorf("steps[%d]: compiled to empty node set", i)
		}

		if i == 0 {
			entry = append(entry, stepEntry...)
		}
		if len(previous) > 0 {
			b.addEdges(previous, stepEntry)
		}
		previous = stepTerminal
	}

	return entry, previous, nil
}

func (b *compileBuilder) compileStep(step StepSpec) (entry []string, terminal []compileExit, err error) {
	modeCount := 0
	if strings.TrimSpace(step.Uses) != "" {
		modeCount++
	}
	if strings.TrimSpace(step.Call) != "" {
		modeCount++
	}
	if step.Relay != nil {
		modeCount++
	}
	if len(step.Steps) > 0 {
		modeCount++
	}
	if len(step.Split) > 0 {
		modeCount++
	}
	if modeCount != 1 {
		return nil, nil, fmt.Errorf("step must define exactly one of uses, call, relay, steps, or split")
	}
	if len(step.With) > 0 && strings.TrimSpace(step.Uses) == "" {
		return nil, nil, fmt.Errorf("with is only supported on uses steps")
	}
	if step.Baggage != nil && !step.Baggage.Empty() {
		if strings.TrimSpace(step.Uses) == "" {
			return nil, nil, fmt.Errorf("baggage is only supported on uses steps")
		}
		if err := validateBaggageSpec(step.Baggage); err != nil {
			return nil, nil, err
		}
	}

	var cond *conditions.Condition
	if step.If != nil {
		if err := conditions.Validate(step.If); err != nil {
			return nil, nil, err
		}
		clone := *step.If
		cond = &clone
	}

	switch {
	case strings.TrimSpace(step.Uses) != "":
		id, err := b.allocNodeID(step.ID)
		if err != nil {
			return nil, nil, err
		}
		node := Node{
			ID:      id,
			Kind:    NodeKindUses,
			Uses:    strings.TrimSpace(step.Uses),
			With:    step.With,
			Baggage: step.Baggage.clone(),
		}
		b.pipeline.Nodes[id] = node
		entry = []string{id}
		terminal = []compileExit{{NodeID: id}}
		if cond != nil {
			return b.wrapConditionalStep(id, cond, entry, terminal)
		}
		return entry, terminal, nil

	case strings.TrimSpace(step.Call) != "":
		id, err := b.allocNodeID(step.ID)
		if err != nil {
			return nil, nil, err
		}
		callTarget := strings.TrimSpace(step.Call)
		node := Node{
			ID:   id,
			Kind: NodeKindCall,
			Call: callTarget,
		}
		b.pipeline.Nodes[id] = node
		b.called[callTarget] = struct{}{}
		entry = []string{id}
		terminal = []compileExit{{NodeID: id}}
		if cond != nil {
			return b.wrapConditionalStep(id, cond, entry, terminal)
		}
		return entry, terminal, nil

	case step.Relay != nil:
		if err := validateRelaySpec(step.Relay); err != nil {
			return nil, nil, err
		}
		id, err := b.allocNodeID(step.ID)
		if err != nil {
			return nil, nil, err
		}
		node := Node{
			ID:    id,
			Kind:  NodeKindRelay,
			Relay: step.Relay.clone(),
		}
		b.pipeline.Nodes[id] = node
		entry = []string{id}
		terminal = []compileExit{{NodeID: id}}
		if cond != nil {
			return b.wrapConditionalStep(id, cond, entry, terminal)
		}
		return entry, terminal, nil

	case len(step.Steps) > 0:
		return b.compileSteps(step.Steps)

	case len(step.Split) > 0:
		var allEntry []string
		var allTerminal []compileExit
		for i, branch := range step.Split {
			branchEntry, branchTerminal, err := b.compileStep(branch)
			if err != nil {
				return nil, nil, fmt.Errorf("split[%d]: %w", i, err)
			}
			allEntry = append(allEntry, branchEntry...)
			allTerminal = append(allTerminal, branchTerminal...)
		}
		return sortedUnique(allEntry), sortedCompileExits(allTerminal), nil
	}

	return nil, nil, fmt.Errorf("unreachable step state")
}

func validateRelaySpec(spec *RelaySpec) error {
	if spec == nil {
		return fmt.Errorf("relay is required")
	}
	if strings.TrimSpace(spec.To) == "" {
		return fmt.Errorf("relay.to is required")
	}
	if strings.TrimSpace(spec.Event) == "" {
		return fmt.Errorf("relay.event is required")
	}
	if spec.Baggage != nil && !spec.Baggage.Empty() {
		if err := validateBaggageSpec(spec.Baggage); err != nil {
			return fmt.Errorf("relay.baggage: %w", err)
		}
	}
	for key, expr := range spec.With {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("relay.with keys must be non-empty")
		}
		if strings.TrimSpace(expr) == "" {
			return fmt.Errorf("relay.with.%s expression must be non-empty", key)
		}
	}
	return nil
}

func (b *compileBuilder) wrapConditionalStep(stepID string, cond *conditions.Condition, entry []string, terminal []compileExit) ([]string, []compileExit, error) {
	switchID, err := b.allocNodeID(stepID + "__switch")
	if err != nil {
		return nil, nil, err
	}
	clone := *cond
	b.pipeline.Nodes[switchID] = Node{
		ID:        switchID,
		Kind:      NodeKindSwitch,
		Uses:      "core.switch",
		Condition: &clone,
	}
	for _, destination := range entry {
		b.pipeline.Edges = append(b.pipeline.Edges, Edge{
			From:      switchID,
			To:        destination,
			EventType: "ductile.switch.true",
		})
	}
	terminal = append(terminal, compileExit{
		NodeID:    switchID,
		EventType: "ductile.switch.false",
	})
	return []string{switchID}, sortedCompileExits(terminal), nil
}

func validateBaggageSpec(spec *BaggageSpec) error {
	if spec == nil || spec.Empty() {
		return nil
	}
	for path, expr := range spec.Mappings {
		if err := validateBaggagePath(path); err != nil {
			return fmt.Errorf("invalid baggage path %q: %w", path, err)
		}
		if strings.TrimSpace(expr) == "" {
			return fmt.Errorf("baggage.%s expression must be non-empty", path)
		}
	}
	if spec.Bulk == nil {
		return nil
	}
	if strings.TrimSpace(spec.Bulk.From) == "" {
		return fmt.Errorf("baggage from must be non-empty")
	}
	if !isPayloadPath(spec.Bulk.From) {
		return fmt.Errorf("baggage from %q must reference payload or payload.<path>", spec.Bulk.From)
	}
	if strings.TrimSpace(spec.Bulk.Namespace) != "" {
		if err := validateBaggagePath(spec.Bulk.Namespace); err != nil {
			return fmt.Errorf("invalid baggage namespace %q: %w", spec.Bulk.Namespace, err)
		}
	}
	return nil
}

func validateBaggagePath(path string) error {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return fmt.Errorf("path must be non-empty")
	}
	for _, part := range strings.Split(trimmed, ".") {
		if part == "" {
			return fmt.Errorf("path segments must be non-empty")
		}
		for i, r := range part {
			switch {
			case r == '_':
			case r >= 'a' && r <= 'z':
			case r >= 'A' && r <= 'Z':
			case i > 0 && r >= '0' && r <= '9':
			default:
				return fmt.Errorf("path segment %q must use letters, digits, or underscore and must not start with a digit", part)
			}
		}
	}
	return nil
}

func isPayloadPath(expr string) bool {
	trimmed := strings.TrimSpace(expr)
	return trimmed == "payload" || strings.HasPrefix(trimmed, "payload.")
}

func (b *compileBuilder) allocNodeID(preferred string) (string, error) {
	id := strings.TrimSpace(preferred)
	if id == "" {
		for {
			b.nextAuto++
			candidate := fmt.Sprintf("step_%d", b.nextAuto)
			if _, exists := b.pipeline.Nodes[candidate]; !exists {
				id = candidate
				break
			}
		}
	}
	if _, exists := b.pipeline.Nodes[id]; exists {
		return "", fmt.Errorf("duplicate step id %q", id)
	}
	return id, nil
}

func (b *compileBuilder) addEdges(fromNodes []compileExit, toNodes []string) {
	for _, from := range fromNodes {
		for _, to := range toNodes {
			b.pipeline.Edges = append(b.pipeline.Edges, Edge{
				From:      from.NodeID,
				To:        to,
				EventType: from.EventType,
			})
		}
	}
}

// BuildCompiledRoutes derives the deterministic route manifest for a compiled
// pipeline shape.
func BuildCompiledRoutes(p *Pipeline) []CompiledRoute {
	if p == nil {
		return nil
	}

	routes := make([]CompiledRoute, 0, len(p.EntryNodeIDs)+len(p.Edges)+len(p.TerminalNodeIDs))
	for _, entryID := range p.EntryNodeIDs {
		node, ok := p.Nodes[entryID]
		if !ok {
			continue
		}
		source := CompiledRouteSource{}
		if p.IsHook {
			source.HookSignal = p.Trigger
		} else {
			source.Trigger = p.Trigger
		}
		if p.FromPlugin != "" {
			source.SourcePlugin = p.FromPlugin
		}
		if p.If != nil {
			clone := *p.If
			source.If = &clone
		}
		routes = append(routes, CompiledRoute{
			ID:          "entry:" + entryID,
			Pipeline:    p.Name,
			Source:      withRouteDepth(source, p.MaxRouteDepth),
			Destination: routeDestinationForNode(node),
		})
	}

	for _, edge := range p.Edges {
		node, ok := p.Nodes[edge.To]
		if !ok {
			continue
		}
		routes = append(routes, CompiledRoute{
			ID:       "edge:" + edge.From + "->" + edge.To,
			Pipeline: p.Name,
			Source: withRouteDepth(CompiledRouteSource{
				Pipeline:  p.Name,
				StepID:    edge.From,
				EventType: edge.EventType,
			}, p.MaxRouteDepth),
			Destination: routeDestinationForNode(node),
		})
	}

	terminalExits := p.TerminalExits
	if len(terminalExits) == 0 {
		for _, terminalID := range p.TerminalNodeIDs {
			terminalExits = append(terminalExits, TerminalExit{StepID: terminalID})
		}
	}
	for _, terminal := range terminalExits {
		routes = append(routes, CompiledRoute{
			ID:       terminalRouteID(terminal),
			Pipeline: p.Name,
			Source: withRouteDepth(CompiledRouteSource{
				Pipeline:  p.Name,
				StepID:    terminal.StepID,
				EventType: terminal.EventType,
			}, p.MaxRouteDepth),
			Destination: CompiledRouteDestination{
				Kind: CompiledRouteDestinationTerminal,
			},
		})
	}

	SortCompiledRoutes(routes)
	return routes
}

func routeDestinationForNode(node Node) CompiledRouteDestination {
	switch node.Kind {
	case NodeKindCall:
		return CompiledRouteDestination{
			Kind:         CompiledRouteDestinationCall,
			StepID:       node.ID,
			CallPipeline: node.Call,
		}
	case NodeKindSwitch:
		return CompiledRouteDestination{
			Kind:    CompiledRouteDestinationUses,
			StepID:  node.ID,
			Plugin:  "core.switch",
			Command: "handle",
		}
	case NodeKindRelay:
		return CompiledRouteDestination{
			Kind:    CompiledRouteDestinationUses,
			StepID:  node.ID,
			Plugin:  "core.relay",
			Command: "handle",
		}
	default:
		return CompiledRouteDestination{
			Kind:    CompiledRouteDestinationUses,
			StepID:  node.ID,
			Plugin:  node.Uses,
			Command: "handle",
		}
	}
}

func withRouteDepth(source CompiledRouteSource, depth int) CompiledRouteSource {
	if depth > 0 {
		source.DepthLT = depth
	}
	return source
}

func terminalRouteID(exit TerminalExit) string {
	if strings.TrimSpace(exit.EventType) == "" {
		return "terminal:" + exit.StepID
	}
	return "terminal:" + exit.StepID + "@" + exit.EventType
}

func validatePipelineCalls(pipelines map[string]*Pipeline) error {
	for name, pipeline := range pipelines {
		for _, called := range pipeline.CalledPipelines {
			if _, ok := pipelines[called]; !ok {
				return fmt.Errorf("pipeline %q references unknown pipeline %q", name, called)
			}
		}
	}

	state := make(map[string]int)
	var walk func(name string, stack []string) error
	walk = func(name string, stack []string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			idx := 0
			for i := range stack {
				if stack[i] == name {
					idx = i
					break
				}
			}
			cycle := append(append([]string{}, stack[idx:]...), name)
			return fmt.Errorf("pipeline call cycle detected: %s", strings.Join(cycle, " -> "))
		}

		state[name] = 1
		stack = append(stack, name)
		for _, dep := range pipelines[name].CalledPipelines {
			if err := walk(dep, stack); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}

	names := make([]string, 0, len(pipelines))
	for name := range pipelines {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err := walk(name, nil); err != nil {
			return err
		}
	}
	return nil
}

func validatePipelineDAG(p *Pipeline) error {
	inDegree := make(map[string]int, len(p.Nodes))
	adj := make(map[string][]string, len(p.Nodes))

	for id := range p.Nodes {
		inDegree[id] = 0
	}
	for _, edge := range p.Edges {
		if _, ok := p.Nodes[edge.From]; !ok {
			return fmt.Errorf("edge references unknown from node %q", edge.From)
		}
		if _, ok := p.Nodes[edge.To]; !ok {
			return fmt.Errorf("edge references unknown to node %q", edge.To)
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
		inDegree[edge.To]++
	}

	queue := make([]string, 0, len(p.Nodes))
	for id, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}

	visited := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		visited++

		for _, next := range adj[n] {
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}

	if visited != len(p.Nodes) {
		return fmt.Errorf("compiled pipeline graph contains a cycle")
	}
	return nil
}

func assignPipelineMaxRouteDepths(pipelines map[string]*Pipeline) error {
	memo := make(map[string]int, len(pipelines))
	visiting := make(map[string]bool, len(pipelines))

	var visit func(string) (int, error)
	visit = func(name string) (int, error) {
		if depth, ok := memo[name]; ok {
			return depth, nil
		}
		if visiting[name] {
			return 0, fmt.Errorf("pipeline max depth cycle detected at %q", name)
		}
		pipeline, ok := pipelines[name]
		if !ok {
			return 0, fmt.Errorf("pipeline %q not found", name)
		}

		visiting[name] = true
		adj := make(map[string][]string, len(pipeline.Nodes))
		for _, edge := range pipeline.Edges {
			adj[edge.From] = append(adj[edge.From], edge.To)
		}
		nodeMemo := make(map[string]int, len(pipeline.Nodes))
		var nodeDepth func(string) (int, error)
		nodeDepth = func(nodeID string) (int, error) {
			if depth, ok := nodeMemo[nodeID]; ok {
				return depth, nil
			}
			node, ok := pipeline.Nodes[nodeID]
			if !ok {
				return 0, fmt.Errorf("pipeline %q missing node %q", name, nodeID)
			}

			own := 1
			if node.Kind == NodeKindCall {
				calledDepth, err := visit(node.Call)
				if err != nil {
					return 0, err
				}
				own = calledDepth
			}

			maxSucc := 0
			for _, nextID := range adj[nodeID] {
				depth, err := nodeDepth(nextID)
				if err != nil {
					return 0, err
				}
				if depth > maxSucc {
					maxSucc = depth
				}
			}
			total := own + maxSucc
			nodeMemo[nodeID] = total
			return total, nil
		}

		maxDepth := 0
		for _, entryID := range pipeline.EntryNodeIDs {
			depth, err := nodeDepth(entryID)
			if err != nil {
				return 0, err
			}
			if depth > maxDepth {
				maxDepth = depth
			}
		}
		if maxDepth == 0 {
			maxDepth = 1
		}
		pipeline.MaxRouteDepth = maxDepth
		memo[name] = maxDepth
		delete(visiting, name)
		return maxDepth, nil
	}

	names := make([]string, 0, len(pipelines))
	for name := range pipelines {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

func fingerprintPipeline(p *Pipeline) (string, error) {
	type fingerprintShape struct {
		Name            string          `json:"name"`
		Trigger         string          `json:"trigger"`
		Nodes           []Node          `json:"nodes"`
		Edges           []Edge          `json:"edges"`
		EntryNodeIDs    []string        `json:"entry_node_ids"`
		TerminalNodeIDs []string        `json:"terminal_node_ids"`
		TerminalExits   []TerminalExit  `json:"terminal_exits"`
		CalledPipelines []string        `json:"called_pipelines"`
		MaxRouteDepth   int             `json:"max_route_depth"`
		CompiledRoutes  []CompiledRoute `json:"compiled_routes"`
	}

	nodes := make([]Node, 0, len(p.Nodes))
	for _, node := range p.Nodes {
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	shape := fingerprintShape{
		Name:            p.Name,
		Trigger:         p.Trigger,
		Nodes:           nodes,
		Edges:           append([]Edge(nil), p.Edges...),
		EntryNodeIDs:    append([]string(nil), p.EntryNodeIDs...),
		TerminalNodeIDs: append([]string(nil), p.TerminalNodeIDs...),
		TerminalExits:   append([]TerminalExit(nil), p.TerminalExits...),
		CalledPipelines: append([]string(nil), p.CalledPipelines...),
		MaxRouteDepth:   p.MaxRouteDepth,
		CompiledRoutes:  append([]CompiledRoute(nil), p.CompiledRoutes...),
	}
	sortEdges(shape.Edges)
	sort.Strings(shape.EntryNodeIDs)
	sort.Strings(shape.TerminalNodeIDs)
	sortTerminalExits(shape.TerminalExits)
	sort.Strings(shape.CalledPipelines)
	SortCompiledRoutes(shape.CompiledRoutes)

	body, err := json.Marshal(shape)
	if err != nil {
		return "", fmt.Errorf("marshal pipeline fingerprint input: %w", err)
	}
	sum := blake3.Sum256(body)
	return "blake3:" + hex.EncodeToString(sum[:]), nil
}

func sortEdges(edges []Edge) {
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From == edges[j].From {
			if edges[i].To == edges[j].To {
				return edges[i].EventType < edges[j].EventType
			}
			return edges[i].To < edges[j].To
		}
		return edges[i].From < edges[j].From
	})
}

// SortCompiledRoutes orders compiled routes deterministically for stable manifests
// and stable route-index traversal.
func SortCompiledRoutes(routes []CompiledRoute) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].ID != routes[j].ID {
			return routes[i].ID < routes[j].ID
		}
		if routes[i].Pipeline != routes[j].Pipeline {
			return routes[i].Pipeline < routes[j].Pipeline
		}
		if routes[i].Source.Trigger != routes[j].Source.Trigger {
			return routes[i].Source.Trigger < routes[j].Source.Trigger
		}
		if routes[i].Source.HookSignal != routes[j].Source.HookSignal {
			return routes[i].Source.HookSignal < routes[j].Source.HookSignal
		}
		if routes[i].Source.SourcePlugin != routes[j].Source.SourcePlugin {
			return routes[i].Source.SourcePlugin < routes[j].Source.SourcePlugin
		}
		if routes[i].Source.Pipeline != routes[j].Source.Pipeline {
			return routes[i].Source.Pipeline < routes[j].Source.Pipeline
		}
		if routes[i].Source.StepID != routes[j].Source.StepID {
			return routes[i].Source.StepID < routes[j].Source.StepID
		}
		if routes[i].Source.EventType != routes[j].Source.EventType {
			return routes[i].Source.EventType < routes[j].Source.EventType
		}
		if routes[i].Source.DepthLT != routes[j].Source.DepthLT {
			return routes[i].Source.DepthLT < routes[j].Source.DepthLT
		}
		if routes[i].Destination.Kind != routes[j].Destination.Kind {
			return routes[i].Destination.Kind < routes[j].Destination.Kind
		}
		if routes[i].Destination.StepID != routes[j].Destination.StepID {
			return routes[i].Destination.StepID < routes[j].Destination.StepID
		}
		if routes[i].Destination.Plugin != routes[j].Destination.Plugin {
			return routes[i].Destination.Plugin < routes[j].Destination.Plugin
		}
		if routes[i].Destination.Command != routes[j].Destination.Command {
			return routes[i].Destination.Command < routes[j].Destination.Command
		}
		return routes[i].Destination.CallPipeline < routes[j].Destination.CallPipeline
	})
}

func sortedUnique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	for _, value := range in {
		seen[value] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func terminalNodeIDs(in []compileExit) []string {
	seen := make(map[string]struct{}, len(in))
	for _, exit := range in {
		seen[exit.NodeID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for nodeID := range seen {
		out = append(out, nodeID)
	}
	sort.Strings(out)
	return out
}

func sortedCompileExits(in []compileExit) []compileExit {
	seen := make(map[string]compileExit, len(in))
	for _, exit := range in {
		key := exit.NodeID + "\x00" + exit.EventType
		seen[key] = exit
	}
	out := make([]compileExit, 0, len(seen))
	for _, exit := range seen {
		out = append(out, exit)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].NodeID == out[j].NodeID {
			return out[i].EventType < out[j].EventType
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

func sortedTerminalExits(in []compileExit) []TerminalExit {
	out := make([]TerminalExit, 0, len(in))
	for _, exit := range sortedCompileExits(in) {
		out = append(out, TerminalExit{
			StepID:    exit.NodeID,
			EventType: exit.EventType,
		})
	}
	return out
}

func sortTerminalExits(exits []TerminalExit) {
	sort.Slice(exits, func(i, j int) bool {
		if exits[i].StepID == exits[j].StepID {
			return exits[i].EventType < exits[j].EventType
		}
		return exits[i].StepID < exits[j].StepID
	})
}

func sortedMapKeys(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
