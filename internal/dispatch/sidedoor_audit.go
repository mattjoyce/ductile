package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/mattjoyce/ductile/internal/config"
)

// The tier-aware root side-door audit (#111). Privsep protects secret zero by
// dropping a plugin to an account that cannot read the age key. That wall is only
// real if the drop account has no OTHER path to host root. This audit probes each
// configured drop account at boot for such "side-doors" and reacts by tier:
//
//   - CONFINED account with a side-door  -> the wall is a LIE (a dropped plugin can
//     escalate). SECURITY warn; under strict mode, fail the boot CLOSED.
//   - CREDENTIALED account with a side-door -> root-equivalent AS DESIGNED (the
//     trusted tier acts as the operator). Informed-consent warn; ALWAYS proceed.
//
// Crucially, "I could not determine" is NOT "clean". A probe that errors, a sudoers
// query that cannot run, a uid with no login name, or an unsupported platform all
// make the account INCONCLUSIVE — which is reported (never silent) and, for a
// CONFINED account under strict mode, fails the boot CLOSED: strict mode means
// "refuse when containment cannot be proven." False positives still never brick a
// NON-strict boot (the default), which only ever warns.

// dangerousGroups maps a supplementary group to why its membership is an uncontested
// path to host root, INDEPENDENT of sudo: the docker/lxd/incus control sockets each
// let a member start a container that mounts the host filesystem as root
// (`docker run -v /:/host ...`). Proven live on the Dell 2026-06-08. Not exhaustive
// (libvirt/kvm patterns exist too) — absence from this set is not proof of safety.
var dangerousGroups = map[string]string{
	"docker": "docker socket: a container can mount the host fs as root",
	"lxd":    "lxd socket: container -> host root",
	"incus":  "incus socket: container -> host root",
}

// osLookup is the seam for the host probes the audit needs. The real implementation
// (newOSLookup) reads live OS state; tests inject a fake so the audit logic is
// verifiable without a populated host. Contract: a method's non-nil error means
// "could not determine" — the audit records it and treats the account as
// INCONCLUSIVE, never as "safe". A clean (nil-error) negative is a real negative.
type osLookup interface {
	// UsernameForUID resolves a uid to its login name; ok=false if no such user
	// (which makes the sudo probe inconclusive, since it is keyed by name).
	UsernameForUID(uid int) (name string, ok bool)
	// GroupNamesForUID returns the account's group names (primary + supplementary).
	GroupNamesForUID(uid int) ([]string, error)
	// SudoNoPasswd reports whether the user has ANY NOPASSWD sudo entry
	// (`sudo -n -l -U <user>`). sudo-not-installed is a clean negative (false,nil);
	// a non-nil error (cannot query, timed out) is inconclusive.
	SudoNoPasswd(username string) (bool, error)
	// WritablePathDirs returns standard secure_path directories the account can
	// write to (a binary-hijack surface). An error means a directory that exists
	// could not be inspected (inconclusive), not "none found".
	WritablePathDirs(uid int) ([]string, error)
	// WritableSetuidRoot returns setuid-root binaries the account can overwrite —
	// directly, or by writing their parent directory (rename/replace). An error is
	// inconclusive.
	WritableSetuidRoot(uid int) ([]string, error)
}

// sideDoorFindings is the per-account probe result.
type sideDoorFindings struct {
	NoPasswdSudo   bool     // has a NOPASSWD sudo entry
	DangerGroups   []string // resolved dangerous group names (docker/lxd/incus)
	WritablePath   []string // secure_path dirs the account can write
	WritableSetuid []string // setuid-root binaries the account can overwrite
	probeErrors    []string // a probe could not determine its answer -> inconclusive
}

// any reports a POSITIVE escalation finding (a side-door that definitely exists).
func (f sideDoorFindings) any() bool {
	return f.NoPasswdSudo ||
		len(f.DangerGroups) > 0 ||
		len(f.WritablePath) > 0 ||
		len(f.WritableSetuid) > 0
}

