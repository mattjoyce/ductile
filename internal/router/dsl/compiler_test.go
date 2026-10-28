package dsl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjoyce/ductile/internal/router/conditions"
)

func TestCompileSpecsValidNestedPipeline(t *testing.T) {
	specs := []PipelineSpec{
		{
			Name: "process-audio",
			On:   "internal.process_audio",
			Steps: []StepSpec{
				{ID: "transcribe", Uses: "transcriber"},
			},
		},
		{
			Name: "wisdom-chain",
			On:   "discord.video_link_received",
			Steps: []StepSpec{
				{ID: "downloader", Uses: "yt-dlp-plugin"},
				{ID: "processing", Call: "process-audio"},
				{
					ID: "delivery",
					Split: []StepSpec{
						{ID: "notify", Uses: "discord-notifier"},
						{
							Steps: []StepSpec{
								{ID: "archive", Uses: "s3-archiver"},
								{ID: "index", Uses: "db-indexer"},
							},
						},
					},
				},
			},
		},
	}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}

	p := set.Pipelines["wisdom-chain"]
	if p == nil {
		t.Fatalf("compiled pipeline wisdom-chain not found")
		return
	}
	if p.Trigger != "discord.video_link_received" {
		t.Fatalf("trigger = %q, want %q", p.Trigger, "discord.video_link_received")
	}
	if len(p.Nodes) != 5 {
		t.Fatalf("node count = %d, want 5", len(p.Nodes))
	}
	if !hasEdge(p.Edges, "downloader", "processing") {
		t.Fatalf("expected edge downloader -> processing")
	}
	if !hasEdge(p.Edges, "processing", "notify") {
		t.Fatalf("expected edge processing -> notify")
	}
	if !hasEdge(p.Edges, "processing", "archive") {
		t.Fatalf("expected edge processing -> archive")
	}
	if !hasEdge(p.Edges, "archive", "index") {
		t.Fatalf("expected edge archive -> index")
	}
	if len(p.EntryNodeIDs) != 1 || p.EntryNodeIDs[0] != "downloader" {
		t.Fatalf("entry nodes = %v, want [downloader]", p.EntryNodeIDs)
	}
	if len(p.TerminalNodeIDs) != 2 || p.TerminalNodeIDs[0] != "index" || p.TerminalNodeIDs[1] != "notify" {
		t.Fatalf("terminal nodes = %v, want [index notify]", p.TerminalNodeIDs)
	}
	if len(p.CalledPipelines) != 1 || p.CalledPipelines[0] != "process-audio" {
		t.Fatalf("called pipelines = %v, want [process-audio]", p.CalledPipelines)
	}
	if !strings.HasPrefix(p.Fingerprint, "sha256:") {
		t.Fatalf("fingerprint = %q, want prefix sha256:", p.Fingerprint)
	}

	// Recompile to confirm deterministic pipeline fingerprint.
	set2, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() second run error = %v", err)
	}
	if p.Fingerprint != set2.Pipelines["wisdom-chain"].Fingerprint {
		t.Fatalf("fingerprint changed across compile runs: %q vs %q", p.Fingerprint, set2.Pipelines["wisdom-chain"].Fingerprint)
	}
}

func TestCompileSpecsRejectsPipelineCallCycle(t *testing.T) {
	specs := []PipelineSpec{
		{
			Name: "a",
			On:   "a.start",
			Steps: []StepSpec{
				{ID: "a1", Call: "b"},
			},
		},
		{
			Name: "b",
			On:   "b.start",
			Steps: []StepSpec{
				{ID: "b1", Call: "a"},
			},
		},
	}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected cycle detection error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestCompileSpecsRejectsUnknownCallTarget(t *testing.T) {
	specs := []PipelineSpec{
		{
			Name: "a",
			On:   "a.start",
			Steps: []StepSpec{
				{ID: "a1", Call: "missing"},
			},
		},
	}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected unknown call target error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown pipeline") {
		t.Fatalf("expected unknown pipeline error, got %v", err)
	}
}

