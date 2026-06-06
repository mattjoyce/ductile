package relay

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/mattjoyce/ductile/internal/config"
)

// NewSender builds an outbound relay sender from runtime config.
func NewSender(cfg *config.Config) (*Sender, error) {
	if cfg == nil {
		return nil, fmt.Errorf("relay sender config is nil")
	}

	instances := make(map[string]remoteInstance, len(cfg.RelayInstances))
	tokens := tokensByName(cfg)
	for _, entry := range cfg.RelayInstances {
		if !entry.Enabled {
			continue
		}
		secret, ok := tokens[entry.SecretRef]
		if !ok {
			return nil, fmt.Errorf("relay instance %q: secret_ref %q not found", entry.Name, entry.SecretRef)
		}

		allowed := make(map[string]struct{}, len(entry.Allow))
		for _, eventType := range entry.Allow {
			eventType = strings.TrimSpace(eventType)
			if eventType == "" {
				continue
			}
			allowed[eventType] = struct{}{}
		}

		instances[entry.Name] = remoteInstance{
			Name:        entry.Name,
			BaseURL:     strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/"),
			IngressPath: strings.TrimSpace(entry.IngressPath),
			Secret:      secret,
			KeyID:       strings.TrimSpace(entry.KeyID),
			Timeout:     normalizeRequestTimeout(entry.Timeout),
			Allow:       allowed,
		}
	}

	return &Sender{
		cfg: senderConfig{
			ServiceName: strings.TrimSpace(cfg.Service.Name),
			Instances:   instances,
		},
		now: nowUTC,
	}, nil
}

// NewReceiver builds an inbound relay receiver from runtime config.
//
// admitter is the shared admission boundary that decides whether to admit a
// dispatch before durable event_context creation (P2-08). It is required:
// pass state.NewAdmitter(queue, state.DefaultMaxContextBytes) at the call
// site so the receiver mirrors production semantics. Tests that do not
// exercise replay paths may pass a no-op admitter built with a nil probe.
func NewReceiver(
	cfg *config.Config,
	queue JobQueuer,
	router EventRouter,
	contexts EventContextStore,
	admitter AdmissionGate,
	logger *slog.Logger,
) (*Receiver, error) {
	if cfg == nil {
		return nil, fmt.Errorf("relay receiver config is nil")
	}
	if cfg.RemoteIngress == nil {
		return nil, nil
	}

	tokens := tokensByName(cfg)
	peers := make(map[string]trustedPeer, len(cfg.RemoteIngress.TrustedPeers))
	for _, entry := range cfg.RemoteIngress.TrustedPeers {
		if !entry.Enabled {
			continue
		}
		secret, ok := tokens[entry.SecretRef]
		if !ok {
			return nil, fmt.Errorf("relay peer %q: secret_ref %q not found", entry.Name, entry.SecretRef)
		}

		accept := make(map[string]struct{}, len(entry.Accept))
		for _, eventType := range entry.Accept {
			eventType = strings.TrimSpace(eventType)
			if eventType == "" {
				continue
			}
			accept[eventType] = struct{}{}
		}
		allowedBags := make(map[string]struct{}, len(entry.Baggage.Allow))
		for _, key := range entry.Baggage.Allow {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			allowedBags[key] = struct{}{}
		}

		peers[entry.Name] = trustedPeer{
			Name:        entry.Name,
			Secret:      secret,
			KeyID:       strings.TrimSpace(entry.KeyID),
			Accept:      accept,
			AllowedBags: allowedBags,
		}
	}

	maxBodySize := int64(defaultRelayMaxBodySize)
	if raw := strings.TrimSpace(cfg.RemoteIngress.MaxBodySize); raw != "" {
		parsed, err := config.ParseByteSize(raw)
		if err != nil {
			return nil, fmt.Errorf("relay receiver max_body_size: %w", err)
		}
		maxBodySize = parsed
	}

	plugins := make(map[string]config.PluginConf, len(cfg.Plugins))
	for name, pc := range cfg.Plugins {
		plugins[name] = pc
	}

	return &Receiver{
		cfg: receiverConfig{
			ListenPath:       normalizeListenPath(cfg.RemoteIngress.ListenPath),
			MaxBodySize:      maxBodySize,
			AllowedClockSkew: normalizeAllowedClockSkew(cfg.RemoteIngress.AllowedClockSkew),
			RequireKeyID:     cfg.RemoteIngress.RequireKeyID,
			Peers:            peers,
			Plugins:          plugins,
		},
		queue:    queue,
		router:   router,
		contexts: contexts,
		admitter: admitter,
		logger:   defaultLogger(logger).With("component", "relay"),
		now:      nowUTC,
	}, nil
}

func tokensByName(cfg *config.Config) map[string]string {
	return cfg.ResolvedSecrets
}
