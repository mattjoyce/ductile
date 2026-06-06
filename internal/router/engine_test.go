package router

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/router/conditions"
	"github.com/mattjoyce/ductile/internal/router/dsl"
)

func TestRouterNextPoisonPredicateIsolatedFromHealthyRoutes(t *testing.T) {
	// Two pipelines on the same trigger. The first carries a predicate that
	// errors at eval time (numeric gt against a string payload). The second is
	// healthy. A poison predicate on one route must not abort routing for the
	// other — and must not surface as an error from Next.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "poison",
			On:   "event.shared",
			If:   &conditions.Condition{Path: "payload.count", Op: conditions.OpGT, Value: 1},
			Steps: []dsl.StepSpec{
				{ID: "poison_step", Uses: "plugin-poison"},
			},
		},
		{
			Name: "healthy",
			On:   "event.shared",
			Steps: []dsl.StepSpec{
				{ID: "healthy_step", Uses: "plugin-healthy"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{
			Type:    "event.shared",
			Payload: map[string]any{"count": "not-a-number"},
		},
	})
	if err != nil {
		t.Fatalf("Next returned error for a poison predicate; want fault isolation: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1 (healthy route survives poison route): %+v", len(out), out)
	}
	if out[0].Plugin != "plugin-healthy" {
		t.Fatalf("surviving dispatch plugin = %q, want plugin-healthy", out[0].Plugin)
	}
}

func TestValidateFromPluginExists(t *testing.T) {
	compile := func(t *testing.T, fromPlugin string) *dsl.Set {
		t.Helper()
		set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
			{
				Name:       "scoped",
				On:         "event.start",
				FromPlugin: fromPlugin,
				Steps:      []dsl.StepSpec{{ID: "step_b", Uses: "plugin-b"}},
			},
		})
		if err != nil {
			t.Fatalf("CompileSpecs: %v", err)
		}
		return set
	}

	registry := plugin.NewRegistry()
	if err := registry.Add(&plugin.Plugin{Name: "plugin-a"}); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	t.Run("known from_plugin loads", func(t *testing.T) {
		if err := validateFromPluginExists(compile(t, "plugin-a"), registry); err != nil {
			t.Fatalf("validateFromPluginExists: unexpected error %v", err)
		}
	})

	t.Run("empty from_plugin loads", func(t *testing.T) {
		if err := validateFromPluginExists(compile(t, ""), registry); err != nil {
			t.Fatalf("validateFromPluginExists: unexpected error %v", err)
		}
	})

	t.Run("unknown from_plugin is rejected at load", func(t *testing.T) {
		err := validateFromPluginExists(compile(t, "plugin-ghost"), registry)
		if err == nil {
			t.Fatalf("expected error for unknown from_plugin")
		}
		if !strings.Contains(err.Error(), "from_plugin references unknown plugin") {
			t.Fatalf("error = %v, want from_plugin unknown-plugin message", err)
		}
	})
}