func TestCompileSpecsAcceptsStructuredIfCondition(t *testing.T) {
	specs := []PipelineSpec{{
		Name: "conditional",
		On:   "event.start",
		Steps: []StepSpec{{
			ID:   "step_a",
			Uses: "plugin-a",
			If: &conditions.Condition{
				All: []conditions.Condition{
					{Path: "payload.kind", Op: conditions.OpContains, Value: "vid"},
					{Path: "context.origin_user", Op: conditions.OpExists},
				},
			},
		}},
	}}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}
	switchNode, ok := set.Pipelines["conditional"].Nodes["step_a__switch"]
	if !ok {
		t.Fatalf("expected compiled switch node")
	}
	if switchNode.Kind != NodeKindSwitch {
		t.Fatalf("switch node kind = %q, want %q", switchNode.Kind, NodeKindSwitch)
	}
	if switchNode.Condition == nil {
		t.Fatalf("expected compiled switch condition")
	}
}

func TestCompileSpecsAcceptsRelayStep(t *testing.T) {
	specs := []PipelineSpec{{
		Name: "ship-backup",
		On:   "backup.archive.created",
		Steps: []StepSpec{{
			ID: "relay-to-lab",
			Relay: &RelaySpec{
				To:        "lab",
				Event:     "backup.ready",
				DedupeKey: "payload.archive_id",
				With: map[string]string{
					"archive_id":   "payload.archive_id",
					"archive_path": "payload.archive_path",
				},
				Baggage: &BaggageSpec{
					Mappings: map[string]string{"trace_id": "context.trace_id"},
				},
			},
		}},
	}}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}
	node := set.Pipelines["ship-backup"].Nodes["relay-to-lab"]
	if node.Kind != NodeKindRelay {
		t.Fatalf("relay node kind = %q, want %q", node.Kind, NodeKindRelay)
	}
	if node.Relay == nil || node.Relay.To != "lab" || node.Relay.Event != "backup.ready" {
		t.Fatalf("relay spec = %+v", node.Relay)
	}
	routes := set.Pipelines["ship-backup"].CompiledRoutes
	if len(routes) == 0 {
		t.Fatalf("expected compiled routes")
	}
	if routes[0].Destination.Plugin != "core.relay" || routes[0].Destination.Command != "handle" {
		t.Fatalf("relay route destination = %+v, want core.relay handle", routes[0].Destination)
	}
}

func TestCompileSpecsRejectsWithOnCallStep(t *testing.T) {
	specs := []PipelineSpec{{
		Name: "invalid-with",
		On:   "event.start",
		Steps: []StepSpec{{
			ID:   "call-step",
			Call: "other-pipeline",
			With: map[string]string{"message": "{payload.message}"},
		}},
	}}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected invalid with usage error")
	}
	if !strings.Contains(err.Error(), "with is only supported on uses steps") {
		t.Fatalf("error = %v, want with usage validation", err)
	}
}

func TestCompileSpecsAcceptsBaggageOnUsesStep(t *testing.T) {
	specs := []PipelineSpec{{
		Name: "explicit-baggage",
		On:   "event.start",
		Steps: []StepSpec{{
			ID:   "transcribe",
			Uses: "whisper",
			Baggage: &BaggageSpec{
				Mappings: map[string]string{
					"summary.text":     "payload.text",
					"summary.language": "payload.language",
				},
				Bulk: &BaggageBulkSpec{
					From:      "payload.metadata",
					Namespace: "whisper",
				},
			},
		}},
	}}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}

	baggage := set.Pipelines["explicit-baggage"].Nodes["transcribe"].Baggage
	if baggage == nil {
		t.Fatalf("compiled node baggage = nil")
		return
	}
	if baggage.Mappings["summary.text"] != "payload.text" {
		t.Fatalf("summary.text mapping = %q, want payload.text", baggage.Mappings["summary.text"])
	}
	if baggage.Bulk == nil {
		t.Fatalf("compiled bulk baggage = nil")
	}
	if baggage.Bulk.From != "payload.metadata" || baggage.Bulk.Namespace != "whisper" {
		t.Fatalf("bulk baggage = %+v, want payload.metadata under whisper", baggage.Bulk)
	}

	specs[0].Steps[0].Baggage.Mappings["summary.text"] = "payload.mutated"
	if baggage.Mappings["summary.text"] != "payload.text" {
		t.Fatalf("compiled baggage changed after source mutation: %q", baggage.Mappings["summary.text"])
	}
}

