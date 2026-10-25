package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/state"
)

const (
	// eventClearpassSpawn is the hub event and log `event` for a spawn the
	// verifier tolerated on a mismatch because a clearpass window was open.
	eventClearpassSpawn = "plugin.clearpass_spawn"
	// eventClearpassClosed is the hub event and log `event` for a window the
	// gateway closed by committing the live bytes as the attestation.
	eventClearpassClosed = "plugin.clearpass_closed"

	auditOpClearpassOpen  = "clearpass_open"
	auditOpClearpassSpawn = "clearpass_spawn"
	auditOpClearpassClose = "clearpass_close"

	// Outcomes of a clearpass_close row. A close is two-phase: "pending" is
	// written before .checksums changes and consumes the window; "ok" is
	// written only after the commit landed; "error" is written when the commit
	// failed after "pending" and reopens the window for the next sweep.
	// "superseded" is plugin lock clearing a window. "refused" records the
	// closer declining to commit (replay, unrecorded, stale, unwritable) and
	// changes nothing.
	closePending    = "pending"
	closeOK         = "ok"
	closeError      = "error"
	closeSuperseded = "superseded"
	closeRefused    = "refused"

	// clearpassCommitGrace is how long after its deadline a window may still be
	// committed by the closer. It covers a gateway that was down at the deadline
	// for up to a day; beyond that the operator re-opens or locks. Together with
	// the audit trail it bounds how old a replayed record could be and still
	// turn into an attestation.
	clearpassCommitGrace = config.MaxClearpass

	// clearpassSweepEvery is how often the gateway looks for expired windows.
	// A window is a matter of hours; a minute's lag at close is invisible.
	clearpassSweepEvery = time.Minute
	// clearpassAuditScan bounds the audit rows read back to count a window's
	// spawns. A window lasts at most a day; this is far more spawns than that.
	clearpassAuditScan = 10000
)

// clearpassAudit is the slice of the state store the closer records to and
// reads back from. Defined here, where it is used.
type clearpassAudit interface {
	AppendVaultAudit(ctx context.Context, ev state.VaultAuditEvent) error
	ListVaultAudit(ctx context.Context, principal string, limit int) ([]state.VaultAuditRow, error)
}

// clearpassCommit is what closing a window did: the attestation before, the
// attestation now, and the window that allowed it.
type clearpassCommit struct {
	Plugin    string
	Window    config.Clearpass
	Previous  config.PluginFingerprint
	Committed config.PluginFingerprint
}

// Changed reports whether the committed bytes differ from the previous
// attestation. An unchanged commit means the window passed with no swap.
func (c clearpassCommit) Changed() bool {
	return c.Previous.ManifestHash != c.Committed.ManifestHash ||
		c.Previous.EntrypointHash != c.Committed.EntrypointHash
}

// clearpassIDTag is how every clearpass audit row begins, so the closer can
// find a window's own rows: "id=<id> ...".
func clearpassIDTag(id string) string { return "id=" + id }

// clearpassRowHasID reports whether an audit row's detail is about window id.
func clearpassRowHasID(detail, id string) bool {
	tag := clearpassIDTag(id)
	return detail == tag || strings.HasPrefix(detail, tag+" ")
}

// clearpassOpenDetail is the audit detail written when a window opens.
func clearpassOpenDetail(cp config.Clearpass) string {
	return fmt.Sprintf("%s until %s note=%q", clearpassIDTag(cp.ID), cp.Until, cp.Note)
}

