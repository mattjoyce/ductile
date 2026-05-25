package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
)

// topologyFixture builds a registry + router pair that exercises every
// graph shape the console cares about:
//
//   - "a.out": produced by A.poll, consumed by B.handle (normal edge)
//   - "b.out": produced by B.handle, consumed by A.poll (closes a cycle A→B→A)
//   - "external.tick": consumed by A.poll, NOT produced by any plugin
//     (dead-end input — no producer in current config)
//   - "orphan.signal": produced by C.poll, NOT consumed by anything
//     (orphan output — no consumer in current config)
//   - "deadend.signal": consumed by A.poll via pipeline p3, NOT produced
//     by anything (second dead-end fixture, distinct pipeline)
//
// Topology endpoint must surface all five so the console can detect
// orphans, dead-ends, and cycles without ductile-side analysis.
func topologyFixture() (*mockRegistry, *mockRouter) {
	reg := &mockRegistry{plugins: map[string]*plugin.Plugin{
		"A": {
			Name:    "A",
			Version: "1.0.0",
			Commands: []plugin.Command{{
				Name: "poll",
				Type: plugin.CommandTypeRead,
				Values: &plugin.Values{
					Emit: []plugin.EmittedValues{{Event: "a.out"}},
				},
			}},
		},
		"B": {
			Name:    "B",
			Version: "1.0.0",
			Commands: []plugin.Command{{
				Name: "handle",
				Type: plugin.CommandTypeWrite,
				Values: &plugin.Values{
					Emit: []plugin.EmittedValues{{Event: "b.out"}},
				},
			}},
		},
		"C": {
			Name:    "C",
			Version: "1.0.0",
			Commands: []plugin.Command{{
				Name: "poll",
				Type: plugin.CommandTypeRead,
				Values: &plugin.Values{
					Emit: []plugin.EmittedValues{{Event: "orphan.signal"}},
				},
			}},
		},
	}}

	// Pipelines:
	//   p1: external.tick → A.poll → (A emits a.out) → B.handle
	//   p2: b.out (entry trigger) → A.poll      (creates the cycle)
	//   p3: deadend.signal (entry trigger) → A.poll  (dead-end input)
	routes := map[string][]dsl.CompiledRoute{
		"p1": {
			{
				ID:       "p1-entry",
				Pipeline: "p1",
				Source:   dsl.CompiledRouteSource{Trigger: "external.tick", Pipeline: "p1"},
				Destination: dsl.CompiledRouteDestination{
					Kind:    dsl.CompiledRouteDestinationUses,
					Plugin:  "A",
					Command: "poll",
				},
			},
			{
				ID:       "p1-transition",
				Pipeline: "p1",
				Source:   dsl.CompiledRouteSource{EventType: "a.out", Pipeline: "p1"},
				Destination: dsl.CompiledRouteDestination{
					Kind:    dsl.CompiledRouteDestinationUses,
					Plugin:  "B",
					Command: "handle",
				},
			},
		},
		"p2": {
			{
				ID:       "p2-entry",
				Pipeline: "p2",
				Source:   dsl.CompiledRouteSource{Trigger: "b.out", Pipeline: "p2"},
				Destination: dsl.CompiledRouteDestination{
					Kind:    dsl.CompiledRouteDestinationUses,
					Plugin:  "A",
					Command: "poll",
				},
			},
		},
		"p3": {
			{
				ID:       "p3-entry",
				Pipeline: "p3",
				Source:   dsl.CompiledRouteSource{Trigger: "deadend.signal", Pipeline: "p3"},
				Destination: dsl.CompiledRouteDestination{
					Kind:    dsl.CompiledRouteDestinationUses,
					Plugin:  "A",
					Command: "poll",
				},
			},
		},
	}

	rtr := &mockRouter{
		getCompiledRoutesFunc: func(name string) []dsl.CompiledRoute {
			return routes[name]
		},
	}
	// PipelineSummary on the stock mockRouter returns nil; topology needs
	// the pipeline names to drive GetCompiledRoutes, so wrap the mock.
	rtr.pipelineSummary = []router.PipelineInfo{
		{Name: "p1", Trigger: "external.tick"},
		{Name: "p2", Trigger: "b.out"},
		{Name: "p3", Trigger: "deadend.signal"},
	}
	return reg, rtr
}