func TestLoadFileParsesBaggageYAML(t *testing.T) {
	configDir := t.TempDir()
	pipelineYAML := `pipelines:
  - name: explicit-baggage
    on: event.start
    steps:
      - id: transcribe
        uses: whisper
        baggage:
          whisper.text: payload.text
          whisper.language: payload.language
          from: payload.metadata
          namespace: whisper.meta
`
	path := filepath.Join(configDir, "pipeline.yaml")
	if err := os.WriteFile(path, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipeline.yaml): %v", err)
	}

	fileSpec, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	baggage := fileSpec.Pipelines[0].Steps[0].Baggage
	if baggage == nil {
		t.Fatalf("loaded baggage = nil")
		return
	}
	if baggage.Mappings["whisper.text"] != "payload.text" {
		t.Fatalf("whisper.text mapping = %q, want payload.text", baggage.Mappings["whisper.text"])
	}
	if baggage.Bulk == nil || baggage.Bulk.From != "payload.metadata" || baggage.Bulk.Namespace != "whisper.meta" {
		t.Fatalf("bulk baggage = %+v, want payload.metadata under whisper.meta", baggage.Bulk)
	}
}

func TestCompileSpecsRejectsBaggageOnCallStep(t *testing.T) {
	specs := []PipelineSpec{{
		Name: "invalid-baggage",
		On:   "event.start",
		Steps: []StepSpec{{
			ID:      "call-step",
			Call:    "other-pipeline",
			Baggage: &BaggageSpec{Mappings: map[string]string{"summary.text": "payload.text"}},
		}},
	}}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected invalid baggage usage error")
	}
	if !strings.Contains(err.Error(), "baggage is only supported on uses steps") {
		t.Fatalf("error = %v, want baggage usage validation", err)
	}
}

func TestCompileSpecsRejectsInvalidBaggage(t *testing.T) {
	tests := []struct {
		name    string
		baggage *BaggageSpec
		want    string
	}{
		{
			name:    "empty path segment",
			baggage: &BaggageSpec{Mappings: map[string]string{"summary..text": "payload.text"}},
			want:    "path segments must be non-empty",
		},
		{
			name:    "digit starts path segment",
			baggage: &BaggageSpec{Mappings: map[string]string{"summary.1text": "payload.text"}},
			want:    "must not start with a digit",
		},
		{
			name:    "empty expression",
			baggage: &BaggageSpec{Mappings: map[string]string{"summary.text": ""}},
			want:    "expression must be non-empty",
		},
		{
			name:    "bulk from outside payload",
			baggage: &BaggageSpec{Bulk: &BaggageBulkSpec{From: "context.summary", Namespace: "summary"}},
			want:    "must reference payload",
		},
		{
			name:    "invalid namespace",
			baggage: &BaggageSpec{Bulk: &BaggageBulkSpec{From: "payload", Namespace: "summary-text"}},
			want:    "invalid baggage namespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs := []PipelineSpec{{
				Name: "invalid-baggage",
				On:   "event.start",
				Steps: []StepSpec{{
					ID:      "step",
					Uses:    "plugin-a",
					Baggage: tt.baggage,
				}},
			}}

			_, err := CompileSpecs(specs)
			if err == nil {
				t.Fatalf("expected invalid baggage error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadFileRejectsNamespaceWithoutBaggageFrom(t *testing.T) {
	configDir := t.TempDir()
	pipelineYAML := `pipelines:
  - name: explicit-baggage
    on: event.start
    steps:
      - id: transcribe
        uses: whisper
        baggage:
          namespace: whisper
`
	path := filepath.Join(configDir, "pipeline.yaml")
	if err := os.WriteFile(path, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(pipeline.yaml): %v", err)
	}

	_, err := LoadFile(path)
	if err == nil {
		t.Fatalf("expected namespace without from parse error")
	}
	if !strings.Contains(err.Error(), "namespace requires from") {
		t.Fatalf("error = %v, want namespace requires from", err)
	}
}

func TestCompileSpecsRejectsInvalidIfCondition(t *testing.T) {
	specs := []PipelineSpec{{
		Name: "conditional",
		On:   "event.start",
		Steps: []StepSpec{{
			ID:   "step_a",
			Uses: "plugin-a",
			If:   &conditions.Condition{Path: "state.flag", Op: conditions.OpEq, Value: true},
		}},
	}}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected invalid if condition error")
	}
	if !strings.Contains(err.Error(), "unsupported root") {
		t.Fatalf("error = %v, want unsupported root", err)
	}
}

func TestLoadAndCompileFilesLoadsMultipleYAMLFiles(t *testing.T) {
	configDir := t.TempDir()

	rootYAML := `pipelines:
  - name: wisdom-chain
    on: discord.video_link_received
    steps:
      - id: start
        call: process-audio
`
	processYAML := `pipelines:
  - name: process-audio
    on: internal.process_audio
    steps:
      - id: transcribe
        uses: transcriber
`

	rootPath := filepath.Join(configDir, "01-root.yaml")
	processPath := filepath.Join(configDir, "02-process.yaml")
	if err := os.WriteFile(rootPath, []byte(rootYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(01-root.yaml): %v", err)
	}
	if err := os.WriteFile(processPath, []byte(processYAML), 0o644); err != nil {
		t.Fatalf("WriteFile(02-process.yaml): %v", err)
	}

	set, err := LoadAndCompileFiles([]string{rootPath, processPath})
	if err != nil {
		t.Fatalf("LoadAndCompileFiles() error = %v", err)
	}
	if len(set.Pipelines) != 2 {
		t.Fatalf("pipeline count = %d, want 2", len(set.Pipelines))
	}
}

func TestCompileSpecsOnHookAccepted(t *testing.T) {
	specs := []PipelineSpec{{
		Name:   "notify-on-done",
		OnHook: "job.completed",
		Steps:  []StepSpec{{ID: "notify", Uses: "discord-notifier"}},
	}}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v, want nil", err)
	}

	p := set.Pipelines["notify-on-done"]
	if p == nil {
		t.Fatalf("pipeline not found")
		return
	}
	if !p.IsHook {
		t.Fatalf("IsHook = false, want true")
	}
	if p.Trigger != "job.completed" {
		t.Fatalf("Trigger = %q, want %q", p.Trigger, "job.completed")
	}
}

func TestCompileSpecsOnHookAndOnMutuallyExclusive(t *testing.T) {
	specs := []PipelineSpec{{
		Name:   "bad-pipeline",
		On:     "some.event",
		OnHook: "job.completed",
		Steps:  []StepSpec{{ID: "step", Uses: "plugin-a"}},
	}}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected error for on+on-hook, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v, want mutually exclusive", err)
	}
}

