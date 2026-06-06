package dispatch

import (
	"errors"
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// ResolvedWorker is the privilege identity a plugin spawns under — the value that
// crosses the resolve→enforce seam (PrivSec ADR §4/§5; Hickey×Armstrong grill
// 2026-06-06). It is resolved from the operator's core config alone (never the
// plugin manifest) and consumed at spawn by the platform credential application.
//
// Confined == false is the named `unconfined` state: the plugin runs at the
// gateway's own uid with no privilege drop (today's behaviour). It is never a
// synthesised worker and must never be conflated with a configured `default`
// tier — see the ADR §5 vocabulary note.
type ResolvedWorker struct {
	Name     string // the granted worker name; "" when unconfined
	UID      int
	GID      int
	StateDir string
	Confined bool
}

// ErrWorkerGrantUndefined marks a plugin granted a worker that is not defined in
// the `workers` map — a misconfiguration. Resolution fails closed rather than
// silently downgrading to unconfined: a declared-but-unbuildable wall is an error,
// not a quiet run at gateway privilege (the fail-closed stance the grill settled).
var ErrWorkerGrantUndefined = errors.New("plugin grants an undefined worker")

// resolveWorker maps a plugin to its worker identity from the operator's core
// config. Tracer scope (#92): a plugin's `worker:` grant naming a configured
// worker resolves to that worker; no grant resolves to unconfined. A grant that
// names an unknown worker fails closed. The general grant model — the shared
// `default` tier fallback (#85) and fingerprint binding (#93) — layers on later.
func resolveWorker(cfg *config.Config, pluginName string) (ResolvedWorker, error) {
	if cfg == nil {
		return ResolvedWorker{}, nil
	}
	pc, ok := cfg.Plugins[pluginName]
	if !ok || pc.Worker == "" {
		// No grant → unconfined. (#85 will make this the shared `default` tier when
		// a workers table exists; the tracer keeps it unconfined.)
		return ResolvedWorker{}, nil
	}
	w, ok := cfg.Workers[pc.Worker]
	if !ok {
		return ResolvedWorker{}, fmt.Errorf("%w: plugin %q → worker %q", ErrWorkerGrantUndefined, pluginName, pc.Worker)
	}
	return ResolvedWorker{
		Name:     pc.Worker,
		UID:      w.UID,
		GID:      w.GID,
		StateDir: w.StateDir,
		Confined: true,
	}, nil
}
