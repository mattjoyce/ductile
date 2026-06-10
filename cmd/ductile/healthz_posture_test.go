package main

import (
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

// #142: with api.enabled=false the API server block can still run (relay
// configured), and /healthz answering "closed" FROM a live listener is the one
// spot reported posture and the live listener set disagree. The posture field
// is suppressed instead.
func TestHealthzPostureSuppressedWhenAPIDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.API.Enabled = false
	if got := healthzPostureFor(cfg, config.PostureClosed); got != "" {
		t.Fatalf("expected empty posture for a disabled API (relay-only listener), got %q", got)
	}
	cfg.API.Enabled = true
	if got := healthzPostureFor(cfg, config.PostureGateway); got != "gateway" {
		t.Fatalf("expected gateway posture for an enabled API, got %q", got)
	}
}
