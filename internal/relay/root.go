package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mattjoyce/ductile/internal/baggage"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/state"
)

type rootContextSeed struct {
	ID    string
	Scope map[string]any
}

func (r *Receiver) enqueueRootDispatches(ctx context.Context, accepted rootAcceptance) (string, error) {
	if r.router == nil {
		return "", nil
	}

	dispatches, err := r.router.Next(ctx, router.Request{
		SourceEventID: accepted.LocalEvent.EventID,
		Event:         accepted.LocalEvent,
	})
	if err != nil {
		return "", fmt.Errorf("resolve relay root routes: %w", err)
	}
	if len(dispatches) == 0 {
		return "", nil
	}

	rootSeeds := make(map[string]rootContextSeed)
	admittedBaggage := r.admitBaggage(accepted.Peer, accepted.Envelope.Baggage)
	firstJobID := ""
	for _, dispatch := range dispatches {
		// Admission boundary (P2-08): decide BEFORE writing durable event_context
		// rows whether this dispatch is a replay the queue would dedupe. On
		// AdmissionReplay we skip context creation entirely; the queue's dedupe
		// path is still authoritative on the job side. admitBaggage above is a
		// peer-ACL filter on baggage keys and is unrelated to this boundary.
		if r.admitter != nil {
			result, err := r.admitter.Admit(ctx, state.AdmissionInput{
				DedupeKey: dispatch.Event.DedupeKey,
				Plugin:    dispatch.Plugin,
				Command:   dispatch.Command,
			})
			if err != nil {
				return "", fmt.Errorf("admit relay dispatch (%s:%s): %w", dispatch.Plugin, dispatch.Command, err)
			}
			if result.Decision == state.AdmissionReplay {
				if firstJobID == "" {
					firstJobID = result.ExistingJobID
				}
				continue
			}
		}

		contextID, err := r.createEntryContext(ctx, rootSeeds, dispatch, admittedBaggage)
		if err != nil {
			return "", err
		}

		payload, err := marshalDispatchPayload(dispatch.Event, dispatch.Command)
		if err != nil {
			return "", fmt.Errorf("marshal relay dispatch payload: %w", err)
		}

		sourceEventID := accepted.LocalEvent.EventID
		enqueueReq := queue.EnqueueRequest{
			Plugin:         dispatch.Plugin,
			Command:        dispatch.Command,
			Payload:        payload,
			SubmittedBy:    "relay:" + accepted.Peer.Name,
			EventContextID: contextID,
			SourceEventID:  &sourceEventID,
			MaxAttempts:    config.MaxAttemptsForPlugin(r.cfg.Plugins[dispatch.Plugin]),
		}
		if dedupeKey := strings.TrimSpace(dispatch.Event.DedupeKey); dedupeKey != "" {
			enqueueReq.DedupeKey = &dedupeKey
		}

		jobID, err := r.queue.Enqueue(ctx, enqueueReq)
		if err != nil {
			var dedupeErr *queue.DedupeDropError
			if errors.As(err, &dedupeErr) {
				if firstJobID == "" {
					firstJobID = dedupeErr.ExistingJobID
				}
				continue
			}
			if errors.Is(err, queue.ErrSourceEventConflict) {
				// Benign at-least-once redelivery of the same target;
				// the original child already exists. Preserve idempotency.
				continue
			}
			return "", fmt.Errorf("enqueue relay root job for plugin %q: %w", dispatch.Plugin, err)
		}
		if firstJobID == "" {
			firstJobID = jobID
		}
	}

	return firstJobID, nil
}

func (r *Receiver) createEntryContext(
	ctx context.Context,
	rootSeeds map[string]rootContextSeed,
	dispatch router.Dispatch,
	admittedBaggage map[string]any,
) (*string, error) {
	if r.contexts == nil {
		return nil, nil
	}

	parentSeed, err := r.ensureRootSeed(ctx, rootSeeds, dispatch, admittedBaggage)
	if err != nil {
		return nil, err
	}
	parentScope := admittedBaggage
	var parentID *string
	if parentSeed != nil {
		parentScope = parentSeed.Scope
		parentID = &parentSeed.ID
	}

	updates, err := r.entryContextUpdates(dispatch, parentScope)
	if err != nil {
		return nil, err
	}
	created, err := r.contexts.Create(ctx, parentID, dispatch.PipelineName, dispatch.StepID, updates)
	if err != nil {
		return nil, fmt.Errorf("create relay entry context (%s:%s): %w", dispatch.PipelineName, dispatch.StepID, err)
	}
	return &created.ID, nil
}

