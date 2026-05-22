package state

import (
	"context"
	"strings"
)

// DedupeStatus is the queue's view of a `(plugin, command, dedupe_key)` tuple
// at the moment of admission. It mirrors the lookups the queue's Enqueue would
// perform but writes no durable state.
type DedupeStatus int

const (
	// DedupeStatusNone means the queue has no outstanding or recent-success
	// job for this tuple; a new enqueue would proceed.
	DedupeStatusNone DedupeStatus = iota
	// DedupeStatusOutstanding means a queued or running job already exists
	// for this tuple; a new enqueue would be dropped as a duplicate.
	DedupeStatusOutstanding
	// DedupeStatusRecentSuccess means a job for this tuple succeeded within
	// the dedupe TTL; a new enqueue would be dropped as a duplicate.
	DedupeStatusRecentSuccess
)

// QueueAdmissionProbe is the small read-only interface the admitter consumes
// from the queue. It exists so internal/state has no compile-time dependency
// on internal/queue (the dependency is queue -> state, not the reverse).
type QueueAdmissionProbe interface {
	DedupeStatus(ctx context.Context, plugin, command, dedupeKey string) (DedupeStatus, string, error)
}

// AdmissionDecision classifies one admission outcome.
type AdmissionDecision int

const (
	// AdmissionFresh means the caller may proceed with durable context
	// creation and enqueue. This is the only decision under which a row
	// should be appended to event_context.
	AdmissionFresh AdmissionDecision = iota
	// AdmissionReplay means the queue already has a job for this identity;
	// the caller must skip durable context creation entirely (P2-08, P2-09).
	AdmissionReplay
	// AdmissionRejectOverlimit means the proposed root baggage exceeds the
	// configured size cap. The caller must surface a client error (e.g.
	// HTTP 413 with a `bytes_actual`/`bytes_limit` hint) and write no row
	// (P2-04).
	AdmissionRejectOverlimit
)

// AdmissionInput describes one prospective durable-context creation site.
// DedupeKey may be empty when the caller has not opted into replay protection
// (e.g. the current pipeline-API trigger which does not derive a key). When
// DedupeKey is non-empty, Plugin and Command MUST be set so the probe lookup
// matches the queue's `(plugin, command, dedupe_key)` dedupe identity exactly.
// AccumulatedJSON is the proposed updates JSON (post-baggage-claim merge) for
// the root context; only the root branch is size-checked here. Per-step child
// writes inherit parent size via the existing defensive check in
// ContextStore.Create.
type AdmissionInput struct {
	DedupeKey       string
	Plugin          string
	Command         string
	AccumulatedJSON []byte
}

// AdmissionResult carries the admit() decision plus optional hint payload.
type AdmissionResult struct {
	Decision      AdmissionDecision
	ExistingJobID string // populated when Decision == AdmissionReplay
	BytesActual   int64  // populated when Decision == AdmissionRejectOverlimit
	BytesLimit    int64  // populated when Decision == AdmissionRejectOverlimit
}

// Admitter is the shared admission boundary in front of ContextStore.Create.
// It decides {Fresh | Replay | RejectOverlimit} from queue dedupe state and the
// proposed baggage size, without writing durable state. Callers MUST consult
// Admit() before any ContextStore.Create call on the relay, dispatch and
// pipeline-API ingress paths; durable creation runs only when the decision is
// AdmissionFresh.
//
// TOCTOU note: between Admit() returning Fresh and the subsequent Create plus
// Enqueue, a concurrent ingress for the same (plugin, command, key) tuple
// could in principle slip through. The current ingress paths each handle one
// signed envelope (relay) or one HTTP request (api/dispatch routed events) per
// goroutine, so the window is empty in practice. Concurrent writers for the
// same dedupe identity would need explicit serialization at the ingress
// boundary or a UNIQUE constraint on event_context — neither is in scope for
// PR3.
type Admitter struct {
	probe    QueueAdmissionProbe
	maxBytes int
}

// NewAdmitter constructs an Admitter that consults the supplied queue probe
// for replay decisions and rejects baggage larger than maxBytes. A nil probe
// disables the replay check (admission is purely size-gated); a non-positive
// maxBytes falls back to DefaultMaxContextBytes.
func NewAdmitter(probe QueueAdmissionProbe, maxBytes int) *Admitter {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxContextBytes
	}
	return &Admitter{
		probe:    probe,
		maxBytes: maxBytes,
	}
}

// Admit returns the admission decision for one prospective root context.
// On AdmissionRejectOverlimit the baggage is too large and no probe lookup is
// performed. On AdmissionReplay the queue would dedupe a subsequent Enqueue
// for this identity; the caller must skip context creation. On AdmissionFresh
// the caller may proceed.
func (a *Admitter) Admit(ctx context.Context, in AdmissionInput) (AdmissionResult, error) {
	if a == nil {
		return AdmissionResult{Decision: AdmissionFresh}, nil
	}

	// Size gate first — never invoke a probe lookup for a payload we already
	// know we will reject.
	if len(in.AccumulatedJSON) > a.maxBytes {
		return AdmissionResult{
			Decision:    AdmissionRejectOverlimit,
			BytesActual: int64(len(in.AccumulatedJSON)),
			BytesLimit:  int64(a.maxBytes),
		}, nil
	}

	key := strings.TrimSpace(in.DedupeKey)
	if key == "" || a.probe == nil {
		return AdmissionResult{Decision: AdmissionFresh}, nil
	}
	if strings.TrimSpace(in.Plugin) == "" || strings.TrimSpace(in.Command) == "" {
		return AdmissionResult{Decision: AdmissionFresh}, nil
	}

	status, existingID, err := a.probe.DedupeStatus(ctx, in.Plugin, in.Command, key)
	if err != nil {
		return AdmissionResult{}, err
	}

	if status == DedupeStatusOutstanding || status == DedupeStatusRecentSuccess {
		return AdmissionResult{
			Decision:      AdmissionReplay,
			ExistingJobID: existingID,
		}, nil
	}

	return AdmissionResult{Decision: AdmissionFresh}, nil
}
