package dispatch

import (
	"fmt"
	"os"

	"github.com/mattjoyce/ductile/internal/config"
)

// BootMode is the privsep posture decided once at startup by the boot gate.
type BootMode int

const (
	// BootUnconfined: spawn plugins at the gateway uid, no drop (today's behaviour).
	BootUnconfined BootMode = iota
	// BootEnforce: drop each granted plugin to its resolved account.
	BootEnforce
)

func (m BootMode) String() string {
	if m == BootEnforce {
		return "enforce"
	}
	return "unconfined"
}

// evaluateBootGate is the pure privsep boot decision (PrivSec ADR §5; grill
// 2026-06-06): capability-to-drop and accounts-configured must AGREE, or the
// gateway refuses to start — no silent auto-degrade. The one escape hatch is an
// explicit `service.unconfined: true`, which permits unconfined operation despite
// a configured/privileged host (the caller logs it loudly).
//
//	capability + accounts      → enforce
//	no capability + no accounts → unconfined (dev/today)
//	capability + no accounts    → REFUSE (privileged daemon, nothing to drop to)
//	no capability + accounts    → REFUSE (a wall declared the host cannot build)
//	unconfinedOverride         → unconfined, always (explicit opt-out)
func evaluateBootGate(capabilityHeld, accountsConfigured, unconfinedOverride bool) (BootMode, error) {
	if unconfinedOverride {
		return BootUnconfined, nil
	}
	switch {
	case capabilityHeld && accountsConfigured:
		return BootEnforce, nil
	case !capabilityHeld && !accountsConfigured:
		return BootUnconfined, nil
	case capabilityHeld && !accountsConfigured:
		return BootUnconfined, fmt.Errorf("privsep boot gate: gateway holds the uid-drop capability but no accounts are configured — refusing to run plugins at gateway privilege (configure an accounts table, launch without CAP_SETUID, or set service.unconfined: true)")
	default: // !capabilityHeld && accountsConfigured
		return BootUnconfined, fmt.Errorf("privsep boot gate: accounts are configured but the gateway lacks the uid-drop capability (CAP_SETUID/SETGID) — refusing to run with a wall it cannot enforce (grant the capability via the init system, remove the accounts table, or set service.unconfined: true)")
	}
}

// BootGate evaluates the privsep posture for a config on the current host. It is
// called once at startup; a returned error must abort the boot (fail closed).
func BootGate(cfg *config.Config) (BootMode, error) {
	if cfg == nil {
		return BootUnconfined, nil
	}
	return evaluateBootGate(hasDropCapability(), len(cfg.Accounts) > 0, cfg.Service.Unconfined)
}

// ReconcileAccountFilesystem enforces the privsep filesystem floor at boot (#87) —
// the secrets surface must be gateway-owned and unreadable by accounts, and each
// account gets a private 0700 dir it owns. All-or-refuse: a returned error must
// abort the boot. Call only when the gate decided to enforce.
func ReconcileAccountFilesystem(cfg *config.Config, secretPaths []string) error {
	if cfg == nil {
		return nil
	}
	return reconcileAccountFilesystem(cfg, secretPaths, os.Geteuid())
}