func (r *Receiver) ensureRootSeed(
	ctx context.Context,
	rootSeeds map[string]rootContextSeed,
	dispatch router.Dispatch,
	admittedBaggage map[string]any,
) (*rootContextSeed, error) {
	if r.contexts == nil || strings.TrimSpace(dispatch.PipelineName) == "" || strings.TrimSpace(dispatch.PipelineInstanceID) == "" {
		return nil, nil
	}

	key := dispatch.PipelineName + "\x00" + dispatch.PipelineInstanceID
	if existing, ok := rootSeeds[key]; ok {
		return &existing, nil
	}

	updates, err := json.Marshal(admittedBaggage)
	if err != nil {
		return nil, fmt.Errorf("marshal admitted relay baggage: %w", err)
	}
	updates, err = state.WithPipelineInstanceID(updates, dispatch.PipelineInstanceID)
	if err != nil {
		return nil, fmt.Errorf("seed relay pipeline instance id: %w", err)
	}
	updates, err = state.WithRouteDepth(updates, 0)
	if err != nil {
		return nil, fmt.Errorf("seed relay route depth: %w", err)
	}
	if dispatch.RouteMaxDepth > 0 {
		updates, err = state.WithRouteMaxDepth(updates, dispatch.RouteMaxDepth)
		if err != nil {
			return nil, fmt.Errorf("seed relay route max depth: %w", err)
		}
	}

	created, err := r.contexts.Create(ctx, nil, dispatch.PipelineName, "", updates)
	if err != nil {
		return nil, fmt.Errorf("create relay pipeline root context (%s): %w", dispatch.PipelineName, err)
	}

	scope := cloneMap(admittedBaggage)
	if len(created.AccumulatedJSON) > 0 {
		if err := json.Unmarshal(created.AccumulatedJSON, &scope); err != nil {
			return nil, fmt.Errorf("decode relay root context: %w", err)
		}
	}

	seed := rootContextSeed{ID: created.ID, Scope: scope}
	rootSeeds[key] = seed
	return &seed, nil
}

func (r *Receiver) entryContextUpdates(dispatch router.Dispatch, parentScope map[string]any) (json.RawMessage, error) {
	var updates json.RawMessage
	if node, ok := r.router.GetNode(dispatch.PipelineName, dispatch.StepID); ok && node.Baggage != nil && !node.Baggage.Empty() {
		claims, err := baggage.ApplyClaims(dispatch.Event.Payload, node.Baggage, parentScope)
		if err != nil {
			return nil, fmt.Errorf("apply relay root baggage claims for %s:%s: %w", dispatch.PipelineName, dispatch.StepID, err)
		}
		updates, err = json.Marshal(claims)
		if err != nil {
			return nil, fmt.Errorf("marshal relay root baggage claims: %w", err)
		}
	}

	var err error
	if dispatch.RouteMaxDepth > 0 {
		updates, err = state.WithRouteMaxDepth(updates, dispatch.RouteMaxDepth)
		if err != nil {
			return nil, fmt.Errorf("seed relay context max depth: %w", err)
		}
	}
	if strings.TrimSpace(dispatch.PipelineInstanceID) != "" {
		updates, err = state.WithPipelineInstanceID(updates, dispatch.PipelineInstanceID)
		if err != nil {
			return nil, fmt.Errorf("seed relay context pipeline instance id: %w", err)
		}
	}
	if dispatch.RouteDepth > 0 {
		updates, err = state.WithRouteDepth(updates, dispatch.RouteDepth)
		if err != nil {
			return nil, fmt.Errorf("seed relay context route depth: %w", err)
		}
	}
	return updates, nil
}

func marshalDispatchPayload(event protocol.Event, command string) (json.RawMessage, error) {
	if strings.TrimSpace(command) == "handle" {
		return json.Marshal(event)
	}
	return json.Marshal(event.Payload)
}

// admitBaggage is a per-peer ACL filter on baggage keys, NOT the general
// admission boundary. It strips keys not in peer.AllowedBags from the
// envelope baggage before the dispatch loop. The replay-vs-fresh admission
// decision (P2-08) is handled by the Receiver.admitter field — see
// state.Admitter.
func (r *Receiver) admitBaggage(peer trustedPeer, baggageMap map[string]any) map[string]any {
	if len(peer.AllowedBags) == 0 || len(baggageMap) == 0 {
		return map[string]any{}
	}

	admitted := make(map[string]any)
	for key, value := range baggageMap {
		if _, ok := peer.AllowedBags[key]; !ok {
			continue
		}
		admitted[key] = value
	}
	return cloneMap(admitted)
}
