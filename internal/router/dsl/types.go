package dsl

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/router/conditions"
	"gopkg.in/yaml.v3"
)

// FileSpec is one YAML file containing one or more pipelines.
type FileSpec struct {
	Pipelines []PipelineSpec `yaml:"pipelines"`
}

// PipelineSpec defines a single pipeline entry in YAML.
type PipelineSpec struct {
	Name          string                `yaml:"name"`
	On            string                `yaml:"on"`
	OnHook        string                `yaml:"on-hook,omitempty"`
	FromPlugin    string                `yaml:"from_plugin,omitempty"`
	If            *conditions.Condition `yaml:"if,omitempty"`
	MaxDepth      *int                  `yaml:"max_depth,omitempty"`
	Steps         []StepSpec            `yaml:"steps"`
	ExecutionMode string                `yaml:"execution_mode,omitempty"`
	Timeout       time.Duration         `yaml:"timeout,omitempty"`
}

// StepSpec is one DSL step. Exactly one execution field must be set:
// - uses
// - call
// - relay
// - steps
// - split
type StepSpec struct {
	ID    string                `yaml:"id,omitempty"`
	Uses  string                `yaml:"uses,omitempty"`
	Call  string                `yaml:"call,omitempty"`
	Relay *RelaySpec            `yaml:"relay,omitempty"`
	If    *conditions.Condition `yaml:"if,omitempty"`
	Steps []StepSpec            `yaml:"steps,omitempty"`
	Split []StepSpec            `yaml:"split,omitempty"`
	With  map[string]string     `yaml:"with,omitempty"`
	// Baggage names durable values for downstream pipeline steps.
	Baggage *BaggageSpec `yaml:"baggage,omitempty"`
}

// RelaySpec defines one first-class remote event relay step.
type RelaySpec struct {
	To        string            `yaml:"to"`
	Event     string            `yaml:"event"`
	DedupeKey string            `yaml:"dedupe_key,omitempty"`
	With      map[string]string `yaml:"with,omitempty"`
	Baggage   *BaggageSpec      `yaml:"baggage,omitempty"`
}

func (r *RelaySpec) clone() *RelaySpec {
	if r == nil {
		return nil
	}
	out := &RelaySpec{
		To:        r.To,
		Event:     r.Event,
		DedupeKey: r.DedupeKey,
		Baggage:   r.Baggage.clone(),
	}
	if len(r.With) > 0 {
		out.With = make(map[string]string, len(r.With))
		for key, value := range r.With {
			out.With[key] = value
		}
	}
	return out
}

// BaggageSpec defines the durable values a uses step claims.
type BaggageSpec struct {
	Mappings map[string]string
	Bulk     *BaggageBulkSpec
}

// BaggageBulkSpec imports all or part of a source object under a namespace.
type BaggageBulkSpec struct {
	From      string
	Namespace string
}

// UnmarshalYAML accepts baggage mappings with optional bulk import controls:
//
//	baggage:
//	  summary.text: payload.text
//	  from: payload
//	  namespace: whisper
func (b *BaggageSpec) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("baggage must be a mapping")
	}

	out := BaggageSpec{Mappings: make(map[string]string)}
	bulk := BaggageBulkSpec{}
	hasFrom := false
	hasNamespace := false

	for i := 0; i < len(node.Content); i += 2 {
		key := strings.TrimSpace(node.Content[i].Value)
		if key == "" {
			return fmt.Errorf("baggage keys must be non-empty")
		}

		valueNode := node.Content[i+1]
		if valueNode.Kind != yaml.ScalarNode {
			return fmt.Errorf("baggage.%s must be a scalar string", key)
		}
		value := strings.TrimSpace(valueNode.Value)
		if value == "" {
			return fmt.Errorf("baggage.%s must be non-empty", key)
		}

		switch key {
		case "from":
			hasFrom = true
			bulk.From = value
		case "namespace":
			hasNamespace = true
			bulk.Namespace = value
		default:
			out.Mappings[key] = value
		}
	}

	if hasNamespace && !hasFrom {
		return fmt.Errorf("baggage namespace requires from")
	}
	if hasFrom {
		out.Bulk = &bulk
	}
	if len(out.Mappings) == 0 {
		out.Mappings = nil
	}

	*b = out
	return nil
}

