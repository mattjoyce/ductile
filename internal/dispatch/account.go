package dispatch

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/mattjoyce/ductile/internal/config"
)

// AccountMode is the privilege tier a plugin spawns under — the closed three-member
// set the luminary grill (Hickey/Liskov, 2026-06-08) demanded in place of a
// `Confined bool` + `Home` string tag that encoded a 3-valued domain in two values.
// Consumers switch on it exhaustively; the zero value is Unconfined so a zero
// ResolvedAccount is consistently the no-drop state.
type AccountMode int

const (
	// ModeUnconfined: no privilege drop — runs at the gateway uid. Zero value.
	ModeUnconfined AccountMode = iota
	// ModeConfined: drops to a throwaway account uid and is WALLED — HOME/cache/cwd
	// rebased to a private 0700 state_dir (#109 C contract).
	ModeConfined
	// ModeCredentialed: drops to a real login uid (the operator) but runs with that
	// user's REAL home so it can reach on-disk creds — NOT walled. The trusted tier
	// (docs/adr/credentialed-runtime-contract.md).
	ModeCredentialed
)

func (m AccountMode) String() string {
	switch m {
	case ModeUnconfined:
		return "unconfined"
	case ModeConfined:
		return "confined"
	case ModeCredentialed:
		return "credentialed"
	default:
		return fmt.Sprintf("mode(%d)", int(m))
	}
}

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
// Mode == ModeUnconfined is the named no-drop state: the plugin runs at the
// gateway's own uid. It is never a synthesised account and must never be conflated
// with a configured `default` tier — see the ADR §5 vocabulary note.
type ResolvedAccount struct {
	Name     string // the resolved account name; "" when unconfined
	UID      int
	GID      int
	StateDir string
	// Home roots a CREDENTIALED account's runtime at the operator's real home (so the
	// plugin reaches ~/.ssh, ~/.config/gh); empty for confined/unconfined. The tier is
	// Mode, not the presence of this field — Home is data for the credentialed mode.
	Home   string
	Mode   AccountMode
	Source AccountSource
}

// Drops reports whether spawning this account performs a uid/gid privilege drop —
// true for BOTH confined and credentialed, false only for unconfined. The drop
// path keys off this; the runtime treatment keys off the mode (Credentialed()).
func (r ResolvedAccount) Drops() bool { return r.Mode != ModeUnconfined }

// Credentialed reports the trusted, real-HOME tier: it drops to its uid like a
// confined account, but its runtime is rooted at the operator's real Home, NOT a
// walled state_dir. The privilege gate (uid>0, never root) is identical to
// confined; only the runtime treatment differs (subprocess_executor / fsreconcile).
func (r ResolvedAccount) Credentialed() bool { return r.Mode == ModeCredentialed }

// Walled reports the confined tier: it drops AND its runtime is rebased to a
// private state_dir (the #109 C contract). Distinct from Credentialed(), which
// also drops but is NOT walled.
func (r ResolvedAccount) Walled() bool { return r.Mode == ModeConfined }

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
			return configuredAccount(defaultAccountName, w, AccountDefault), nil
		}
		return ResolvedAccount{Source: AccountUnconfined}, nil
	}

	w, ok := cfg.Accounts[grant]
	if !ok {
		return ResolvedAccount{}, fmt.Errorf("%w: plugin %q → account %q", ErrAccountGrantUndefined, pluginName, grant)
	}
	return configuredAccount(grant, w, AccountGranted), nil
}

// configuredAccount builds the resolved value for a configured account, choosing
// the tier from the config shape: a `home:` selects the CREDENTIALED (trusted)
// tier, otherwise CONFINED (walled). This is the SINGLE construction point for a
// dropping account — the grill (Liskov) caught a second, hand-built site
// (mostRestrictedAccount) that silently dropped Home; routing all resolution
// through here keeps the tier explicit and consistent at every construction.
func configuredAccount(name string, w config.AccountConf, src AccountSource) ResolvedAccount {
	mode := ModeConfined
	if w.Home != "" {
		mode = ModeCredentialed
	}
	return ResolvedAccount{
		Name:     name,
		UID:      w.UID,
		GID:      w.GID,
		StateDir: w.StateDir,
		Home:     w.Home,
		Mode:     mode,
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
//   - unconfined           -> must carry NO droppable identity (uid/gid == 0, no home)
//   - confined/credentialed -> must be a real, NON-root identity (uid>0 AND gid>0);
//     credentialed must additionally carry an absolute, non-"/" home (the seam, not
//     just config load, refuses a bad home — it does not trust the builder).
//
// These are the security-critical invariants: a dropping verdict can never become a
// drop to uid 0 (root) or a negative/zero id, and an "unconfined" verdict can never
// smuggle a uid in. (Name/Source are audit metadata, not gating — the drop is
// governed by the ids.) The drop path (applyAccountCredential) calls this before
// applying any credential, so a botched ResolvedAccount fails CLOSED.
func (r ResolvedAccount) Validate() error {
	switch r.Mode {
	case ModeUnconfined:
		if r.UID != 0 || r.GID != 0 {
			return fmt.Errorf("privsep: inconsistent ResolvedAccount: unconfined but carries uid:gid %d:%d", r.UID, r.GID)
		}
		// A home is meaningless without a drop — an unconfined value carrying one is
		// a malformed verdict, not a silent real-HOME run.
		if r.Home != "" {
			return fmt.Errorf("privsep: inconsistent ResolvedAccount: unconfined but carries a home %q", r.Home)
		}
		return nil
	case ModeConfined, ModeCredentialed:
		// Identical privilege gate for both drop tiers: never root, never a
		// non-positive id. The tier changes only the runtime treatment, never this.
		if r.UID <= 0 || r.GID <= 0 {
			return fmt.Errorf("privsep: refusing privilege drop to uid:gid %d:%d — must be >0 (never root)", r.UID, r.GID)
		}
		// Credentialed roots HOME + cwd at this path and the gateway never tightens
		// it, so the seam refuses a non-absolute or root ("/") home outright (#100:
		// don't trust how the value was built — a relative path or "/" reaching
		// cmd.Dir/HOME is a botched verdict, not a silent run).
		if r.Mode == ModeCredentialed && (!filepath.IsAbs(r.Home) || filepath.Clean(r.Home) == "/") {
			return fmt.Errorf("privsep: credentialed account home must be an absolute path other than \"/\" (got %q)", r.Home)
		}
		return nil
	default:
		return fmt.Errorf("privsep: unknown account mode %d", int(r.Mode))
	}
}