// inconclusive reports that at least one probe could not determine its answer, so
// the absence of a positive finding does NOT prove containment.
func (f sideDoorFindings) inconclusive() bool { return len(f.probeErrors) > 0 }

// gatherSideDoors runs all four probes for one account uid. It never panics or
// returns an error: a probe that fails is recorded in probeErrors (making the
// account inconclusive) and contributes no positive finding.
func gatherSideDoors(uid int, look osLookup) sideDoorFindings {
	var f sideDoorFindings

	if groups, err := look.GroupNamesForUID(uid); err != nil {
		f.probeErrors = append(f.probeErrors, "group lookup: "+err.Error())
	} else {
		for _, g := range groups {
			if _, bad := dangerousGroups[g]; bad {
				f.DangerGroups = append(f.DangerGroups, g)
			}
		}
		sort.Strings(f.DangerGroups)
	}

	if username, ok := look.UsernameForUID(uid); ok {
		switch np, serr := look.SudoNoPasswd(username); {
		case serr != nil:
			f.probeErrors = append(f.probeErrors, "sudo check: "+serr.Error())
		case np:
			f.NoPasswdSudo = true
		}
	} else {
		f.probeErrors = append(f.probeErrors,
			fmt.Sprintf("no login name for uid %d (sudo not checked)", uid))
	}

	if dirs, err := look.WritablePathDirs(uid); err != nil {
		f.probeErrors = append(f.probeErrors, "writable-path check: "+err.Error())
	} else {
		f.WritablePath = dirs
	}

	if files, err := look.WritableSetuidRoot(uid); err != nil {
		f.probeErrors = append(f.probeErrors, "setuid-root check: "+err.Error())
	} else {
		f.WritableSetuid = files
	}

	return f
}

// sideDoorVerdict is the tier reactor's pure decision for one account.
type sideDoorVerdict struct {
	report   bool       // there is something to emit (a finding OR inconclusive)
	failBoot bool       // the boot must be refused
	level    slog.Level // log level for the emitted message
	headline string     // the human-facing security message
}

// classifySideDoor applies the tier-aware policy to a set of findings. It is pure
// (no OS, no logging) so every branch is table-testable.
//
//   - clean + conclusive          -> nothing
//   - credentialed (finding/unsure) -> informed-consent WARN, ALWAYS proceed
//   - confined + positive finding  -> SECURITY WARN; fail CLOSED iff strict
//   - confined + inconclusive only -> "containment unproven" WARN; fail CLOSED iff
//     strict (strict means: refuse when the wall cannot be verified)
func classifySideDoor(f sideDoorFindings, mode AccountMode, failClosed bool) sideDoorVerdict {
	finding := f.any()
	unsure := f.inconclusive()
	if !finding && !unsure {
		return sideDoorVerdict{}
	}

	switch mode {
	case ModeCredentialed:
		// The trusted tier runs as the operator by design; a side-door is the
		// accepted risk, on the record. It is never failed closed, even under strict
		// and even when inconclusive (there is no wall to verify).
		headline := "credentialed (trusted) account has a host root side-door — root-equivalent AS DESIGNED (acts as the operator); recording informed consent"
		if !finding {
			headline = "credentialed (trusted) account: side-door probe INCONCLUSIVE — not verified; proceeding (trusted tier is root-equivalent by design)"
		}
		return sideDoorVerdict{report: true, level: slog.LevelWarn, headline: headline}

	case ModeConfined:
		headline := "confined account has a host root side-door — the privsep wall is NOT real for it (a dropped plugin can escalate to root)"
		if !finding {
			headline = "confined account: could NOT verify the absence of a host root side-door (probe inconclusive) — containment UNPROVEN"
		}
		v := sideDoorVerdict{report: true, level: slog.LevelWarn, headline: headline}
		if failClosed {
			v.failBoot = true
			v.level = slog.LevelError
		}
		return v

	default:
		// ModeUnconfined has no account entry so should not reach here; treat any
		// surprise as the safe (confined) interpretation rather than swallow it.
		v := sideDoorVerdict{report: true, level: slog.LevelWarn,
			headline: "account with an unexpected privilege mode has a host root side-door or inconclusive probe — treating as confined"}
		if failClosed {
			v.failBoot = true
			v.level = slog.LevelError
		}
		return v
	}
}