func TestHandleTopology_ShapeAndEdges(t *testing.T) {
	t.Parallel()
	reg, rtr := topologyFixture()

	db := setupTestDB(t)
	server := setupTestServerWithRouter(t, db, reg, rtr)

	req := httptest.NewRequest(http.MethodGet, "/topology", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	var resp TopologyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rr.Body.String())
	}

	// Plugins surface — all three plugins, sorted.
	gotNames := make([]string, 0, len(resp.Plugins))
	for _, p := range resp.Plugins {
		gotNames = append(gotNames, p.Name)
		if !p.Active {
			t.Errorf("plugin %q: Active = false, want true", p.Name)
		}
	}
	want := []string{"A", "B", "C"}
	if !equalStrings(gotNames, want) {
		t.Fatalf("plugin names = %v, want %v", gotNames, want)
	}

	pluginByName := map[string]TopologyPlugin{}
	for _, p := range resp.Plugins {
		pluginByName[p.Name] = p
	}

	// A: emits a.out; consumes external.tick + b.out + deadend.signal
	if got := edgeSet(pluginByName["A"].Outputs); !containsEdge(got, "a.out", "poll") {
		t.Errorf("A.Outputs missing (a.out, poll): %v", got)
	}
	for _, s := range []string{"external.tick", "b.out", "deadend.signal"} {
		if !containsEdge(edgeSet(pluginByName["A"].Inputs), s, "poll") {
			t.Errorf("A.Inputs missing (%s, poll): %v", s, pluginByName["A"].Inputs)
		}
	}

	// B: emits b.out; consumes a.out
	if !containsEdge(edgeSet(pluginByName["B"].Outputs), "b.out", "handle") {
		t.Errorf("B.Outputs missing (b.out, handle): %v", pluginByName["B"].Outputs)
	}
	if !containsEdge(edgeSet(pluginByName["B"].Inputs), "a.out", "handle") {
		t.Errorf("B.Inputs missing (a.out, handle): %v", pluginByName["B"].Inputs)
	}

	// C: emits orphan.signal only; consumes nothing
	if !containsEdge(edgeSet(pluginByName["C"].Outputs), "orphan.signal", "poll") {
		t.Errorf("C.Outputs missing (orphan.signal, poll): %v", pluginByName["C"].Outputs)
	}
	if len(pluginByName["C"].Inputs) != 0 {
		t.Errorf("C.Inputs = %v, want []", pluginByName["C"].Inputs)
	}

	// Signals inverted index
	sigByName := map[string]TopologySignal{}
	for _, s := range resp.Signals {
		sigByName[s.Name] = s
	}

	// Orphan output: C produces orphan.signal, nobody consumes it.
	orphan, ok := sigByName["orphan.signal"]
	if !ok {
		t.Fatalf("signals missing orphan.signal")
	}
	if !endpointsContain(orphan.Producers, "C", "poll") {
		t.Errorf("orphan.signal producers = %v, want C/poll", orphan.Producers)
	}
	if len(orphan.Consumers) != 0 {
		t.Errorf("orphan.signal consumers = %v, want []", orphan.Consumers)
	}

	// Dead-end input: external.tick consumed by A, produced by nobody.
	deadIn, ok := sigByName["external.tick"]
	if !ok {
		t.Fatalf("signals missing external.tick")
	}
	if len(deadIn.Producers) != 0 {
		t.Errorf("external.tick producers = %v, want []", deadIn.Producers)
	}
	if !endpointsContain(deadIn.Consumers, "A", "poll") {
		t.Errorf("external.tick consumers = %v, want A/poll", deadIn.Consumers)
	}

	// Second dead-end input fixture (distinct pipeline path).
	deadIn2, ok := sigByName["deadend.signal"]
	if !ok {
		t.Fatalf("signals missing deadend.signal")
	}
	if len(deadIn2.Producers) != 0 {
		t.Errorf("deadend.signal producers = %v, want []", deadIn2.Producers)
	}
	if !endpointsContain(deadIn2.Consumers, "A", "poll") {
		t.Errorf("deadend.signal consumers = %v, want A/poll", deadIn2.Consumers)
	}

	// Cycle: a.out (A→B) and b.out (B→A) — both must appear with the
	// expected producer/consumer pairs so a client can detect the cycle.
	aOut, ok := sigByName["a.out"]
	if !ok {
		t.Fatalf("signals missing a.out")
	}
	if !endpointsContain(aOut.Producers, "A", "poll") {
		t.Errorf("a.out producers = %v, want A/poll", aOut.Producers)
	}
	if !endpointsContain(aOut.Consumers, "B", "handle") {
		t.Errorf("a.out consumers = %v, want B/handle", aOut.Consumers)
	}
	bOut, ok := sigByName["b.out"]
	if !ok {
		t.Fatalf("signals missing b.out")
	}
	if !endpointsContain(bOut.Producers, "B", "handle") {
		t.Errorf("b.out producers = %v, want B/handle", bOut.Producers)
	}
	if !endpointsContain(bOut.Consumers, "A", "poll") {
		t.Errorf("b.out consumers = %v, want A/poll", bOut.Consumers)
	}

	// Walk the signal graph to verify the A→B→A cycle is reconstructible
	// from the response alone — the test that proves the console can do
	// cycle detection client-side from this endpoint's output.
	if !hasCycle(sigByName) {
		t.Errorf("expected cycle detectable from response, none found")
	}
}