// commitClearpass closes the named plugin's window by recording the live bytes
// as its attestation. It is the one writer shared by the gateway closer and the
// CLI `--close`, and by construction clears the window: MergePluginFingerprint
// replaces the whole record and the new one carries no Clearpass.
//
// The close is two-phase in the audit log so a window is never left open to
// replay and never used up by a write that did not happen:
//
//  1. probe that .checksums can be written; a read-only config directory stops
//     here with nothing recorded, so the window is still there to close later;
//  2. rec.pending writes the "pending" close row, which consumes the window; a
//     close that cannot be recorded commits nothing;
//  3. .checksums is rewritten; on failure rec.failed writes the "error" row,
//     which reopens the window for the next attempt;
//  4. rec.committed writes the "ok" row, the only one that says "committed".
func commitClearpass(dir string, recorded config.PluginFingerprint, current config.ResolvedPlugin, nonce []byte, rec clearpassRecorder) (clearpassCommit, error) {
	if recorded.Clearpass == nil {
		return clearpassCommit{}, fmt.Errorf("plugin %q has no clearpass to close", recorded.Name)
	}
	live, err := config.ComputePluginFingerprint(current, nonce)
	if err != nil {
		return clearpassCommit{}, fmt.Errorf("clearpass close: %w", err)
	}
	commit := clearpassCommit{Plugin: recorded.Name, Window: *recorded.Clearpass, Previous: recorded, Committed: live}
	if err := config.ProbeChecksumsWritable(dir); err != nil {
		return clearpassCommit{}, fmt.Errorf("%w: %v; close it from a writable one-shot with 'ductile plugin clearpass %s --close' before the deadline",
			errClearpassUnwritable, err, recorded.Name)
	}
	manifest, err := config.LoadChecksums(dir)
	if err != nil {
		return clearpassCommit{}, fmt.Errorf("clearpass close: %w", err)
	}
	if err := rec.pending(commit); err != nil {
		return clearpassCommit{}, fmt.Errorf("clearpass close not recorded, so nothing was committed: %w", err)
	}
	merged := config.MergePluginFingerprint(manifest.PluginFingerprints, live)
	if err := config.WritePluginFingerprints(dir, merged, false); err != nil {
		werr := fmt.Errorf("clearpass close: write .checksums: %w", err)
		rec.failed(commit, werr)
		return clearpassCommit{}, werr
	}
	rec.committed(commit)
	return commit, nil
}

// clearpassRecorder writes the audit rows of a two-phase close. pending must
// succeed before .checksums changes; failed and committed describe what then
// happened and are best-effort (a lost row is logged, never fatal).
type clearpassRecorder struct {
	pending   func(clearpassCommit) error
	failed    func(clearpassCommit, error)
	committed func(clearpassCommit)
}

// errClearpassUnwritable marks a close that could not start because the config
// directory is read-only. Nothing is recorded, so the window stays closable.
var errClearpassUnwritable = errors.New("clearpass cannot be committed here")

// clearpassPendingDetail is the detail of a pending close row. It says what
// will be committed, never that it was.
func clearpassPendingDetail(commit clearpassCommit) string {
	return fmt.Sprintf("%s plugin %q closing: will commit entrypoint %s (was %s), manifest %s (was %s)",
		clearpassIDTag(commit.Window.ID), commit.Plugin,
		config.ShortHash(commit.Committed.EntrypointHash), config.ShortHash(commit.Previous.EntrypointHash),
		config.ShortHash(commit.Committed.ManifestHash), config.ShortHash(commit.Previous.ManifestHash))
}

// clearpassCloser is the gateway's side of a clearpass (card #144): it reports
// each tolerated spawn, and when a window's deadline passes it commits the live
// bytes as the attestation and says what it accepted. It runs at boot and then
// once a minute, so a window closes on time whether or not the plugin spawned,
// and a gateway that was down at the deadline closes it on the next start.
type clearpassCloser struct {
	registry  *plugin.Registry
	configDir string
	nonceSrc  fingerprintNonceSource
	logger    *slog.Logger
	hub       *events.Hub
	audit     clearpassAudit
	now       func() time.Time
	// refused remembers which (window, reason) refusals were already audited.
	// Only sweep touches it, and sweep runs on one goroutine.
	refused map[string]bool
}

func newClearpassCloser(registry *plugin.Registry, configDir string, nonceSrc fingerprintNonceSource, logger *slog.Logger, hub *events.Hub, audit clearpassAudit) *clearpassCloser {
	if logger == nil {
		logger = slog.Default()
	}
	return &clearpassCloser{registry: registry, configDir: configDir, nonceSrc: nonceSrc, logger: logger, hub: hub, audit: audit, now: time.Now}
}

