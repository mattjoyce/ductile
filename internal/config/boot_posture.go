package config

// BootPosture is the first-class, observable state of the gateway's listener
// plane at boot. It is promoted from an EMERGENT condition — previously inferred
// ad hoc from api.enabled plus whether tokens happened to resolve — to a single
// named value the runtime computes once and every observer (system status,
// doctor, selfcheck) reads. Promoting it to a value is what keeps those observers
// from each re-deriving "am I activated?" independently and disagreeing.
//
// See docs/adr/vault-credential-ladder.md §4 (two-posture bootstrap) and §5
// (the posture must be first-class and observable, not an accidental
// half-booted state a bug can strand).
type BootPosture int

const (
	// PostureClosed: the gateway plane is not requested (api.enabled is false).
	// A relay-only deployment still opens its own listener — that path is
	// independent of this decision and is left to the caller.
	PostureClosed BootPosture = iota

	// PostureGateway: the normal serving posture. The public gateway listener
	// opens only after ResolveAPITokens succeeds — fail-closed, #94/#119.
	PostureGateway

	// PostureManagementOnly: the from-scratch "vault-operable / ductile-closed"
	// posture. A genesis-only vault (admin token present, NO api token yet) is
	// open and operable over the LOCAL management transport, while the public
	// gateway listener is NOT served. The admin token mints the api token(s)
	// through this surface; `system reload` then transitions to PostureGateway.
	PostureManagementOnly
)

// String renders the posture for logs and status output.
func (p BootPosture) String() string {
	switch p {
	case PostureClosed:
		return "closed"
	case PostureGateway:
		return "gateway"
	case PostureManagementOnly:
		return "management-only"
	default:
		return "unknown"
	}
}

// APIEnabledWithoutToken reports the structural condition behind #94/#119: the
// gateway API is enabled but no bearer token is configured. On its own that is a
// misconfiguration — an enabled gateway with no credential — EXCEPT in the
// from-scratch bootstrap posture, where a vault is present to mint the token
// (DecideBootPosture then returns PostureManagementOnly). hasVault carries that
// single exception, so the rule lives in ONE place and the three layers that
// enforce it (config load-validation, doctor, runtime admission) cannot drift.
// Returns true when the config should be rejected.
func APIEnabledWithoutToken(cfg *Config, hasVault bool) bool {
	return cfg != nil && cfg.API.Enabled && len(cfg.API.Auth.Tokens) == 0 && !hasVault
}

// DecideBootPosture decides which posture the GATEWAY plane boots into, from the
// resolved config and whether a vault owner is present to operate. It is pure:
// no I/O, no secret resolution. It decides only from the COUNT of configured api
// tokens whether the from-scratch management posture applies — the caller still
// runs ResolveAPITokens (fail-closed) for the gateway posture.
//
// The fail-closed gate (#94/#119) is deliberately NOT moved here: that gate fires
// when api tokens ARE configured but do not resolve (a typo'd/missing secret_ref)
// — a different condition than "no api token configured at all." Management
// posture is the latter, and only when a vault owner exists to make the surface
// meaningful. With zero tokens and no vault owner there is nothing to manage, so
// the decision stays PostureGateway and the caller's RequireAPIAuth abort still
// fails the boot closed.
func DecideBootPosture(cfg *Config, hasVaultOwner bool) BootPosture {
	if cfg == nil || !cfg.API.Enabled {
		return PostureClosed
	}
	if len(cfg.API.Auth.Tokens) == 0 && hasVaultOwner {
		return PostureManagementOnly
	}
	return PostureGateway
}