func TestCompileSpecsRequiresOnOrOnHook(t *testing.T) {
	specs := []PipelineSpec{{
		Name:  "missing-trigger",
		Steps: []StepSpec{{ID: "step", Uses: "plugin-a"}},
	}}

	_, err := CompileSpecs(specs)
	if err == nil {
		t.Fatalf("expected error when neither on nor on-hook set, got nil")
	}
	if !strings.Contains(err.Error(), "on or on-hook is required") {
		t.Fatalf("error = %v, want 'on or on-hook is required'", err)
	}
}

func TestCompileSpecsRegularPipelineIsNotHook(t *testing.T) {
	specs := []PipelineSpec{{
		Name:  "regular",
		On:    "plugin.event",
		Steps: []StepSpec{{ID: "step", Uses: "plugin-a"}},
	}}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}
	p := set.Pipelines["regular"]
	if p.IsHook {
		t.Fatalf("IsHook = true for regular pipeline, want false")
	}
}

func TestCompileSpecsEmitsCompiledRoutesManifest(t *testing.T) {
	specs := []PipelineSpec{
		{
			Name: "process-audio",
			On:   "internal.process_audio",
			Steps: []StepSpec{
				{ID: "transcribe", Uses: "transcriber"},
			},
		},
		{
			Name: "wisdom-chain",
			On:   "discord.video_link_received",
			Steps: []StepSpec{
				{ID: "downloader", Uses: "yt-dlp-plugin"},
				{ID: "processing", Call: "process-audio"},
				{
					ID: "delivery",
					Split: []StepSpec{
						{ID: "notify", Uses: "discord-notifier"},
						{
							Steps: []StepSpec{
								{ID: "archive", Uses: "s3-archiver"},
								{ID: "index", Uses: "db-indexer"},
							},
						},
					},
				},
			},
		},
	}

	set, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}

	p := set.Pipelines["wisdom-chain"]
	if p == nil {
		t.Fatalf("compiled pipeline wisdom-chain not found")
		return
	}
	if len(p.CompiledRoutes) != 7 {
		t.Fatalf("compiled route count = %d, want 7", len(p.CompiledRoutes))
	}

	assertCompiledRoute(t, p.CompiledRoutes[0], CompiledRoute{
		ID:       "edge:archive->index",
		Pipeline: "wisdom-chain",
		Source: CompiledRouteSource{
			Pipeline: "wisdom-chain",
			StepID:   "archive",
		},
		Destination: CompiledRouteDestination{
			Kind:    CompiledRouteDestinationUses,
			StepID:  "index",
			Plugin:  "db-indexer",
			Command: "handle",
		},
	})
	assertCompiledRoute(t, p.CompiledRoutes[1], CompiledRoute{
		ID:       "edge:downloader->processing",
		Pipeline: "wisdom-chain",
		Source: CompiledRouteSource{
			Pipeline: "wisdom-chain",
			StepID:   "downloader",
		},
		Destination: CompiledRouteDestination{
			Kind:         CompiledRouteDestinationCall,
			StepID:       "processing",
			CallPipeline: "process-audio",
		},
	})
	assertCompiledRoute(t, p.CompiledRoutes[4], CompiledRoute{
		ID:       "entry:downloader",
		Pipeline: "wisdom-chain",
		Source: CompiledRouteSource{
			Trigger: "discord.video_link_received",
		},
		Destination: CompiledRouteDestination{
			Kind:    CompiledRouteDestinationUses,
			StepID:  "downloader",
			Plugin:  "yt-dlp-plugin",
			Command: "handle",
		},
	})
	assertCompiledRoute(t, p.CompiledRoutes[5], CompiledRoute{
		ID:       "terminal:index",
		Pipeline: "wisdom-chain",
		Source: CompiledRouteSource{
			Pipeline: "wisdom-chain",
			StepID:   "index",
		},
		Destination: CompiledRouteDestination{
			Kind: CompiledRouteDestinationTerminal,
		},
	})
	assertCompiledRoute(t, p.CompiledRoutes[6], CompiledRoute{
		ID:       "terminal:notify",
		Pipeline: "wisdom-chain",
		Source: CompiledRouteSource{
			Pipeline: "wisdom-chain",
			StepID:   "notify",
		},
		Destination: CompiledRouteDestination{
			Kind: CompiledRouteDestinationTerminal,
		},
	})

	set2, err := CompileSpecs(specs)
	if err != nil {
		t.Fatalf("CompileSpecs() second run error = %v", err)
	}
	if p.Fingerprint != set2.Pipelines["wisdom-chain"].Fingerprint {
		t.Fatalf("fingerprint changed across compile runs: %q vs %q", p.Fingerprint, set2.Pipelines["wisdom-chain"].Fingerprint)
	}
}