// reportSpawn is the verifier's reporter: one WARN line, one hub event and one
// audit row per tolerated spawn. It is a WARN, not a SECURITY line, because the
// operator asked for the window; the hashes are in the line so a swap the
// operator did not intend is still visible.
func (c *clearpassCloser) reportSpawn(s clearpassSpawn) {
	detail := fmt.Sprintf("%s clearpass until %s: entrypoint recorded %s live %s, manifest recorded %s live %s",
		clearpassIDTag(s.Window.ID), s.Window.Until,
		config.ShortHash(s.Recorded.EntrypointHash), config.ShortHash(s.Live.EntrypointHash),
		config.ShortHash(s.Recorded.ManifestHash), config.ShortHash(s.Live.ManifestHash))
	c.logger.Warn("plugin fingerprint mismatch at spawn accepted under an open clearpass",
		"event", eventClearpassSpawn, "plugin", s.Plugin, "until", s.Window.Until, "note", s.Window.Note,
		"entrypoint_recorded", config.ShortHash(s.Recorded.EntrypointHash), "entrypoint_live", config.ShortHash(s.Live.EntrypointHash),
		"manifest_recorded", config.ShortHash(s.Recorded.ManifestHash), "manifest_live", config.ShortHash(s.Live.ManifestHash))
	if c.hub != nil {
		c.hub.Publish(eventClearpassSpawn, map[string]any{
			"plugin": s.Plugin, "until": s.Window.Until, "note": s.Window.Note, "detail": detail,
		})
	}
	c.appendAudit(auditOpClearpassSpawn, s.Plugin, "ok", detail)
}

// run sweeps at once and then every interval until ctx is done.
func (c *clearpassCloser) run(ctx context.Context, every time.Duration) {
	c.sweep(ctx, true)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sweep(ctx, false)
		}
	}
}

// sweep closes every expired window and, on the first pass, names every open
// one so the boot log says what the gateway will accept.
func (c *clearpassCloser) sweep(ctx context.Context, announce bool) {
	manifest, err := config.LoadChecksums(c.configDir)
	if err != nil {
		c.logger.Error("clearpass sweep cannot read .checksums", "error", err)
		return
	}
	now := c.now()
	var nonce []byte
	for _, fp := range manifest.PluginFingerprints {
		if fp.Clearpass == nil {
			continue
		}
		if nonce == nil {
			nonce, err = c.nonceSrc.FingerprintNonce()
			if err != nil {
				c.logger.Error("clearpass sweep cannot read the vault nonce", "error", err)
				return
			}
		}
		switch {
		case !fp.Clearpass.Verify(fp, nonce):
			// Never commit under a window the vault did not seal: that would let a
			// .checksums write turn into an attestation of arbitrary bytes.
			c.logger.Error("clearpass record does not verify under the vault nonce; ignored, the plugin stays denied until it is re-opened or locked",
				"event", eventClearpassClosed, "plugin", fp.Name, "until", fp.Clearpass.Until)
		case fp.Clearpass.Open(now):
			if announce {
				c.logger.Info("clearpass open: changed bytes for this plugin will be accepted and committed at close",
					"plugin", fp.Name, "until", fp.Clearpass.Until, "note", fp.Clearpass.Note)
			}
			if werr := config.ProbeChecksumsWritable(c.configDir); werr != nil {
				c.logger.Warn("clearpass open but this gateway cannot commit it: the config directory is not writable; close it from a writable one-shot before the deadline, or the plugin is denied after it",
					"event", eventClearpassClosed, "plugin", fp.Name, "until", fp.Clearpass.Until,
					"close_with", "ductile plugin clearpass "+fp.Name+" --close", "error", werr)
			}
		case fp.Clearpass.Expired(now):
			c.closeOne(ctx, fp)
		}
	}
}

// errClearpassNotCommittable marks a window the closer will not turn into an
// attestation: one it has no record of being opened, one already closed, or
// one expired beyond the grace. It is not retried; the operator re-opens or
// locks.
var errClearpassNotCommittable = errors.New("clearpass not committed")

