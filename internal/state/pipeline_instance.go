package state

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// PipelineInstanceNamespace is the reserved durable baggage namespace for
	// control-plane pipeline execution identity.
	PipelineInstanceNamespace = "ductile"
	// PipelineInstanceIDField is the durable key storing the pipeline execution
	// instance ID inside accumulated context baggage.
	PipelineInstanceIDField = "pipeline_instance_id"
	// RouteDepthField is the durable key storing the current routed job depth.
	RouteDepthField = "route_depth"
	// RouteMaxDepthField is the durable key storing the loop-guard budget for
	// one pipeline execution tree.
	RouteMaxDepthField = "route_max_depth"
)

// WithPipelineInstanceID merges the given pipeline execution instance ID into a
// JSON object of context updates.
func WithPipelineInstanceID(updates json.RawMessage, instanceID string) (json.RawMessage, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil, fmt.Errorf("pipeline instance id is empty")
	}
	return withPipelineControlPlane(updates, func(ductile map[string]any) error {
		if existing, exists := ductile[PipelineInstanceIDField]; exists && existing != instanceID {
			return fmt.Errorf("context updates already contain %s.%s=%v", PipelineInstanceNamespace, PipelineInstanceIDField, existing)
		}
		ductile[PipelineInstanceIDField] = instanceID
		return nil
	})
}

// WithRouteDepth records the current routed job depth in context updates.
func WithRouteDepth(updates json.RawMessage, depth int) (json.RawMessage, error) {
	if depth < 0 {
		return nil, fmt.Errorf("route depth must be non-negative")
	}
	return withPipelineControlPlane(updates, func(ductile map[string]any) error {
		if existing, exists := ductile[RouteDepthField]; exists {
			if existingDepth, ok := intValue(existing); !ok || existingDepth != depth {
				return fmt.Errorf("context updates already contain %s.%s=%v", PipelineInstanceNamespace, RouteDepthField, existing)
			}
		}
		ductile[RouteDepthField] = depth
		return nil
	})
}

// WithRouteMaxDepth records the execution loop-guard budget in context updates.
func WithRouteMaxDepth(updates json.RawMessage, maxDepth int) (json.RawMessage, error) {
	if maxDepth <= 0 {
		return nil, fmt.Errorf("route max depth must be positive")
	}
	return withPipelineControlPlane(updates, func(ductile map[string]any) error {
		if existing, exists := ductile[RouteMaxDepthField]; exists {
			if existingDepth, ok := intValue(existing); !ok || existingDepth != maxDepth {
				return fmt.Errorf("context updates already contain %s.%s=%v", PipelineInstanceNamespace, RouteMaxDepthField, existing)
			}
		}
		ductile[RouteMaxDepthField] = maxDepth
		return nil
	})
}

// RouteControlPlane is the control-plane execution identity carried in a
// context's accumulated durable baggage under the PipelineInstanceNamespace.
type RouteControlPlane struct {
	PipelineInstanceID string
	RouteDepth         int
	RouteMaxDepth      int
}

// RouteControlPlaneFromAccumulated decodes every control-plane field from
// accumulated context baggage in a single pass, instead of re-unmarshalling the
// blob once per field. Absent fields are zero. Use this where more than one
// field is needed; the single-field helpers below remain for one-field callers.
func RouteControlPlaneFromAccumulated(accumulated json.RawMessage) RouteControlPlane {
	ductile, ok := pipelineControlPlane(accumulated)
	if !ok {
		return RouteControlPlane{}
	}
	instanceID, _ := ductile[PipelineInstanceIDField].(string)
	depth, _ := intValue(ductile[RouteDepthField])
	maxDepth, _ := intValue(ductile[RouteMaxDepthField])
	return RouteControlPlane{
		PipelineInstanceID: strings.TrimSpace(instanceID),
		RouteDepth:         depth,
		RouteMaxDepth:      maxDepth,
	}
}

// PipelineInstanceIDFromAccumulated extracts the durable pipeline execution
// instance ID from accumulated context baggage.
func PipelineInstanceIDFromAccumulated(accumulated json.RawMessage) string {
	ductile, ok := pipelineControlPlane(accumulated)
	if !ok {
		return ""
	}
	instanceID, _ := ductile[PipelineInstanceIDField].(string)
	return strings.TrimSpace(instanceID)
}

// RouteDepthFromAccumulated extracts the current routed job depth from durable
// context baggage.
func RouteDepthFromAccumulated(accumulated json.RawMessage) int {
	ductile, ok := pipelineControlPlane(accumulated)
	if !ok {
		return 0
	}
	value, _ := intValue(ductile[RouteDepthField])
	return value
}

// RouteMaxDepthFromAccumulated extracts the execution loop-guard budget from
// durable context baggage.
func RouteMaxDepthFromAccumulated(accumulated json.RawMessage) int {
	ductile, ok := pipelineControlPlane(accumulated)
	if !ok {
		return 0
	}
	value, _ := intValue(ductile[RouteMaxDepthField])
	return value
}

func withPipelineControlPlane(updates json.RawMessage, mutate func(map[string]any) error) (json.RawMessage, error) {
	obj := map[string]any{}
	if len(updates) > 0 {
		if err := json.Unmarshal(updates, &obj); err != nil {
			return nil, fmt.Errorf("decode context updates: %w", err)
		}
	}

	ductile, err := nestedObject(obj, PipelineInstanceNamespace)
	if err != nil {
		return nil, err
	}
	if err := mutate(ductile); err != nil {
		return nil, err
	}
	obj[PipelineInstanceNamespace] = ductile

	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshal context updates: %w", err)
	}
	return raw, nil
}

func pipelineControlPlane(accumulated json.RawMessage) (map[string]any, bool) {
	if len(accumulated) == 0 {
		return nil, false
	}

	var obj map[string]any
	if err := json.Unmarshal(accumulated, &obj); err != nil {
		return nil, false
	}

	ductile, ok := obj[PipelineInstanceNamespace].(map[string]any)
	if !ok {
		return nil, false
	}
	return ductile, true
}

func nestedObject(obj map[string]any, key string) (map[string]any, error) {
	value, exists := obj[key]
	if !exists || value == nil {
		return map[string]any{}, nil
	}
	nested, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("context updates key %q must be an object", key)
	}
	return nested, nil
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