// Empty reports whether this spec contains no claims.
func (b *BaggageSpec) Empty() bool {
	return b == nil || len(b.Mappings) == 0 && b.Bulk == nil
}

func (b *BaggageSpec) clone() *BaggageSpec {
	if b == nil {
		return nil
	}

	out := &BaggageSpec{}
	if len(b.Mappings) > 0 {
		out.Mappings = make(map[string]string, len(b.Mappings))
		for key, value := range b.Mappings {
			out.Mappings[key] = value
		}
	}
	if b.Bulk != nil {
		bulk := *b.Bulk
		out.Bulk = &bulk
	}
	return out
}

// NodeKind identifies the executable action represented by a DAG node.
type NodeKind string

const (
	NodeKindUses   NodeKind = "uses"
	NodeKindCall   NodeKind = "call"
	NodeKindRelay  NodeKind = "relay"
	NodeKindSwitch NodeKind = "switch"
)

// Node is one executable vertex in a compiled pipeline DAG.
type Node struct {
	ID        string
	Kind      NodeKind
	Uses      string
	Call      string
	Relay     *RelaySpec
	Condition *conditions.Condition
	With      map[string]string
	Baggage   *BaggageSpec
}

// Edge defines a directed dependency between two nodes.
type Edge struct {
	From      string
	To        string
	EventType string
}

// TerminalExit defines one terminal transition point for a compiled pipeline.
type TerminalExit struct {
	StepID    string
	EventType string
}

// CompiledRouteDestinationKind identifies what kind of destination a compiled route targets.
type CompiledRouteDestinationKind string

const (
	CompiledRouteDestinationUses     CompiledRouteDestinationKind = "uses"
	CompiledRouteDestinationCall     CompiledRouteDestinationKind = "call"
	CompiledRouteDestinationTerminal CompiledRouteDestinationKind = "terminal"
)

// CompiledRouteSource is the match side of one compiled orchestration rule.
//
// SourcePlugin scopes entry routes (root triggers and on-hook lifecycle hooks)
// to a specific upstream plugin. Empty means today's behavior — match regardless
// of upstream plugin. Transition routes do not use SourcePlugin.
type CompiledRouteSource struct {
	Trigger      string
	Pipeline     string
	StepID       string
	HookSignal   string
	SourcePlugin string
	EventType    string
	DepthLT      int
	If           *conditions.Condition
}

// CompiledRouteDestination is the dispatch side of one compiled orchestration rule.
type CompiledRouteDestination struct {
	Kind         CompiledRouteDestinationKind
	StepID       string
	Plugin       string
	Command      string
	CallPipeline string
}

// CompiledRoute is a deterministic manifest row derived from a pipeline DAG.
// Sprint 5 uses this to make the route-oriented orchestration shape explicit
// before runtime matching switches over to consume compiled routes directly.
type CompiledRoute struct {
	ID          string
	Pipeline    string
	Source      CompiledRouteSource
	Destination CompiledRouteDestination
}

// Pipeline is a compiled DAG for one named pipeline.
type Pipeline struct {
	Name            string
	Trigger         string
	IsHook          bool   // true when triggered by on-hook: (lifecycle signal) rather than on: (plugin event)
	FromPlugin      string // optional source-plugin selector for entry routes (Sprint 17)
	ExecutionMode   string
	Timeout         time.Duration
	If              *conditions.Condition // optional pipeline-level trigger predicate (entry routes only)
	AuthorMaxDepth  *int                  // user-set max_depth: overrides MaxRouteDepth when non-nil; *0 means unlimited
	Nodes           map[string]Node
	Edges           []Edge
	EntryNodeIDs    []string
	TerminalNodeIDs []string
	TerminalExits   []TerminalExit
	CalledPipelines []string
	MaxRouteDepth   int
	CompiledRoutes  []CompiledRoute
	Fingerprint     string // sha256:<hex> of normalized compiled form.
}

// Set is a compiled collection of pipelines keyed by name.
type Set struct {
	Pipelines map[string]*Pipeline
}