// closeOne commits one expired window and reports the outcome. A commit that
// fails before its pending row (read-only config, unreadable vault) or after
// it (the .checksums write) leaves the window closable, so the next sweep
// tries again; only a refusal from admit is final. Refusals and failures are
// audited once per window and reason, not every minute.
func (c *clearpassCloser) closeOne(ctx context.Context, recorded config.PluginFingerprint) {
	var observed int
	var detail string
	id := recorded.Clearpass.ID
	rec := clearpassRecorder{
		pending: func(commit clearpassCommit) error {
			return c.appendAuditErr(auditOpClearpassClose, commit.Plugin, closePending, clearpassPendingDetail(commit))
		},
		failed: func(commit clearpassCommit, err error) {
			c.appendAudit(auditOpClearpassClose, commit.Plugin, closeError, clearpassIDTag(id)+" commit failed, window reopened: "+err.Error())
		},
		committed: func(commit clearpassCommit) {
			observed = c.observedSpawns(ctx, commit)
			detail = clearpassCloseDetail(commit, observed)
			c.appendAudit(auditOpClearpassClose, commit.Plugin, closeOK, detail)
		},
	}
	commit, err := c.admit(ctx, recorded)
	if err == nil {
		commit, err = c.commit(recorded, rec)
	}
	if err != nil {
		c.logger.Error("clearpass close failed; the plugin is denied until it is committed or locked",
			"event", eventClearpassClosed, "plugin", recorded.Name, "until", recorded.Clearpass.Until, "error", err)
		if errors.Is(err, errClearpassNotCommittable) || errors.Is(err, errClearpassUnwritable) {
			c.auditRefusalOnce(recorded.Name, id, err)
		}
		return
	}
	c.logger.Info("clearpass closed: "+detail,
		"event", eventClearpassClosed, "plugin", commit.Plugin, "changed", commit.Changed(), "observed_spawns", observed,
		"entrypoint_previous", config.ShortHash(commit.Previous.EntrypointHash), "entrypoint_committed", config.ShortHash(commit.Committed.EntrypointHash),
		"manifest_previous", config.ShortHash(commit.Previous.ManifestHash), "manifest_committed", config.ShortHash(commit.Committed.ManifestHash))
	if c.hub != nil {
		c.hub.Publish(eventClearpassClosed, map[string]any{
			"plugin": commit.Plugin, "changed": commit.Changed(), "observed_spawns": observed, "detail": detail,
		})
	}
}

// auditRefusalOnce records a refusal the first time it is seen for a window,
// so a window stuck on a read-only gateway does not write a row every minute.
func (c *clearpassCloser) auditRefusalOnce(plugin, id string, err error) {
	// Key on the kind of refusal, not its text: the text carries a probe file
	// name and an age that change every sweep.
	kind := "not-committable"
	if errors.Is(err, errClearpassUnwritable) {
		kind = "unwritable"
	}
	key := id + "\x00" + kind
	if c.refused == nil {
		c.refused = make(map[string]bool)
	}
	if c.refused[key] {
		return
	}
	c.refused[key] = true
	c.appendAudit(auditOpClearpassClose, plugin, closeRefused, clearpassIDTag(id)+" "+err.Error())
}

// admit decides whether an expired window may be committed: its deadline must
// be within the grace, and the audit trail must show it opened and not yet
// closed. This is what stops a sealed record replayed from a backup, or one
// already consumed, from attesting whatever is on disk.
func (c *clearpassCloser) admit(ctx context.Context, recorded config.PluginFingerprint) (clearpassCommit, error) {
	cp := recorded.Clearpass
	deadline, err := cp.Deadline()
	if err != nil {
		return clearpassCommit{}, fmt.Errorf("%w: %v", errClearpassNotCommittable, err)
	}
	if late := c.now().Sub(deadline); late > clearpassCommitGrace {
		return clearpassCommit{}, fmt.Errorf("%w: window expired %s ago, beyond the %s grace; re-open it or lock the plugin",
			errClearpassNotCommittable, late.Truncate(time.Minute), clearpassCommitGrace)
	}
	if c.audit == nil {
		return clearpassCommit{}, fmt.Errorf("%w: no audit log to confirm the window was opened", errClearpassNotCommittable)
	}
	rows, err := c.audit.ListVaultAudit(ctx, recorded.Name, clearpassAuditScan)
	if err != nil {
		return clearpassCommit{}, fmt.Errorf("%w: cannot read the audit log: %v", errClearpassNotCommittable, err)
	}
	// Rows come newest first. The newest close row that changes state decides:
	// pending, ok or superseded mean the window is consumed; error means a
	// commit failed after pending and the window is open again. A refused row
	// changes nothing, so it can never reopen a window.
	opened, closed, decided := false, false, false
	for _, r := range rows {
		if !clearpassRowHasID(r.Detail, cp.ID) {
			continue
		}
		switch r.Op {
		case auditOpClearpassOpen:
			opened = true
		case auditOpClearpassClose:
			if decided || r.Outcome == closeRefused {
				continue
			}
			decided = true
			closed = r.Outcome != closeError
		}
	}
	switch {
	case closed:
		return clearpassCommit{}, fmt.Errorf("%w: the audit log shows window %s already closed; a replayed record is not re-committed", errClearpassNotCommittable, cp.ID)
	case !opened:
		return clearpassCommit{}, fmt.Errorf("%w: the audit log has no record of window %s being opened", errClearpassNotCommittable, cp.ID)
	}
	return clearpassCommit{}, nil
}

