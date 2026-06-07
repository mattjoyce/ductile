package dispatch

import (
	"errors"
	"fmt"

	"github.com/mattjoyce/ductile/internal/config"
)

// AccountSource records why a plugin resolved to its identity — useful for audit
// logging and tests, and the third member of the ResolvedAccount value the grill
// named (ADR §4/§5).
type AccountSource string

const (
	// AccountGranted: the plugin's own `account:` grant named this tier.
	AccountGranted AccountSource = "granted"
	// AccountDefault: no grant, fell back to the shared `default` tier (Q2).
	AccountDefault AccountSource = "default"
	// AccountUnconfined: no drop — runs at the gateway uid (the named no-drop state).
	AccountUnconfined AccountSource = "unconfined"
)

// defaultAccountName is the shared trusted tier an ungranted plugin falls back to
// when a `accounts` table defines it (operator decision Q2). It is a *configured*
// account, never a synthesised one.
const defaultAccountName = "default"

// ResolvedAccount is the privilege identity a plugin spawns under — the value that
// crosses the resolve→enforce seam (PrivSec ADR §4/§5; Hickey×Armstrong grill
// 2026-06-06). It is resolved from the operator's core config alone (never the
// plugin manifest) and consumed at spawn by the platform credential application.
//
// Confined == false is the named `unconfined` state: the plugin runs at the
// gateway's own uid with no privilege drop (today's behaviour). It is never a
// synthesised account and must never be conflated with a configured `default`
// tier — see the ADR §5 vocabulary note.
type ResolvedAccount struct {
	Name     string // the resolved account name; "" when unconfined
	UID      int
	GID      int
	StateDir string
	Confined bool
	Source   AccountSource
}

// ErrAccountGrantUndefined marks a plugin granted an account that is not defined in
// the `accounts` map — a misconfiguration. Resolution fails closed rather than
// silently downgrading to unconfined: a declared-but-unbuildable wall is an error,
// not a quiet run at gateway privilege (the fail-closed stance the grill settled).
var ErrAccountGrantUndefined = errors.New("plugin grants an undefined account")

// ErrAccountDropFailed marks a spawn that failed because the privilege drop itself
// could not be performed (the kernel refused setgroups/setgid/setuid, or the
// platform has no uid-drop). It is deliberately distinct from a missing-binary
// start error so the dispatcher can fail it CLOSED and TERMINAL: a botched drop is
// a misconfiguration, and retrying re-runs the same doomed setuid (ADR §8; #86).
var ErrAccountDropFailed = errors.New("privsep: account uid/gid drop failed at spawn")

// resolveAccount maps a plugin to its account identity using the operator's core
// config ALONE — the plugin manifest is never consulted (ADR §4 authority split:
// the untrusted author declares needs, the trusted operator grants privilege).
// The function signature enforces this: it takes only the Config, so a manifest
// "account hint" cannot reach the decision even if one were ever added.
//
// Resolution (#85):
//   - explicit `account:` grant → that tier (source granted); unknown tier fails closed;
//   - no grant + a configured `default` tier → the shared `default` (source default, Q2);
//   - no grant + no `default` tier → unconfined (the boot gate, #86, decides whether
//     unconfined is permitted on this host).
func resolveAccount(cfg *config.Config, pluginName string) (ResolvedAccount, error) {
	if cfg == nil {
		return ResolvedAccount{Source: AccountUnconfined}, nil
	}

	grant := cfg.Plugins[pluginName].RunAs // "" for an unknown or ungranted plugin

	if grant == "" {
		// No grant. Fall back to the shared `default` tier when the operator defined
		// one (Q2); otherwise unconfined. Never a synthesised account.
		if w, ok := cfg.Accounts[defaultAccountName]; ok {
			return confinedAccount(defaultAccountName, w, AccountDefault), nil
		}
		return ResolvedAccount{Source: AccountUnconfined}, nil
	}

	w, ok := cfg.Accounts[grant]
	if !ok {
		return ResolvedAccount{}, fmt.Errorf("%w: plugin %q → account %q", ErrAccountGrantUndefined, pluginName, grant)
	}
	return confinedAccount(grant, w, AccountGranted), nil
}

func confinedAccount(name string, w config.AccountConf, src AccountSource) ResolvedAccount {
	return ResolvedAccount{
		Name:     name,
		UID:      w.UID,
		GID:      w.GID,
		StateDir: w.StateDir,
		Confined: true,
		Source:   src,
	}
}

// Validate is the resolve→enforce seam guard (#100): a ResolvedAccount must be
// internally consistent before it can drive a privilege drop. `Confined` and the
// identity (UID/GID/Name/Source) encode one fact in several fields, so a zero
// value or a malformed value could otherwise be MISREAD — either as a silent
// "unconfined" or, far worse, as "confined, drop to uid 0" (root). This makes any
// inconsistency fail CLOSED at the seam, regardless of how the value was built:
//
//   - unconfined  -> must carry NO droppable identity (uid/gid == 0)
//   - confined    -> must be a real, NON-root identity (uid>0 AND gid>0)
//
// These are the security-critical invariants: a confined verdict can never become
// a drop to uid 0 (root) or a negative/zero id, and an "unconfined" verdict can
// never smuggle a uid in. (Name/Source are audit metadata, not gating — the drop
// is governed by the ids.) The drop path (applyAccountCredential) calls this
// before applying any credential, so a botched ResolvedAccount fails CLOSED.
func (r ResolvedAccount) Validate() error {
	if !r.Confined {
		if r.UID != 0 || r.GID != 0 {
			return fmt.Errorf("privsep: inconsistent ResolvedAccount: unconfined but carries uid:gid %d:%d", r.UID, r.GID)
		}
		return nil
	}
	if r.UID <= 0 || r.GID <= 0 {
		return fmt.Errorf("privsep: refusing privilege drop to uid:gid %d:%d — must be >0 (never root)", r.UID, r.GID)
	}
	return nil
}
