package dispatch

import (
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// BootMode is the privsep posture decided once at startup by the boot gate.
type BootMode int

const (
	// BootUnconfined: spawn plugins at the gateway uid, no drop (today's behaviour).
	BootUnconfined BootMode = iota
	// BootEnforce: drop each granted plugin to its resolved worker.
	BootEnforce
)

func (m BootMode) String() string {
	if m == BootEnforce {
		return "enforce"
	}
	return "unconfined"
}

// evaluateBootGate is the pure privsep boot decision (PrivSec ADR §5; grill
// 2026-06-06): capability-to-drop and workers-configured must AGREE, or the
// gateway refuses to start — no silent auto-degrade. The one escape hatch is an
// explicit `service.unconfined: true`, which permits unconfined operation despite
// a configured/privileged host (the caller logs it loudly).
//
//	capability + workers      → enforce
//	no capability + no workers → unconfined (dev/today)
//	capability + no workers    → REFUSE (privileged daemon, nothing to drop to)
//	no capability + workers    → REFUSE (a wall declared the host cannot build)
//	unconfinedOverride         → unconfined, always (explicit opt-out)
func evaluateBootGate(capabilityHeld, workersConfigured, unconfinedOverride bool) (BootMode, error) {
	if unconfinedOverride {
		return BootUnconfined, nil
	}
	switch {
	case capabilityHeld && workersConfigured:
		return BootEnforce, nil
	case !capabilityHeld && !workersConfigured:
		return BootUnconfined, nil
	case capabilityHeld && !workersConfigured:
		return BootUnconfined, fmt.Errorf("privsep boot gate: gateway holds the uid-drop capability but no workers are configured — refusing to run plugins at gateway privilege (configure a workers table, launch without CAP_SETUID, or set service.unconfined: true)")
	default: // !capabilityHeld && workersConfigured
		return BootUnconfined, fmt.Errorf("privsep boot gate: workers are configured but the gateway lacks the uid-drop capability (CAP_SETUID/SETGID) — refusing to run with a wall it cannot enforce (grant the capability via the init system, remove the workers table, or set service.unconfined: true)")
	}
}

// BootGate evaluates the privsep posture for a config on the current host. It is
// called once at startup; a returned error must abort the boot (fail closed).
func BootGate(cfg *config.Config) (BootMode, error) {
	if cfg == nil {
		return BootUnconfined, nil
	}
	return evaluateBootGate(hasDropCapability(), len(cfg.Workers) > 0, cfg.Service.Unconfined)
}