func TestHandleTopology_RequiresAuth(t *testing.T) {
	t.Parallel()
	reg, rtr := topologyFixture()
	db := setupTestDB(t)
	server := setupTestServerWithRouter(t, db, reg, rtr)

	req := httptest.NewRequest(http.MethodGet, "/topology", nil)
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleTopology_EmptyRegistryReturnsEmptyArrays(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	server := setupTestServer(t, db, &mockRegistry{})

	req := httptest.NewRequest(http.MethodGet, "/topology", nil)
	req.Header.Set("Authorization", "Bearer test-key-123")
	rr := httptest.NewRecorder()
	server.setupRoutes().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp TopologyResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Plugins) != 0 || len(resp.Signals) != 0 {
		t.Errorf("expected empty graph; plugins=%v signals=%v", resp.Plugins, resp.Signals)
	}
}

// equalStrings is a small helper so the test reads cleanly without
// pulling cmp/slices for one assertion.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func edgeSet(edges []TopologyEdge) []TopologyEdge { return edges }

func containsEdge(edges []TopologyEdge, signal, command string) bool {
	for _, e := range edges {
		if e.Signal == signal && e.Command == command {
			return true
		}
	}
	return false
}

func endpointsContain(eps []TopologyEndpoint, plugin, command string) bool {
	for _, e := range eps {
		if e.Plugin == plugin && e.Command == command {
			return true
		}
	}
	return false
}

// hasCycle does a tiny DFS on the producer→consumer projection of the
// signal index. Edge: from each producer plugin, follow every signal it
// produces to each consumer plugin. If we revisit a node already on the
// active stack, we've found a cycle.
func hasCycle(signals map[string]TopologySignal) bool {
	// Build adjacency: plugin -> set of downstream plugins.
	adj := map[string]map[string]struct{}{}
	for _, sig := range signals {
		for _, prod := range sig.Producers {
			for _, cons := range sig.Consumers {
				if _, ok := adj[prod.Plugin]; !ok {
					adj[prod.Plugin] = map[string]struct{}{}
				}
				adj[prod.Plugin][cons.Plugin] = struct{}{}
			}
		}
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		for nb := range adj[n] {
			switch color[nb] {
			case gray:
				return true
			case white:
				if dfs(nb) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for n := range adj {
		if color[n] == white {
			if dfs(n) {
				return true
			}
		}
	}
	return false
}