// AuditAccountSideDoors probes every configured drop account for host root
// side-doors at boot and reacts by tier (#111). failClosed (strict mode) promotes a
// CONFINED account's side-door OR an inconclusive probe to a boot refusal;
// credentialed accounts always proceed (informed consent). Returns nil when nothing
// must fail the boot.
func AuditAccountSideDoors(cfg *config.Config, failClosed bool, logger *slog.Logger) error {
	return auditAccountSideDoors(cfg, failClosed, logger, newOSLookup())
}

// auditAccountSideDoors is the testable core — the boot wrapper supplies the live
// OSLookup; tests supply a fake.
func auditAccountSideDoors(cfg *config.Config, failClosed bool, logger *slog.Logger, look osLookup) error {
	if cfg == nil || look == nil {
		return nil
	}

	names := make([]string, 0, len(cfg.Accounts))
	for name := range cfg.Accounts {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic logs + tests

	var failed []string
	for _, name := range names {
		// Single-source the tier: configuredAccount is THE construction point for a
		// dropping account (account.go). Never re-derive the tier from raw config —
		// that drift is the bug the grill caught here and in mostRestrictedAccount.
		ra := configuredAccount(name, cfg.Accounts[name], AccountDefault)
		if err := ra.Validate(); err != nil {
			// A malformed account (e.g. uid 0) is refused at the drop seam anyway;
			// surface it here rather than probe a nonsense uid into a false clean.
			if logger != nil {
				logger.Warn("privsep SECURITY: account is misconfigured — cannot audit its side-doors (it will be refused at drop)",
					"account", name, "error", err.Error())
			}
			continue
		}

		f := gatherSideDoors(ra.UID, look)
		v := classifySideDoor(f, ra.Mode, failClosed)
		if !v.report {
			continue
		}
		if logger != nil {
			logger.LogAttrs(context.Background(), v.level,
				"privsep SECURITY: "+v.headline,
				sideDoorLogAttrs(name, ra, f)...)
		}
		if v.failBoot {
			failed = append(failed, name)
		}
	}

	if len(failed) > 0 {
		return fmt.Errorf("privsep side-door audit: confined account(s) %s hold a host root side-door (or could not be verified) and strict mode is on — refusing to boot a wall that cannot contain them", strings.Join(failed, ", "))
	}
	return nil
}

// sideDoorLogAttrs renders one account's findings as structured slog attributes,
// matching the existing privsep boot-log style (account/uid keyed warnings).
func sideDoorLogAttrs(name string, ra ResolvedAccount, f sideDoorFindings) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("account", name),
		slog.Int("uid", ra.UID),
		slog.String("mode", ra.Mode.String()),
	}
	if f.NoPasswdSudo {
		attrs = append(attrs, slog.Bool("nopasswd_sudo", true))
	}
	if len(f.DangerGroups) > 0 {
		attrs = append(attrs, slog.String("root_groups", strings.Join(f.DangerGroups, ",")))
		risks := make([]string, 0, len(f.DangerGroups))
		for _, g := range f.DangerGroups {
			risks = append(risks, g+" ("+dangerousGroups[g]+")")
		}
		attrs = append(attrs, slog.String("root_group_risk", strings.Join(risks, "; ")))
	}
	if len(f.WritablePath) > 0 {
		attrs = append(attrs, slog.String("writable_path_dirs", strings.Join(f.WritablePath, ",")))
	}
	if len(f.WritableSetuid) > 0 {
		attrs = append(attrs, slog.String("writable_setuid_root", strings.Join(f.WritableSetuid, ",")))
	}
	if f.inconclusive() {
		attrs = append(attrs,
			slog.Bool("inconclusive", true),
			slog.String("probe_errors", strings.Join(f.probeErrors, "; ")))
	}
	attrs = append(attrs, slog.String("note",
		"detection is best-effort; absence of a finding is not proof of containment"))
	return attrs
}