func TestCompileSpecsEmitsCompiledRoutesForHookPipeline(t *testing.T) {
	set, err := CompileSpecs([]PipelineSpec{{
		Name:   "notify-on-complete",
		OnHook: "job.completed",
		Steps:  []StepSpec{{ID: "notify", Uses: "discord-notifier"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs() error = %v", err)
	}

	p := set.Pipelines["notify-on-complete"]
	if p == nil {
		t.Fatalf("pipeline not found")
		return
	}
	if len(p.CompiledRoutes) != 2 {
		t.Fatalf("compiled route count = %d, want 2", len(p.CompiledRoutes))
	}
	assertCompiledRoute(t, p.CompiledRoutes[0], CompiledRoute{
		ID:       "entry:notify",
		Pipeline: "notify-on-complete",
		Source: CompiledRouteSource{
			HookSignal: "job.completed",
		},
		Destination: CompiledRouteDestination{
			Kind:    CompiledRouteDestinationUses,
			StepID:  "notify",
			Plugin:  "discord-notifier",
			Command: "handle",
		},
	})
	assertCompiledRoute(t, p.CompiledRoutes[1], CompiledRoute{
		ID:       "terminal:notify",
		Pipeline: "notify-on-complete",
		Source: CompiledRouteSource{
			Pipeline: "notify-on-complete",
			StepID:   "notify",
		},
		Destination: CompiledRouteDestination{
			Kind: CompiledRouteDestinationTerminal,
		},
	})
}

func hasEdge(edges []Edge, from, to string) bool {
	for _, edge := range edges {
		if edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

func TestLoadFileParsesPipelineLevelIfAndMaxDepth(t *testing.T) {
	configDir := t.TempDir()
	pipelineYAML := `pipelines:
  - name: filtered-trigger
    on: data.change.garmin
    if:
      path: payload.kind
      op: eq
      value: workout
    max_depth: 4
    steps:
      - id: consume
        uses: summary
`
	path := filepath.Join(configDir, "pipeline.yaml")
	if err := os.WriteFile(path, []byte(pipelineYAML), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fileSpec, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	spec := fileSpec.Pipelines[0]
	if spec.If == nil {
		t.Fatalf("pipeline.If = nil, want parsed predicate")
	}
	if spec.If.Path != "payload.kind" || spec.If.Op != conditions.OpEq || spec.If.Value != "workout" {
		t.Fatalf("pipeline.If = %+v, want path/op/value parsed", spec.If)
	}
	if spec.MaxDepth == nil || *spec.MaxDepth != 4 {
		t.Fatalf("MaxDepth = %v, want *4", spec.MaxDepth)
	}
}

func TestCompileSpecsStampsPipelineLevelIfOnEntryRoutes(t *testing.T) {
	cond := &conditions.Condition{
		Path:  "payload.run",
		Op:    conditions.OpEq,
		Value: true,
	}
	set, err := CompileSpecs([]PipelineSpec{{
		Name:  "guarded",
		On:    "x",
		If:    cond,
		Steps: []StepSpec{{ID: "s", Uses: "p"}},
	}})
	if err != nil {
		t.Fatalf("CompileSpecs: %v", err)
	}
	p := set.Pipelines["guarded"]
	if p.If == nil {
		t.Fatalf("compiled pipeline missing If")
	}
	entryFound := false
	for _, route := range p.CompiledRoutes {
		if route.Source.Trigger != "x" {
			continue
		}
		if route.Source.If == nil {
			t.Fatalf("entry route missing Source.If: %+v", route)
		}
		if route.Source.If.Path != "payload.run" || route.Source.If.Op != conditions.OpEq {
			t.Fatalf("entry route If = %+v", route.Source.If)
		}
		entryFound = true
	}
	if !entryFound {
		t.Fatalf("no entry route found in compiled set")
	}
}

func TestCompileSpecsRejectsInvalidPipelineLevelIf(t *testing.T) {
	_, err := CompileSpecs([]PipelineSpec{{
		Name: "bad-if",
		On:   "x",
		If: &conditions.Condition{
			// Missing op + value while having a path: invalid.
			Path: "payload.k",
		},
		Steps: []StepSpec{{ID: "s", Uses: "p"}},
	}})
	if err == nil {
		t.Fatal("expected validation failure for malformed pipeline-level if")
	}
	if !strings.Contains(err.Error(), "if:") {
		t.Fatalf("error %v should be tagged with 'if:'", err)
	}
}

func TestCompileSpecsPipelineFingerprintIncludesIfPredicate(t *testing.T) {
	withIf := []PipelineSpec{{
		Name:  "p",
		On:    "x",
		If:    &conditions.Condition{Path: "payload.k", Op: conditions.OpEq, Value: "a"},
		Steps: []StepSpec{{ID: "s", Uses: "plug"}},
	}}
	withoutIf := []PipelineSpec{{
		Name:  "p",
		On:    "x",
		Steps: []StepSpec{{ID: "s", Uses: "plug"}},
	}}
	a, err := CompileSpecs(withIf)
	if err != nil {
		t.Fatalf("withIf: %v", err)
	}
	b, err := CompileSpecs(withoutIf)
	if err != nil {
		t.Fatalf("withoutIf: %v", err)
	}
	if a.Pipelines["p"].Fingerprint == b.Pipelines["p"].Fingerprint {
		t.Fatalf("fingerprint unchanged after adding pipeline-level If — expected divergence (route fingerprint must include predicate)")
	}
}

func assertCompiledRoute(t *testing.T, got, want CompiledRoute) {
	t.Helper()
	if got.ID != want.ID {
		t.Fatalf("route id = %q, want %q", got.ID, want.ID)
	}
	if got.Pipeline != want.Pipeline {
		t.Fatalf("route pipeline = %q, want %q", got.Pipeline, want.Pipeline)
	}
	if want.Source.DepthLT == 0 {
		want.Source.DepthLT = got.Source.DepthLT
	}
	if got.Source != want.Source {
		t.Fatalf("route source = %+v, want %+v", got.Source, want.Source)
	}
	if got.Destination != want.Destination {
		t.Fatalf("route destination = %+v, want %+v", got.Destination, want.Destination)
	}
}
