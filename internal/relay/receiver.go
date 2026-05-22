package relay

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mattjoyce/ductile/internal/protocol"
)

type requestError struct {
	status int
	msg    string
}

func (e *requestError) Error() string {
	return e.msg
}

// Receiver validates and accepts authenticated remote relay requests.
type Receiver struct {
	cfg      receiverConfig
	queue    JobQueuer
	router   EventRouter
	contexts EventContextStore
	admitter AdmissionGate
	logger   *slog.Logger
	now      func() time.Time
}

// RoutePattern returns the chi route pattern for peer-specific relay ingress.
func (r *Receiver) RoutePattern() string {
	return normalizeListenPath(r.cfg.ListenPath) + "/{peer}"
}

// HandleHTTP validates one remote relay request and converts it into local root work.
func (r *Receiver) HandleHTTP(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()

	accepted, err := r.accept(req)
	if err != nil {
		var reqErr *requestError
		if errors.As(err, &reqErr) {
			r.respondJSON(w, reqErr.status, ErrorResponse{Error: reqErr.msg})
			return
		}
		r.logger.Error("relay request failed", "path", req.URL.Path, "error", err)
		r.respondJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to accept relay request"})
		return
	}

	jobID, err := r.enqueueRootDispatches(ctx, *accepted)
	if err != nil {
		r.logger.Error("relay enqueue failed",
			"peer", accepted.Peer.Name,
			"event_type", accepted.LocalEvent.Type,
			"receiver_event_id", accepted.LocalEvent.EventID,
			"error", err,
		)
		r.respondJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to enqueue relay event"})
		return
	}

	r.logger.Info("relay request accepted",
		"peer", accepted.Peer.Name,
		"event_type", accepted.LocalEvent.Type,
		"receiver_event_id", accepted.LocalEvent.EventID,
		"job_id", jobID,
		"origin_instance", accepted.Envelope.Origin.Instance,
		"origin_job_id", accepted.Envelope.Origin.JobID,
		"origin_event_id", accepted.Envelope.Origin.EventID,
	)

	r.respondJSON(w, http.StatusAccepted, AcceptanceResponse{
		Status:          "accepted",
		Peer:            accepted.Peer.Name,
		EventType:       accepted.LocalEvent.Type,
		ReceiverEventID: accepted.LocalEvent.EventID,
		JobID:           jobID,
	})
}

func (r *Receiver) accept(req *http.Request) (*rootAcceptance, error) {
	peerName := strings.TrimSpace(chi.URLParam(req, "peer"))
	if peerName == "" {
		return nil, &requestError{status: http.StatusForbidden, msg: "unknown relay peer"}
	}

	peer, ok := r.cfg.Peers[peerName]
	if !ok {
		return nil, &requestError{status: http.StatusForbidden, msg: "unknown relay peer"}
	}

	headerPeer := strings.TrimSpace(req.Header.Get(HeaderPeer))
	if headerPeer == "" || headerPeer != peerName {
		return nil, &requestError{status: http.StatusForbidden, msg: "peer identity mismatch"}
	}

	timestampHeader := strings.TrimSpace(req.Header.Get(HeaderTimestamp))
	if timestampHeader == "" {
		return nil, &requestError{status: http.StatusForbidden, msg: "missing relay timestamp"}
	}
	timestamp, err := parseRelayTimestamp(timestampHeader)
	if err != nil {
		return nil, &requestError{status: http.StatusForbidden, msg: "invalid relay timestamp"}
	}
	now := nowUTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	skew := now.Sub(timestamp)
	if skew < 0 {
		skew = -skew
	}
	if skew > r.cfg.AllowedClockSkew {
		return nil, &requestError{status: http.StatusForbidden, msg: "relay timestamp outside allowed clock skew"}
	}

	keyID := strings.TrimSpace(req.Header.Get(HeaderKeyID))
	if r.cfg.RequireKeyID && keyID == "" {
		return nil, &requestError{status: http.StatusForbidden, msg: "missing relay key id"}
	}
	if peer.KeyID != "" && keyID != peer.KeyID {
		return nil, &requestError{status: http.StatusForbidden, msg: "relay key id mismatch"}
	}

	signature := strings.TrimSpace(req.Header.Get(HeaderSignature))
	if signature == "" {
		return nil, &requestError{status: http.StatusForbidden, msg: "missing relay signature"}
	}

	limitedReader := io.LimitReader(req.Body, r.cfg.MaxBodySize+1)
	body, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, &requestError{status: http.StatusBadRequest, msg: "failed to read request body"}
	}
	if int64(len(body)) > r.cfg.MaxBodySize {
		return nil, &requestError{status: http.StatusRequestEntityTooLarge, msg: "relay payload too large"}
	}

	if err := verifySignature(req.Method, req.URL.Path, timestampHeader, body, signature, peer.Secret); err != nil {
		return nil, &requestError{status: http.StatusForbidden, msg: "forbidden"}
	}

	envelope, err := validateEnvelope(body)
	if err != nil {
		return nil, &requestError{status: http.StatusBadRequest, msg: err.Error()}
	}
	if origin := strings.TrimSpace(envelope.Origin.Instance); origin != "" && origin != peerName {
		return nil, &requestError{status: http.StatusForbidden, msg: "origin instance mismatch"}
	}
	if len(peer.Accept) > 0 {
		if _, ok := peer.Accept[envelope.Event.Type]; !ok {
			return nil, &requestError{status: http.StatusUnprocessableEntity, msg: "event type not allowed for peer"}
		}
	}

	// Use the caller-supplied dedupe key when present. If absent, derive one
	// from envelope identity so a replay of the same signed envelope within the
	// clock-skew window cannot enqueue a second job. Derivation requires
	// origin.event_id; if that field is empty the caller has not opted into
	// replay protection and we accept the risk rather than silently dropping.
	dedupeKey := strings.TrimSpace(envelope.Event.DedupeKey)
	if dedupeKey == "" {
		if originEventID := strings.TrimSpace(envelope.Origin.EventID); originEventID != "" {
			dedupeKey = "relay:" + peerName + ":" + strings.TrimSpace(envelope.Origin.Instance) + ":" + originEventID
		}
	}

	localEvent := protocol.Event{
		Type:      envelope.Event.Type,
		Payload:   cloneMap(envelope.Event.Payload),
		DedupeKey: dedupeKey,
		Source:    "relay:" + peer.Name,
		Timestamp: now,
		EventID:   uuid.NewString(),
	}

	return &rootAcceptance{
		LocalEvent: localEvent,
		Peer:       peer,
		Envelope:   *envelope,
	}, nil
}

func (r *Receiver) respondJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		r.logger.Error("failed to write relay response", "error", err)
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		out = make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
	}
	return out
}
