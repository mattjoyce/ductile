package relay

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/protocol"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/router/dsl"
	"github.com/mattjoyce/ductile/internal/state"
)

// Relay transport headers.
const (
	HeaderPeer      = "X-Ductile-Peer"
	HeaderKeyID     = "X-Ductile-Key-Id"
	HeaderTimestamp = "X-Ductile-Timestamp"
	HeaderSignature = "X-Ductile-Signature"
)

// Envelope is the on-wire remote relay request body.
type Envelope struct {
	Event   EnvelopeEvent  `json:"event"`
	Origin  EnvelopeOrigin `json:"origin,omitempty"`
	Baggage map[string]any `json:"baggage,omitempty"`
}

// EnvelopeEvent is the immutable relayed event value.
type EnvelopeEvent struct {
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	DedupeKey string         `json:"dedupe_key,omitempty"`
}

// EnvelopeOrigin carries sender-side observability metadata.
type EnvelopeOrigin struct {
	Instance string `json:"instance"`
	Plugin   string `json:"plugin,omitempty"`
	JobID    string `json:"job_id,omitempty"`
	EventID  string `json:"event_id,omitempty"`
}

// AcceptanceResponse describes a validated relay request.
type AcceptanceResponse struct {
	Status          string `json:"status"`
	Peer            string `json:"peer"`
	EventType       string `json:"event_type"`
	ReceiverEventID string `json:"receiver_event_id"`
	JobID           string `json:"job_id,omitempty"`
}

// ErrorResponse reports a relay request error.
type ErrorResponse struct {
	Error string `json:"error"`
}

type remoteInstance struct {
	Name        string
	BaseURL     string
	IngressPath string
	Secret      string
	KeyID       string
	Timeout     time.Duration
	Allow       map[string]struct{}
}

type trustedPeer struct {
	Name        string
	Secret      string
	KeyID       string
	Accept      map[string]struct{}
	AllowedBags map[string]struct{}
}

type senderConfig struct {
	ServiceName string
	Instances   map[string]remoteInstance
}

type receiverConfig struct {
	ListenPath       string
	MaxBodySize      int64
	AllowedClockSkew time.Duration
	RequireKeyID     bool
	Peers            map[string]trustedPeer
	// Plugins is a snapshot of plugin config (name -> PluginConf) used to
	// resolve enqueue-time retry policy. Populated at NewReceiver. P2-02.
	Plugins map[string]config.PluginConf
}

// JobQueuer enqueues relay-triggered jobs.
type JobQueuer interface {
	Enqueue(ctx context.Context, req queue.EnqueueRequest) (string, error)
}

// EventRouter resolves root relay events into local jobs.
type EventRouter interface {
	Next(ctx context.Context, req router.Request) ([]router.Dispatch, error)
	GetNode(pipelineName string, stepID string) (dsl.Node, bool)
}

// EventContextStore persists root-entry baggage and route metadata.
type EventContextStore interface {
	Create(ctx context.Context, parentID *string, pipelineName string, stepID string, updates json.RawMessage) (*state.EventContext, error)
	Get(ctx context.Context, id string) (*state.EventContext, error)
}

type rootAcceptance struct {
	LocalEvent protocol.Event
	Peer       trustedPeer
	Envelope   Envelope
}

func defaultLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}