func TestRouterNextRootTrigger(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "chain",
			On:   "event.start",
			Steps: []dsl.StepSpec{
				{ID: "step_b", Uses: "plugin-b"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		SourceJobID: "job-a",
		Event: protocol.Event{
			Type:    "event.start",
			Payload: map[string]any{"origin_channel_id": "chan-1"},
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "plugin-b" || out[0].Command != "handle" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].PipelineName != "chain" || out[0].StepID != "step_b" {
		t.Fatalf("unexpected pipeline metadata: %+v", out[0])
	}
	if out[0].PipelineInstanceID == "" {
		t.Fatalf("expected root trigger to assign pipeline instance id: %+v", out[0])
	}
}

func TestRouterGetNode(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:  "chain",
		On:    "event.start",
		Steps: []dsl.StepSpec{{ID: "step_b", Uses: "plugin-b"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	node, ok := r.GetNode("chain", "step_b")
	if !ok {
		t.Fatalf("expected node lookup success")
	}
	if node.ID != "step_b" {
		t.Fatalf("node.ID = %q, want %q", node.ID, "step_b")
	}
}

func TestRouterGetCompiledRoutes(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name: "chain",
		On:   "event.start",
		Steps: []dsl.StepSpec{
			{ID: "step_b", Uses: "plugin-b"},
			{ID: "step_c", Uses: "plugin-c"},
		},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	routes := r.GetCompiledRoutes("chain")
	if len(routes) != 3 {
		t.Fatalf("compiled route count = %d, want 3", len(routes))
	}
	if routes[0].ID != "edge:step_b->step_c" {
		t.Fatalf("first route id = %q, want edge:step_b->step_c", routes[0].ID)
	}

	// Returned slice must be a copy.
	routes[0].ID = "mutated"
	routes2 := r.GetCompiledRoutes("chain")
	if routes2[0].ID != "edge:step_b->step_c" {
		t.Fatalf("compiled routes mutated through returned slice: %q", routes2[0].ID)
	}
}

func TestRouterNewBuildsCompiledRoutesForManualPipelines(t *testing.T) {
	r := New(&dsl.Set{
		Pipelines: map[string]*dsl.Pipeline{
			"test-pipeline": {
				Name:    "test-pipeline",
				Trigger: "test.trigger",
				Nodes: map[string]dsl.Node{
					"entry": {ID: "entry", Kind: dsl.NodeKindUses, Uses: "echo"},
				},
				EntryNodeIDs:    []string{"entry"},
				TerminalNodeIDs: []string{"entry"},
			},
		},
	}, nil)

	routes := r.GetCompiledRoutes("test-pipeline")
	if len(routes) != 2 {
		t.Fatalf("compiled route count = %d, want 2", len(routes))
	}

	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{Type: "test.trigger"},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "echo" || out[0].StepID != "entry" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
}

func TestRouterNextStepSuccessorTransition(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "chain",
			On:   "event.start",
			Steps: []dsl.StepSpec{
				{ID: "step_b", Uses: "plugin-b"},
				{ID: "step_c", Uses: "plugin-c"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		SourceJobID:              "job-b",
		SourcePipeline:           "chain",
		SourceStepID:             "step_b",
		SourcePipelineInstanceID: "instance-1",
		SourceEventID:            "evt-1",
		Event: protocol.Event{
			Type: "any.event",
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "plugin-c" || out[0].StepID != "step_c" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].PipelineInstanceID != "instance-1" {
		t.Fatalf("pipeline instance id = %q, want %q", out[0].PipelineInstanceID, "instance-1")
	}
}

func TestRouterNextStepSuccessorTransitionRequiresPipelineInstanceID(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "chain",
			On:   "event.start",
			Steps: []dsl.StepSpec{
				{ID: "step_b", Uses: "plugin-b"},
				{ID: "step_c", Uses: "plugin-c"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		SourceJobID:    "job-b",
		SourcePipeline: "chain",
		SourceStepID:   "step_b",
		SourceEventID:  "evt-1",
		Event: protocol.Event{
			Type: "any.event",
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 without pipeline instance id", len(out))
	}
}

func TestRouterNextRootTriggerExpandsCalledPipeline(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "process-audio",
			On:   "internal.process_audio",
			Steps: []dsl.StepSpec{
				{ID: "transcribe", Uses: "transcriber"},
			},
		},
		{
			Name: "wisdom-chain",
			On:   "event.start",
			Steps: []dsl.StepSpec{
				{ID: "processing", Call: "process-audio"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		SourceJobID: "job-a",
		Event: protocol.Event{
			Type: "event.start",
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "transcriber" || out[0].Command != "handle" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].PipelineName != "process-audio" || out[0].StepID != "transcribe" {
		t.Fatalf("unexpected pipeline metadata: %+v", out[0])
	}
	if out[0].PipelineInstanceID == "" {
		t.Fatalf("expected root trigger to assign pipeline instance id: %+v", out[0])
	}
}

func TestRouterNextStepTransitionIntoCalledPipeline(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "process-audio",
			On:   "internal.process_audio",
			Steps: []dsl.StepSpec{
				{ID: "transcribe", Uses: "transcriber"},
			},
		},
		{
			Name: "chain",
			On:   "event.start",
			Steps: []dsl.StepSpec{
				{ID: "step_b", Uses: "plugin-b"},
				{ID: "processing", Call: "process-audio"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		SourceJobID:              "job-b",
		SourcePipeline:           "chain",
		SourceStepID:             "step_b",
		SourcePipelineInstanceID: "instance-1",
		SourceEventID:            "evt-1",
		Event: protocol.Event{
			Type: "any.event",
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "transcriber" || out[0].Command != "handle" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].PipelineName != "process-audio" || out[0].StepID != "transcribe" {
		t.Fatalf("unexpected pipeline metadata: %+v", out[0])
	}
	if out[0].PipelineInstanceID != "instance-1" {
		t.Fatalf("pipeline instance id = %q, want %q", out[0].PipelineInstanceID, "instance-1")
	}
}

func TestRouterNextHookDispatch(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "notify-on-complete",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "notify", Uses: "discord-notifier"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)

	payload := map[string]any{"plugin": "claude_harvest", "status": "succeeded"}
	out, err := r.NextHook(context.Background(), "claude_harvest", "job.completed", payload, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "discord-notifier" || out[0].Command != "handle" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].Event.Type != "job.completed" {
		t.Fatalf("event type = %q, want %q", out[0].Event.Type, "job.completed")
	}
	if out[0].Event.Payload["plugin"] != "claude_harvest" {
		t.Fatalf("payload plugin = %v, want claude_harvest", out[0].Event.Payload["plugin"])
	}
	// Hook dispatches must have no pipeline/step context — they are root jobs.
	if out[0].PipelineName != "" || out[0].StepID != "" {
		t.Fatalf("hook dispatch must be root-level: pipeline=%q step=%q", out[0].PipelineName, out[0].StepID)
	}
}

func TestRouterNextHookUnknownSignalReturnsEmpty(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "notify-on-complete",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "notify", Uses: "discord-notifier"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.NextHook(context.Background(), "", "job.failed", nil, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 for unregistered signal", len(out))
	}
}

func TestRouterHookPipelineNotInTriggerIndex(t *testing.T) {
	// Hook pipelines must not appear in the regular trigger index.
	// A regular pipeline's on: event must not inadvertently match a hook signal.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "hook-pipeline",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "notify", Uses: "discord-notifier"}},
		},
		{
			Name:  "regular-pipeline",
			On:    "plugin.event",
			Steps: []dsl.StepSpec{{ID: "step", Uses: "plugin-a"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)

	// Emitting "job.completed" as a plugin event must not trigger the hook pipeline.
	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{Type: "job.completed", Payload: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("Next dispatched %d jobs for hook signal — hook pipeline must not be in trigger index", len(out))
	}
}

func TestRouterNextHookMultiplePipelines(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "notify-a",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "n", Uses: "discord-notifier"}},
		},
		{
			Name:   "notify-b",
			OnHook: "job.completed",
			Steps:  []dsl.StepSpec{{ID: "n", Uses: "slack-notifier"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.NextHook(context.Background(), "any_plugin", "job.completed", nil, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("dispatch count = %d, want 2", len(out))
	}
}

func TestRouterNextHookExpandsCalledPipeline(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:   "hook-dispatch",
			OnHook: "job.completed",
			Steps: []dsl.StepSpec{
				{ID: "fanout", Call: "hook-target"},
			},
		},
		{
			Name: "hook-target",
			On:   "internal.hook.target",
			Steps: []dsl.StepSpec{
				{ID: "notify", Uses: "discord-notifier"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)

	payload := map[string]any{"plugin": "claude_harvest", "status": "succeeded"}
	out, err := r.NextHook(context.Background(), "claude_harvest", "job.completed", payload, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "discord-notifier" || out[0].Command != "handle" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].Event.Type != "job.completed" {
		t.Fatalf("event type = %q, want %q", out[0].Event.Type, "job.completed")
	}
	if out[0].PipelineName != "" || out[0].StepID != "" {
		t.Fatalf("hook dispatch must remain root-level after call expansion: pipeline=%q step=%q", out[0].PipelineName, out[0].StepID)
	}
}

func TestRouterGetEntryDispatchesExpandsCalledPipeline(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name: "process-audio",
			On:   "internal.process_audio",
			Steps: []dsl.StepSpec{
				{ID: "transcribe", Uses: "transcriber"},
			},
		},
		{
			Name: "wisdom-chain",
			On:   "event.start",
			Steps: []dsl.StepSpec{
				{ID: "processing", Call: "process-audio"},
			},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.GetEntryDispatches("wisdom-chain", protocol.Event{Type: "event.start"})
	if err != nil {
		t.Fatalf("GetEntryDispatches: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "transcriber" || out[0].Command != "handle" {
		t.Fatalf("unexpected dispatch: %+v", out[0])
	}
	if out[0].PipelineName != "process-audio" || out[0].StepID != "transcribe" {
		t.Fatalf("unexpected pipeline metadata: %+v", out[0])
	}
}

func TestRouterNextRootTriggerSkipsWhenIfFalse(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name: "filtered",
		On:   "data.change.garmin",
		If: &conditions.Condition{
			Path:  "payload.kind",
			Op:    conditions.OpEq,
			Value: "workout",
		},
		Steps: []dsl.StepSpec{{ID: "consume", Uses: "summary"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)

	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{
			Type:    "data.change.garmin",
			Payload: map[string]any{"kind": "weight"},
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("predicate-false dispatched %d jobs, want 0: %+v", len(out), out)
	}

	out, err = r.Next(context.Background(), Request{
		Event: protocol.Event{
			Type:    "data.change.garmin",
			Payload: map[string]any{"kind": "workout"},
		},
	})
	if err != nil {
		t.Fatalf("Next (true): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("predicate-true dispatch count = %d, want 1", len(out))
	}
	if out[0].Plugin != "summary" {
		t.Fatalf("unexpected plugin: %+v", out[0])
	}
}

func TestRouterNextRootTriggerNoIfPreservesBehaviour(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:  "unfiltered",
		On:    "data.change.garmin",
		Steps: []dsl.StepSpec{{ID: "consume", Uses: "summary"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)
	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{
			Type:    "data.change.garmin",
			Payload: map[string]any{"kind": "anything"},
		},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1 (no predicate = today's behaviour)", len(out))
	}
}

func TestRouterNextRootTriggerIfPartitionsMultipleConsumers(t *testing.T) {
	// Two pipelines on the same trigger, one with an if: predicate.
	// The unconditioned one must always fire; the conditioned one only when
	// payload satisfies the predicate.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{
		{
			Name:  "always",
			On:    "git_repo_sync.completed",
			Steps: []dsl.StepSpec{{ID: "policy", Uses: "repo_policy"}},
		},
		{
			Name: "only-with-changes",
			On:   "git_repo_sync.completed",
			If: &conditions.Condition{
				Path:  "payload.new_commits",
				Op:    conditions.OpEq,
				Value: true,
			},
			Steps: []dsl.StepSpec{{ID: "log", Uses: "changelog"}},
		},
	})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)

	// new_commits=false: only "always" fires.
	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{
			Type:    "git_repo_sync.completed",
			Payload: map[string]any{"new_commits": false},
		},
	})
	if err != nil {
		t.Fatalf("Next (false): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1 (predicate-false skips conditioned consumer)", len(out))
	}
	if out[0].PipelineName != "always" {
		t.Fatalf("expected only the unconditioned pipeline to fire, got %q", out[0].PipelineName)
	}

	// new_commits=true: both fire.
	out, err = r.Next(context.Background(), Request{
		Event: protocol.Event{
			Type:    "git_repo_sync.completed",
			Payload: map[string]any{"new_commits": true},
		},
	})
	if err != nil {
		t.Fatalf("Next (true): %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("dispatch count = %d, want 2 (both consumers fire)", len(out))
	}
}

func TestRouterNextHookSkipsWhenIfFalse(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:   "scoped-failure-notify",
		OnHook: "job.failed",
		If: &conditions.Condition{
			Path:  "payload.plugin",
			Op:    conditions.OpEq,
			Value: "fabric",
		},
		Steps: []dsl.StepSpec{{ID: "n", Uses: "discord-notifier"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	r := New(set, nil)

	out, err := r.NextHook(context.Background(), "check_youtube", "job.failed",
		map[string]any{"plugin": "check_youtube"}, nil)
	if err != nil {
		t.Fatalf("NextHook (false): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("predicate-false hook dispatched %d jobs, want 0", len(out))
	}

	out, err = r.NextHook(context.Background(), "fabric", "job.failed",
		map[string]any{"plugin": "fabric"}, nil)
	if err != nil {
		t.Fatalf("NextHook (true): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("predicate-true hook dispatch count = %d, want 1", len(out))
	}
}

func TestRouterCompileRejectsInvalidPipelineIf(t *testing.T) {
	_, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name: "bad-if",
		On:   "x",
		If: &conditions.Condition{
			// Invalid: path with no op.
			Path: "payload.k",
		},
		Steps: []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}})
	if err == nil {
		t.Fatal("expected compile error for malformed pipeline-level if")
	}
}

func TestRouterAuthorMaxDepthOverridesAutoComputed(t *testing.T) {
	override := 7
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:     "with-max",
		On:       "x",
		MaxDepth: &override,
		Steps:    []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}

	pipeline := set.Pipelines["with-max"]
	if pipeline.MaxRouteDepth != 7 {
		t.Fatalf("MaxRouteDepth = %d, want 7 (author override)", pipeline.MaxRouteDepth)
	}
	for _, route := range pipeline.CompiledRoutes {
		if route.Source.Trigger == "" {
			continue
		}
		if route.Source.DepthLT != 7 {
			t.Fatalf("entry route DepthLT = %d, want 7", route.Source.DepthLT)
		}
	}
}

func TestRouterAuthorMaxDepthZeroMeansUnlimited(t *testing.T) {
	zero := 0
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:     "unlimited",
		On:       "x",
		MaxDepth: &zero,
		Steps: []dsl.StepSpec{
			{ID: "a", Uses: "p"},
			{ID: "b", Uses: "p"},
			{ID: "c", Uses: "p"},
		},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	pipeline := set.Pipelines["unlimited"]
	if pipeline.MaxRouteDepth != 0 {
		t.Fatalf("MaxRouteDepth = %d, want 0 (author-set 0 = unlimited per §11.3)", pipeline.MaxRouteDepth)
	}
	for _, route := range pipeline.CompiledRoutes {
		if route.Source.DepthLT != 0 {
			t.Fatalf("DepthLT = %d, want 0 (unlimited)", route.Source.DepthLT)
		}
	}
}

func TestRouterCompileRejectsNegativeMaxDepth(t *testing.T) {
	neg := -1
	_, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:     "bad-max",
		On:       "x",
		MaxDepth: &neg,
		Steps:    []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}})
	if err == nil {
		t.Fatal("expected compile error for negative max_depth")
	}
}

func TestCloneEvent(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name string
		ev   protocol.Event
	}{
		{
			name: "full event",
			ev: protocol.Event{
				Type:      "test.event",
				DedupeKey: "key-1",
				Source:    "plugin-1",
				Timestamp: now,
				EventID:   "evt-123",
				Payload:   map[string]any{"foo": "bar", "num": 42},
			},
		},
		{
			name: "nil payload",
			ev: protocol.Event{
				Type:      "test.event",
				Timestamp: now,
			},
		},
		{
			name: "empty payload",
			ev: protocol.Event{
				Type:      "test.event",
				Timestamp: now,
				Payload:   map[string]any{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloned := cloneEvent(tt.ev)

			if cloned.Type != tt.ev.Type {
				t.Errorf("Type mismatch: got %q, want %q", cloned.Type, tt.ev.Type)
			}
			if cloned.DedupeKey != tt.ev.DedupeKey {
				t.Errorf("DedupeKey mismatch: got %q, want %q", cloned.DedupeKey, tt.ev.DedupeKey)
			}
			if cloned.Source != tt.ev.Source {
				t.Errorf("Source mismatch: got %q, want %q", cloned.Source, tt.ev.Source)
			}
			if !cloned.Timestamp.Equal(tt.ev.Timestamp) {
				t.Errorf("Timestamp mismatch: got %v, want %v", cloned.Timestamp, tt.ev.Timestamp)
			}
			if cloned.EventID != tt.ev.EventID {
				t.Errorf("EventID mismatch: got %q, want %q", cloned.EventID, tt.ev.EventID)
			}

			if tt.ev.Payload == nil {
				if cloned.Payload != nil {
					t.Error("Payload should be nil")
				}
			} else {
				if cloned.Payload == nil {
					t.Fatal("Payload should not be nil")
				}
				if len(cloned.Payload) != len(tt.ev.Payload) {
					t.Errorf("Payload length mismatch: got %d, want %d", len(cloned.Payload), len(tt.ev.Payload))
				}
				for k, v := range tt.ev.Payload {
					if cloned.Payload[k] != v {
						t.Errorf("Payload key %q mismatch: got %v, want %v", k, cloned.Payload[k], v)
					}
				}

				// Verify it's a new map
				cloned.Payload["new_key"] = "new_val"
				if _, ok := tt.ev.Payload["new_key"]; ok {
					t.Error("Modifying cloned payload affected original payload")
				}
			}
		})
	}
}

// --- Sprint 17 -------------------------------------------------------------
//
// from_plugin: scopes a pipeline's trigger or hook signal to a single source
// plugin. Entry-route 'if:' predicates evaluate against payload + context so
// authors can gate fan-out on durable baggage already claimed upstream.

func TestRouterNextHookFromPluginMatches(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:       "page-on-claude-failure",
		OnHook:     "job.failed",
		FromPlugin: "claude_harvest",
		Steps:      []dsl.StepSpec{{ID: "page", Uses: "pagerduty"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	out, err := r.NextHook(context.Background(), "claude_harvest", "job.failed",
		map[string]any{"plugin": "claude_harvest"}, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1 (matching from_plugin)", len(out))
	}
	if out[0].Plugin != "pagerduty" {
		t.Fatalf("unexpected plugin: %+v", out[0])
	}
}

func TestRouterNextHookFromPluginDoesNotMatchOtherSourcePlugins(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:       "page-on-claude-failure",
		OnHook:     "job.failed",
		FromPlugin: "claude_harvest",
		Steps:      []dsl.StepSpec{{ID: "page", Uses: "pagerduty"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	out, err := r.NextHook(context.Background(), "fabric", "job.failed",
		map[string]any{"plugin": "fabric"}, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 (mismatched from_plugin)", len(out))
	}
}

func TestRouterNextHookFromPluginRejectsEmptySourcePlugin(t *testing.T) {
	// from_plugin: is a positive selector. Empty source plugin must not match
	// it — the route's selector is an explicit assertion.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:       "page-on-claude-failure",
		OnHook:     "job.failed",
		FromPlugin: "claude_harvest",
		Steps:      []dsl.StepSpec{{ID: "page", Uses: "pagerduty"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	out, err := r.NextHook(context.Background(), "", "job.failed",
		map[string]any{"plugin": ""}, nil)
	if err != nil {
		t.Fatalf("NextHook: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 (empty source plugin against from_plugin selector)", len(out))
	}
}

func TestRouterNextHookEmptyFromPluginPreservesBehaviour(t *testing.T) {
	// No from_plugin selector — every job.failed signal still triggers the
	// hook, regardless of source plugin.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:   "notify-any-failure",
		OnHook: "job.failed",
		Steps:  []dsl.StepSpec{{ID: "notify", Uses: "discord"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	for _, plugin := range []string{"claude_harvest", "fabric", ""} {
		out, err := r.NextHook(context.Background(), plugin, "job.failed",
			map[string]any{"plugin": plugin}, nil)
		if err != nil {
			t.Fatalf("NextHook(%q): %v", plugin, err)
		}
		if len(out) != 1 {
			t.Fatalf("dispatch count for plugin=%q = %d, want 1", plugin, len(out))
		}
	}
}

func TestRouterNextRootTriggerFromPluginScopes(t *testing.T) {
	// from_plugin: on a plugin-emitted event (on:) — only fires when the
	// upstream plugin matches.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:       "garmin-only",
		On:         "data.change",
		FromPlugin: "garmin",
		Steps:      []dsl.StepSpec{{ID: "consume", Uses: "summary"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	out, err := r.Next(context.Background(), Request{
		SourcePlugin: "garmin",
		Event:        protocol.Event{Type: "data.change", Payload: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1 (matching from_plugin)", len(out))
	}

	out, err = r.Next(context.Background(), Request{
		SourcePlugin: "weatherman",
		Event:        protocol.Event{Type: "data.change", Payload: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 (mismatched from_plugin)", len(out))
	}
}

func TestRouterNextRootTriggerFromPluginEmptySourcePluginRejected(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:       "garmin-only",
		On:         "data.change",
		FromPlugin: "garmin",
		Steps:      []dsl.StepSpec{{ID: "consume", Uses: "summary"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	out, err := r.Next(context.Background(), Request{
		// Webhook-style trigger: no source plugin known.
		Event: protocol.Event{Type: "data.change", Payload: map[string]any{}},
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 (empty source plugin against from_plugin selector)", len(out))
	}
}

func TestRouterNextRootTriggerIfEvaluatesAgainstSourceContext(t *testing.T) {
	// Pipeline-level if: against context.* — true branch fires, false branch
	// does not. Demonstrates that durable upstream baggage is reachable from
	// entry-route predicates without copying values into payload.
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name: "notify-on-first-attempt",
		On:   "git_repo_sync.completed",
		If: &conditions.Condition{
			Path:  "context.whisper.attempt",
			Op:    conditions.OpEq,
			Value: 1,
		},
		Steps: []dsl.StepSpec{{ID: "notify", Uses: "discord"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	// First attempt: predicate true.
	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{Type: "git_repo_sync.completed", Payload: map[string]any{}},
		SourceContext: map[string]any{
			"whisper": map[string]any{"attempt": 1},
		},
	})
	if err != nil {
		t.Fatalf("Next (true): %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("dispatch count = %d, want 1 (predicate-true on context.*)", len(out))
	}

	// Retry: predicate false.
	out, err = r.Next(context.Background(), Request{
		Event: protocol.Event{Type: "git_repo_sync.completed", Payload: map[string]any{}},
		SourceContext: map[string]any{
			"whisper": map[string]any{"attempt": 3},
		},
	})
	if err != nil {
		t.Fatalf("Next (false): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 (predicate-false on context.*)", len(out))
	}
}

func TestRouterNextRootTriggerIfAbsentContextPredicateFails(t *testing.T) {
	// Absent context: predicate against context.* simply fails to match.
	// No special-case error — same semantics as a payload predicate against
	// an absent payload key. Documented behaviour for routes that fire from
	// surfaces with no upstream context (webhook ingress, scheduler tick).
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name: "notify-on-first-attempt",
		On:   "git_repo_sync.completed",
		If: &conditions.Condition{
			Path:  "context.whisper.attempt",
			Op:    conditions.OpEq,
			Value: 1,
		},
		Steps: []dsl.StepSpec{{ID: "notify", Uses: "discord"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	r := New(set, nil)

	out, err := r.Next(context.Background(), Request{
		Event: protocol.Event{Type: "git_repo_sync.completed", Payload: map[string]any{}},
		// SourceContext intentionally nil
	})
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("dispatch count = %d, want 0 (absent context against context.* predicate)", len(out))
	}
}

func TestCompileSpecsRejectsHookContextPredicate(t *testing.T) {
	// Context is never available at hook time (the dispatcher short-circuits
	// hooks for context-bearing jobs), so a context.* predicate on an on-hook:
	// trigger can never match. It must be rejected at load, not silently
	// dead-route at runtime.
	_, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:   "page-on-high-severity",
		OnHook: "job.failed",
		If: &conditions.Condition{
			Path:  "context.severity",
			Op:    conditions.OpEq,
			Value: "high",
		},
		Steps: []dsl.StepSpec{{ID: "page", Uses: "pagerduty"}},
	}})
	if err == nil {
		t.Fatalf("expected compile error for on-hook trigger predicate referencing context.*")
	}
	if !strings.Contains(err.Error(), "context is not available at hook time") {
		t.Fatalf("error = %v, want hook-context rejection message", err)
	}

	// A payload.* predicate on the same hook is fine — context-free.
	if _, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:   "page-on-high-severity",
		OnHook: "job.failed",
		If: &conditions.Condition{
			Path:  "payload.severity",
			Op:    conditions.OpEq,
			Value: "high",
		},
		Steps: []dsl.StepSpec{{ID: "page", Uses: "pagerduty"}},
	}}); err != nil {
		t.Fatalf("payload predicate on hook should compile: %v", err)
	}
}

func TestCompileSpecsFromPluginAffectsFingerprint(t *testing.T) {
	specA := []dsl.PipelineSpec{{
		Name:   "p",
		OnHook: "job.failed",
		Steps:  []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}}
	specB := []dsl.PipelineSpec{{
		Name:       "p",
		OnHook:     "job.failed",
		FromPlugin: "claude_harvest",
		Steps:      []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}}
	a, err := dsl.CompileSpecs(specA)
	if err != nil {
		t.Fatalf("CompileSpecs A: %v", err)
	}
	b, err := dsl.CompileSpecs(specB)
	if err != nil {
		t.Fatalf("CompileSpecs B: %v", err)
	}
	if a.Pipelines["p"].Fingerprint == b.Pipelines["p"].Fingerprint {
		t.Fatalf("fingerprint should differ when from_plugin: changes (a=%s b=%s)",
			a.Pipelines["p"].Fingerprint, b.Pipelines["p"].Fingerprint)
	}
}

func TestCompileSpecsFromPluginFingerprintStable(t *testing.T) {
	spec := []dsl.PipelineSpec{{
		Name:       "p",
		OnHook:     "job.failed",
		FromPlugin: "claude_harvest",
		Steps:      []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}}
	a, err := dsl.CompileSpecs(spec)
	if err != nil {
		t.Fatalf("CompileSpecs A: %v", err)
	}
	b, err := dsl.CompileSpecs(spec)
	if err != nil {
		t.Fatalf("CompileSpecs B: %v", err)
	}
	if a.Pipelines["p"].Fingerprint != b.Pipelines["p"].Fingerprint {
		t.Fatalf("fingerprint must be stable across compile runs (a=%s b=%s)",
			a.Pipelines["p"].Fingerprint, b.Pipelines["p"].Fingerprint)
	}
}

func TestCompiledRouteSourcePluginPropagatesToCompiledRoutes(t *testing.T) {
	set, err := dsl.CompileSpecs([]dsl.PipelineSpec{{
		Name:       "p",
		OnHook:     "job.failed",
		FromPlugin: "claude_harvest",
		Steps:      []dsl.StepSpec{{ID: "s", Uses: "p"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	pipeline := set.Pipelines["p"]
	if pipeline.FromPlugin != "claude_harvest" {
		t.Fatalf("Pipeline.FromPlugin = %q, want %q", pipeline.FromPlugin, "claude_harvest")
	}
	var sawEntry bool
	for _, route := range pipeline.CompiledRoutes {
		if route.Source.HookSignal != "" {
			sawEntry = true
			if route.Source.SourcePlugin != "claude_harvest" {
				t.Fatalf("entry route source plugin = %q, want %q",
					route.Source.SourcePlugin, "claude_harvest")
			}
		}
	}
	if !sawEntry {
		t.Fatalf("expected at least one hook entry route in compiled manifest")
	}
}
