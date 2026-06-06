package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/mattjoyce/ductile/internal/router/dsl"
)

// TopologyResponse is returned by GET /topology — the full plugin↔signal
// graph. Console consumers render plugins as nodes and use the inverted
// signal index for O(1) "who produces X?" / "who consumes Y?" lookups
// behind the hover-highlight interaction.
type TopologyResponse struct {
	Version    string           `json:"version,omitempty"`
	CapturedAt time.Time        `json:"captured_at"`
	Plugins    []TopologyPlugin `json:"plugins"`
	Signals    []TopologySignal `json:"signals"`
}

// TopologyPlugin is one node in the graph: a plugin with its declared
// signal inputs and outputs aggregated across all commands and routes.
type TopologyPlugin struct {
	Name    string         `json:"name"`
	Version string         `json:"version,omitempty"`
	Active  bool           `json:"active"`
	Inputs  []TopologyEdge `json:"inputs"`
	Outputs []TopologyEdge `json:"outputs"`
}

// TopologyEdge attaches a signal to the plugin's command on either side.
// Command is omitted on entries derived from pipeline trigger routes when
// the routing layer did not bind a specific command.
type TopologyEdge struct {
	Signal  string `json:"signal"`
	Command string `json:"command,omitempty"`
}

// TopologySignal is one edge label in the graph, inverted into the
// (producers, consumers) view the console needs for hover highlighting.
type TopologySignal struct {
	Name      string             `json:"name"`
	Producers []TopologyEndpoint `json:"producers"`
	Consumers []TopologyEndpoint `json:"consumers"`
}

// TopologyEndpoint names one end of a signal edge — a (plugin, command) tuple.
type TopologyEndpoint struct {
	Plugin  string `json:"plugin"`
	Command string `json:"command,omitempty"`
}

// edgeKey is the dedupe key for per-plugin signal edges.
type edgeKey struct {
	signal  string
	command string
}

// handleTopology handles GET /topology.
func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	plugins := s.registry.All()

	pluginOutputs := map[string]map[edgeKey]struct{}{}
	pluginInputs := map[string]map[edgeKey]struct{}{}
	signalProducers := map[string]map[TopologyEndpoint]struct{}{}
	signalConsumers := map[string]map[TopologyEndpoint]struct{}{}

	// Outputs: walk plugin manifests for declared Values.Emit entries.
	for name, p := range plugins {
		for _, cmd := range p.Commands {
			if cmd.Values == nil {
				continue
			}
			for _, emit := range cmd.Values.Emit {
				if emit.Event == "" {
					continue
				}
				recordEdge(pluginOutputs, name, edgeKey{signal: emit.Event, command: cmd.Name})
				recordEndpoint(signalProducers, emit.Event, TopologyEndpoint{Plugin: name, Command: cmd.Name})
			}
		}
	}

	// Inputs: walk compiled router routes. Source.EventType covers transition
	// routes; Source.Trigger covers entry routes. A route's Destination.Plugin
	// is the consuming plugin when Kind == "uses".
	for _, pi := range s.router.PipelineSummary() {
		for _, route := range s.router.GetCompiledRoutes(pi.Name) {
			if route.Destination.Kind != dsl.CompiledRouteDestinationUses {
				continue
			}
			if route.Destination.Plugin == "" {
				continue
			}
			signal := route.Source.EventType
			if signal == "" {
				signal = route.Source.Trigger
			}
			if signal == "" {
				continue
			}
			plug := route.Destination.Plugin
			cmd := route.Destination.Command
			recordEdge(pluginInputs, plug, edgeKey{signal: signal, command: cmd})
			recordEndpoint(signalConsumers, signal, TopologyEndpoint{Plugin: plug, Command: cmd})
		}
	}

	// Plugin list = union of registered plugins and plugins referenced by
	// routes (the latter may be unloaded but still part of the configured
	// topology — surface them as active:false so the console can grey them).
	nameSet := map[string]struct{}{}
	for name := range plugins {
		nameSet[name] = struct{}{}
	}
	for name := range pluginInputs {
		nameSet[name] = struct{}{}
	}
	for name := range pluginOutputs {
		nameSet[name] = struct{}{}
	}
	sortedNames := make([]string, 0, len(nameSet))
	for name := range nameSet {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	respPlugins := make([]TopologyPlugin, 0, len(sortedNames))
	for _, name := range sortedNames {
		p, registered := plugins[name]
		tp := TopologyPlugin{
			Name:    name,
			Active:  registered,
			Inputs:  edgesToSorted(pluginInputs[name]),
			Outputs: edgesToSorted(pluginOutputs[name]),
		}
		if registered {
			tp.Version = p.Version
		}
		respPlugins = append(respPlugins, tp)
	}

	// Signals list = union of producer and consumer signal names.
	signalSet := map[string]struct{}{}
	for s := range signalProducers {
		signalSet[s] = struct{}{}
	}
	for s := range signalConsumers {
		signalSet[s] = struct{}{}
	}
	sortedSignals := make([]string, 0, len(signalSet))
	for s := range signalSet {
		sortedSignals = append(sortedSignals, s)
	}
	sort.Strings(sortedSignals)

	respSignals := make([]TopologySignal, 0, len(sortedSignals))
	for _, sig := range sortedSignals {
		respSignals = append(respSignals, TopologySignal{
			Name:      sig,
			Producers: endpointsToSorted(signalProducers[sig]),
			Consumers: endpointsToSorted(signalConsumers[sig]),
		})
	}

	respondJSON(w, http.StatusOK, TopologyResponse{
		Version:    s.config.Version,
		CapturedAt: time.Now().UTC(),
		Plugins:    respPlugins,
		Signals:    respSignals,
	})
}

func recordEdge(m map[string]map[edgeKey]struct{}, name string, ek edgeKey) {
	if _, ok := m[name]; !ok {
		m[name] = map[edgeKey]struct{}{}
	}
	m[name][ek] = struct{}{}
}

func recordEndpoint(m map[string]map[TopologyEndpoint]struct{}, signal string, ep TopologyEndpoint) {
	if _, ok := m[signal]; !ok {
		m[signal] = map[TopologyEndpoint]struct{}{}
	}
	m[signal][ep] = struct{}{}
}

func edgesToSorted(set map[edgeKey]struct{}) []TopologyEdge {
	out := make([]TopologyEdge, 0, len(set))
	for k := range set {
		out = append(out, TopologyEdge{Signal: k.signal, Command: k.command})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Signal != out[j].Signal {
			return out[i].Signal < out[j].Signal
		}
		return out[i].Command < out[j].Command
	})
	return out
}

func endpointsToSorted(set map[TopologyEndpoint]struct{}) []TopologyEndpoint {
	out := make([]TopologyEndpoint, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Command < out[j].Command
	})
	return out
}