func (c *clearpassCloser) commit(recorded config.PluginFingerprint, rec clearpassRecorder) (clearpassCommit, error) {
	p, ok := c.registry.Get(recorded.Name)
	if !ok {
		return clearpassCommit{}, fmt.Errorf("plugin %q is not in the live registry", recorded.Name)
	}
	nonce, err := c.nonceSrc.FingerprintNonce()
	if err != nil {
		return clearpassCommit{}, err
	}
	current := config.ResolvedPlugin{
		Name:           recorded.Name,
		Enabled:        recorded.Enabled,
		Uses:           recorded.Uses,
		ManifestPath:   filepath.Join(p.Path, "manifest.yaml"),
		EntrypointPath: p.Entrypoint,
	}
	return commitClearpass(c.configDir, recorded, current, nonce, rec)
}

// observedSpawns counts the window's tolerated spawns from the audit log, which
// survives restarts; a count of -1 means the log could not be read.
func (c *clearpassCloser) observedSpawns(ctx context.Context, commit clearpassCommit) int {
	if c.audit == nil {
		return 0
	}
	opened, err := commit.Window.Opened()
	if err != nil {
		return -1
	}
	rows, err := c.audit.ListVaultAudit(ctx, commit.Plugin, clearpassAuditScan)
	if err != nil {
		c.logger.Warn("clearpass close could not count observed spawns", "plugin", commit.Plugin, "error", err)
		return -1
	}
	n := 0
	for _, r := range rows {
		if r.Op != auditOpClearpassSpawn {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
		if err != nil || at.Before(opened) {
			continue
		}
		n++
	}
	return n
}

// appendAudit is the best-effort form for rows that only describe what already
// happened (a tolerated spawn, a refused close): a lost row is logged loudly,
// it does not change the outcome.
func (c *clearpassCloser) appendAudit(op, principal, outcome, detail string) {
	if err := c.appendAuditErr(op, principal, outcome, detail); err != nil {
		c.logger.Error("vault audit write failed", "op", op, "plugin", principal, "error", err)
	}
}

// appendAuditErr returns the write error for rows that gate an action: the
// close row consumes the window, so it must land before the commit.
func (c *clearpassCloser) appendAuditErr(op, principal, outcome, detail string) error {
	if c.audit == nil {
		return errors.New("no audit log")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.audit.AppendVaultAudit(ctx, state.VaultAuditEvent{
		Op: op, Principal: principal, Actor: "core", Outcome: outcome, Detail: detail,
	})
}

// clearpassCloseDetail is the one-line account of a close, shared by the log,
// the hub event, the audit row and the CLI.
func clearpassCloseDetail(commit clearpassCommit, observed int) string {
	spawns := "never spawned"
	switch {
	case observed < 0:
		spawns = "observed spawns unknown"
	case observed == 1:
		spawns = "observed in 1 spawn"
	case observed > 1:
		spawns = fmt.Sprintf("observed in %d spawns", observed)
	}
	if !commit.Changed() {
		return fmt.Sprintf("%s plugin %q unchanged (entrypoint %s, manifest %s), %s; window %s to %s",
			clearpassIDTag(commit.Window.ID), commit.Plugin, config.ShortHash(commit.Committed.EntrypointHash), config.ShortHash(commit.Committed.ManifestHash),
			spawns, commit.Window.OpenedAt, commit.Window.Until)
	}
	return fmt.Sprintf("%s plugin %q committed entrypoint %s (was %s), manifest %s (was %s), %s; window %s to %s",
		clearpassIDTag(commit.Window.ID), commit.Plugin,
		config.ShortHash(commit.Committed.EntrypointHash), config.ShortHash(commit.Previous.EntrypointHash),
		config.ShortHash(commit.Committed.ManifestHash), config.ShortHash(commit.Previous.ManifestHash),
		spawns, commit.Window.OpenedAt, commit.Window.Until)
}
