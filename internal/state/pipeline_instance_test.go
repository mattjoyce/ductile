package state

import (
	"encoding/json"
	"testing"
)

func TestRouteControlPlaneFromAccumulated(t *testing.T) {
	// Build an accumulated blob the same way the runtime seeds it.
	var acc json.RawMessage
	acc, err := WithPipelineInstanceID(acc, "inst-1")
	if err != nil {
		t.Fatalf("WithPipelineInstanceID: %v", err)
	}
	acc, err = WithRouteDepth(acc, 2)
	if err != nil {
		t.Fatalf("WithRouteDepth: %v", err)
	}
	acc, err = WithRouteMaxDepth(acc, 7)
	if err != nil {
		t.Fatalf("WithRouteMaxDepth: %v", err)
	}

	cp := RouteControlPlaneFromAccumulated(acc)
	if cp.PipelineInstanceID != "inst-1" {
		t.Fatalf("PipelineInstanceID = %q, want inst-1", cp.PipelineInstanceID)
	}
	if cp.RouteDepth != 2 {
		t.Fatalf("RouteDepth = %d, want 2", cp.RouteDepth)
	}
	if cp.RouteMaxDepth != 7 {
		t.Fatalf("RouteMaxDepth = %d, want 7", cp.RouteMaxDepth)
	}

	// One-pass reader must agree field-for-field with the single-field helpers.
	if got := PipelineInstanceIDFromAccumulated(acc); got != cp.PipelineInstanceID {
		t.Fatalf("instance id mismatch: one-pass %q vs single %q", cp.PipelineInstanceID, got)
	}
	if got := RouteDepthFromAccumulated(acc); got != cp.RouteDepth {
		t.Fatalf("route depth mismatch: one-pass %d vs single %d", cp.RouteDepth, got)
	}
	if got := RouteMaxDepthFromAccumulated(acc); got != cp.RouteMaxDepth {
		t.Fatalf("route max depth mismatch: one-pass %d vs single %d", cp.RouteMaxDepth, got)
	}
}

func TestRouteControlPlaneFromAccumulatedEmpty(t *testing.T) {
	for _, tc := range []struct {
		name string
		acc  json.RawMessage
	}{
		{name: "nil", acc: nil},
		{name: "empty object", acc: json.RawMessage(`{}`)},
		{name: "no ductile namespace", acc: json.RawMessage(`{"whisper":{"attempt":1}}`)},
		{name: "malformed", acc: json.RawMessage(`{not json`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if cp := RouteControlPlaneFromAccumulated(tc.acc); cp != (RouteControlPlane{}) {
				t.Fatalf("expected zero RouteControlPlane, got %+v", cp)
			}
		})
	}
}
