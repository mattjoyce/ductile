package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
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

	auditOpClearpassSpawn = "clearpass_spawn"
	auditOpClearpassClose = "clearpass_close"

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

// commitClearpass closes the named plugin's window by recording the live bytes
// as its attestation. It is the one writer shared by the gateway closer and the
// CLI `--close`, and by construction clears the window: MergePluginFingerprint
// replaces the whole record and the new one carries no Clearpass.
func commitClearpass(dir string, recorded config.PluginFingerprint, current config.ResolvedPlugin, nonce []byte) (clearpassCommit, error) {
	if recorded.Clearpass == nil {
		return clearpassCommit{}, fmt.Errorf("plugin %q has no clearpass to close", recorded.Name)
	}
	live, err := config.ComputePluginFingerprint(current, nonce)
	if err != nil {
		return clearpassCommit{}, fmt.Errorf("clearpass close: %w", err)
	}
	manifest, err := config.LoadChecksums(dir)
	if err != nil {
		return clearpassCommit{}, fmt.Errorf("clearpass close: %w", err)
	}
	merged := config.MergePluginFingerprint(manifest.PluginFingerprints, live)
	if err := config.WritePluginFingerprints(dir, merged, false); err != nil {
		return clearpassCommit{}, fmt.Errorf("clearpass close: write .checksums: %w", err)
	}
	return clearpassCommit{Plugin: recorded.Name, Window: *recorded.Clearpass, Previous: recorded, Committed: live}, nil
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
	detail := fmt.Sprintf("clearpass until %s: entrypoint recorded %s live %s, manifest recorded %s live %s",
		s.Window.Until,
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
		case fp.Clearpass.Expired(now):
			c.closeOne(ctx, fp)
		}
	}
}

// closeOne commits one expired window and reports the outcome. A commit that
// fails leaves the window in place: the verifier then denies the plugin and
// names the expired window, and the next sweep tries again.
func (c *clearpassCloser) closeOne(ctx context.Context, recorded config.PluginFingerprint) {
	commit, err := c.commit(recorded)
	if err != nil {
		c.logger.Error("clearpass close failed; the plugin is denied until it is committed or locked",
			"event", eventClearpassClosed, "plugin", recorded.Name, "until", recorded.Clearpass.Until, "error", err)
		c.appendAudit(auditOpClearpassClose, recorded.Name, "error", err.Error())
		return
	}
	observed := c.observedSpawns(ctx, commit)
	detail := clearpassCloseDetail(commit, observed)
	c.logger.Info("clearpass closed: "+detail,
		"event", eventClearpassClosed, "plugin", commit.Plugin, "changed", commit.Changed(), "observed_spawns", observed,
		"entrypoint_previous", config.ShortHash(commit.Previous.EntrypointHash), "entrypoint_committed", config.ShortHash(commit.Committed.EntrypointHash),
		"manifest_previous", config.ShortHash(commit.Previous.ManifestHash), "manifest_committed", config.ShortHash(commit.Committed.ManifestHash))
	if c.hub != nil {
		c.hub.Publish(eventClearpassClosed, map[string]any{
			"plugin": commit.Plugin, "changed": commit.Changed(), "observed_spawns": observed, "detail": detail,
		})
	}
	c.appendAudit(auditOpClearpassClose, commit.Plugin, "ok", detail)
}

func (c *clearpassCloser) commit(recorded config.PluginFingerprint) (clearpassCommit, error) {
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
	return commitClearpass(c.configDir, recorded, current, nonce)
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

func (c *clearpassCloser) appendAudit(op, principal, outcome, detail string) {
	if c.audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.audit.AppendVaultAudit(ctx, state.VaultAuditEvent{
		Op: op, Principal: principal, Actor: "core", Outcome: outcome, Detail: detail,
	}); err != nil {
		c.logger.Error("vault audit write failed", "op", op, "plugin", principal, "error", err)
	}
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
		return fmt.Sprintf("plugin %q unchanged (entrypoint %s, manifest %s), %s; window %s to %s",
			commit.Plugin, config.ShortHash(commit.Committed.EntrypointHash), config.ShortHash(commit.Committed.ManifestHash),
			spawns, commit.Window.OpenedAt, commit.Window.Until)
	}
	return fmt.Sprintf("plugin %q committed entrypoint %s (was %s), manifest %s (was %s), %s; window %s to %s",
		commit.Plugin,
		config.ShortHash(commit.Committed.EntrypointHash), config.ShortHash(commit.Previous.EntrypointHash),
		config.ShortHash(commit.Committed.ManifestHash), config.ShortHash(commit.Previous.ManifestHash),
		spawns, commit.Window.OpenedAt, commit.Window.Until)
}
