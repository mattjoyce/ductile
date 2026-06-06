package dispatch

import (
	"errors"
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// WorkerSource records why a plugin resolved to its identity — useful for audit
// logging and tests, and the third member of the ResolvedWorker value the grill
// named (ADR §4/§5).
type WorkerSource string

const (
	// WorkerGranted: the plugin's own `worker:` grant named this tier.
	WorkerGranted WorkerSource = "granted"
	// WorkerDefaultTier: no grant, fell back to the shared `default` tier (Q2).
	WorkerDefaultTier WorkerSource = "default"
	// WorkerUnconfined: no drop — runs at the gateway uid (the named no-drop state).
	WorkerUnconfined WorkerSource = "unconfined"
)

// defaultWorkerName is the shared trusted tier an ungranted plugin falls back to
// when a `workers` table defines it (operator decision Q2). It is a *configured*
// worker, never a synthesised one.
const defaultWorkerName = "default"

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
	Name     string // the resolved worker name; "" when unconfined
	UID      int
	GID      int
	StateDir string
	Confined bool
	Source   WorkerSource
}

// ErrWorkerGrantUndefined marks a plugin granted a worker that is not defined in
// the `workers` map — a misconfiguration. Resolution fails closed rather than
// silently downgrading to unconfined: a declared-but-unbuildable wall is an error,
// not a quiet run at gateway privilege (the fail-closed stance the grill settled).
var ErrWorkerGrantUndefined = errors.New("plugin grants an undefined worker")

// ErrWorkerDropFailed marks a spawn that failed because the privilege drop itself
// could not be performed (the kernel refused setgroups/setgid/setuid, or the
// platform has no uid-drop). It is deliberately distinct from a missing-binary
// start error so the dispatcher can fail it CLOSED and TERMINAL: a botched drop is
// a misconfiguration, and retrying re-runs the same doomed setuid (ADR §8; #86).
var ErrWorkerDropFailed = errors.New("privsep: worker uid/gid drop failed at spawn")

// resolveWorker maps a plugin to its worker identity using the operator's core
// config ALONE — the plugin manifest is never consulted (ADR §4 authority split:
// the untrusted author declares needs, the trusted operator grants privilege).
// The function signature enforces this: it takes only the Config, so a manifest
// "worker hint" cannot reach the decision even if one were ever added.
//
// Resolution (#85):
//   - explicit `worker:` grant → that tier (source granted); unknown tier fails closed;
//   - no grant + a configured `default` tier → the shared `default` (source default, Q2);
//   - no grant + no `default` tier → unconfined (the boot gate, #86, decides whether
//     unconfined is permitted on this host).
func resolveWorker(cfg *config.Config, pluginName string) (ResolvedWorker, error) {
	if cfg == nil {
		return ResolvedWorker{Source: WorkerUnconfined}, nil
	}

	grant := cfg.Plugins[pluginName].Worker // "" for an unknown or ungranted plugin

	if grant == "" {
		// No grant. Fall back to the shared `default` tier when the operator defined
		// one (Q2); otherwise unconfined. Never a synthesised worker.
		if w, ok := cfg.Workers[defaultWorkerName]; ok {
			return confinedWorker(defaultWorkerName, w, WorkerDefaultTier), nil
		}
		return ResolvedWorker{Source: WorkerUnconfined}, nil
	}

	w, ok := cfg.Workers[grant]
	if !ok {
		return ResolvedWorker{}, fmt.Errorf("%w: plugin %q → worker %q", ErrWorkerGrantUndefined, pluginName, grant)
	}
	return confinedWorker(grant, w, WorkerGranted), nil
}

func confinedWorker(name string, w config.WorkerConf, src WorkerSource) ResolvedWorker {
	return ResolvedWorker{
		Name:     name,
		UID:      w.UID,
		GID:      w.GID,
		StateDir: w.StateDir,
		Confined: true,
		Source:   src,
	}
}
