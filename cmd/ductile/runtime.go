package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mattjoyce/ductile/internal/api"
	"github.com/mattjoyce/ductile/internal/auth"
	"github.com/mattjoyce/ductile/internal/config"
	"github.com/mattjoyce/ductile/internal/configsnapshot"
	"github.com/mattjoyce/ductile/internal/dispatch"
	"github.com/mattjoyce/ductile/internal/doctor"
	"github.com/mattjoyce/ductile/internal/events"
	"github.com/mattjoyce/ductile/internal/lock"
	"github.com/mattjoyce/ductile/internal/log"
	"github.com/mattjoyce/ductile/internal/plugin"
	"github.com/mattjoyce/ductile/internal/queue"
	"github.com/mattjoyce/ductile/internal/relay"
	"github.com/mattjoyce/ductile/internal/router"
	"github.com/mattjoyce/ductile/internal/scheduler"
	"github.com/mattjoyce/ductile/internal/state"
	"github.com/mattjoyce/ductile/internal/storage"
	"github.com/mattjoyce/ductile/internal/vault"
	"github.com/mattjoyce/ductile/internal/webhook"
)

type runtimeState struct {
	cfg                    *config.Config
	configPath             string
	logger                 *slog.Logger
	registry               *plugin.Registry
	router                 router.Engine
	scheduler              *scheduler.Scheduler
	dispatcher             *dispatch.Dispatcher
	apiServer              *api.Server
	webhook                *webhook.Server
	ctx                    context.Context
	cancel                 context.CancelFunc
	wg                     sync.WaitGroup
	stopOnce               sync.Once
	stopDone               chan struct{}
	errCh                  chan error
	db                     *sql.DB
	configSource           string
	activeConfigSnapshotID string
}

type reloadManager struct {
	mu           sync.Mutex
	configPath   string
	configSource string
	runtime      *runtimeState
	errCh        chan error
	reloadFunc   func(context.Context) (api.ReloadResponse, error)
}

type runtimeBuildOptions struct {
	snapshotReason     string
	existingSnapshotID string
	// vaultOwner, when non-nil, is the vault owner already decrypted by the
	// load-time projection (config.LoadWithVault). buildRuntime reuses it as the
	// live owner instead of decrypting the blob again (#43 redundant decrypt; epic
	// #48 slice 2). nil — restore or no vault — falls back to a fresh load.
	vaultOwner *vault.Vault
}

func (rt *runtimeState) Stop() {
	if rt == nil {
		return
	}
	if rt.stopDone == nil {
		rt.stopDone = make(chan struct{})
	}
	rt.stopOnce.Do(func() {
		defer close(rt.stopDone)
		if rt.cancel != nil {
			rt.cancel()
		}
		if rt.scheduler != nil {
			rt.scheduler.Stop()
		}
		rt.wg.Wait()
		if rt.db != nil {
			// Refresh planner statistics on graceful shutdown so the next
			// run picks indexes against current row counts. Bounded by a
			// short timeout because rt.ctx is already cancelled and the
			// caller is waiting for shutdown to complete.
			octx, ocancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := rt.db.ExecContext(octx, "PRAGMA optimize;"); err != nil && rt.logger != nil {
				rt.logger.Warn("PRAGMA optimize on shutdown failed", "error", err)
			}
			ocancel()
			_ = rt.db.Close()
		}
	})
	<-rt.stopDone
}

func (rt *runtimeState) WaitListenersStopped(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	if rt.apiServer != nil {
		if err := rt.apiServer.WaitServeStopped(ctx); err != nil {
			return fmt.Errorf("api listener stopped: %w", err)
		}
	}
	if rt.webhook != nil {
		if err := rt.webhook.WaitServeStopped(ctx); err != nil {
			return fmt.Errorf("webhook listener stopped: %w", err)
		}
	}
	return nil
}

func newRuntimeContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	return ctx, func() {
		cancel(nil)
	}
}

func (rm *reloadManager) Stop() {
	rm.mu.Lock()
	rt := rm.runtime
	rm.runtime = nil
	rm.mu.Unlock()
	if rt == nil {
		return
	}
	rt.Stop()
}

func (rm *reloadManager) Reload(ctx context.Context) (api.ReloadResponse, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	oldRuntime := rm.runtime
	if oldRuntime == nil {
		return api.ReloadResponse{Status: "error", Message: "runtime not available"}, fmt.Errorf("runtime not available")
	}
	oldCfg := oldRuntime.cfg

	newCfg, newOwner, err := config.LoadWithVault(rm.configPath)
	if err != nil {
		return api.ReloadResponse{Status: "error", Message: err.Error()}, err
	}
	// P2-10: source the fail_on_drift policy from the RUNNING config (oldCfg),
	// not the proposed newCfg — otherwise an attacker could relax admission by
	// disabling it in the very reload they are trying to push.
	if err := verifyReloadIntegrity(rm.configPath, oldCfg.Service.AdmissionPolicy().FailOnDrift, newOwner); err != nil {
		return api.ReloadResponse{Status: "error", Message: err.Error()}, err
	}
	if err := validateReloadableFields(oldCfg, newCfg); err != nil {
		return api.ReloadResponse{Status: "error", Message: err.Error()}, err
	}

	oldRuntime.logger.Info("reloading config")

	if ctx == nil {
		ctx = context.Background()
	}

	go oldRuntime.Stop()

	listenerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := oldRuntime.WaitListenersStopped(listenerCtx); err != nil {
		rm.runtime = nil
		return api.ReloadResponse{Status: "error", Message: err.Error()}, err
	}

	runtime, err := buildRuntime(newCfg, rm.configPath, rm.configSource, rm.reloadFunc, rm.errCh, runtimeBuildOptions{
		snapshotReason: configsnapshot.ReasonReload,
		// Reuse the owner LoadWithVault already decrypted instead of letting
		// buildRuntime re-decrypt the blob via LoadVault. The start path already
		// threads its owner (#43 single-decrypt); the reload path regressed to a
		// double-decrypt (2026-06-06 branch review, Ousterhout 6c) — this closes it.
		vaultOwner: newOwner,
	})
	if err != nil {
		oldRuntime.logger.Error("reload failed; attempting to restore previous runtime", "error", err)
		restored, restoreErr := buildRuntime(oldCfg, rm.configPath, rm.configSource, rm.reloadFunc, rm.errCh, runtimeBuildOptions{
			existingSnapshotID: oldRuntime.activeConfigSnapshotID,
		})
		if restoreErr == nil {
			rm.runtime = restored
		} else {
			rm.runtime = nil
			err = fmt.Errorf("reload failed: %w; restore previous runtime: %v", err, restoreErr)
		}
		return api.ReloadResponse{Status: "error", Message: err.Error()}, err
	}

	rm.runtime = runtime

	return api.ReloadResponse{
		Status:     "ok",
		ReloadedAt: time.Now().UTC().Format(time.RFC3339),
		Message:    "configuration reloaded",
	}, nil
}

func validateReloadableFields(oldCfg, newCfg *config.Config) error {
	if oldCfg.State.Path != newCfg.State.Path {
		return fmt.Errorf("config reload rejected: changes to state.path require a full restart")
	}
	if oldCfg.API.Listen != newCfg.API.Listen {
		return fmt.Errorf("config reload rejected: changes to api.listen require a full restart")
	}
	oldWebhookListen := ""
	newWebhookListen := ""
	if oldCfg.Webhooks != nil {
		oldWebhookListen = oldCfg.Webhooks.Listen
	}
	if newCfg.Webhooks != nil {
		newWebhookListen = newCfg.Webhooks.Listen
	}
	if oldWebhookListen != newWebhookListen {
		return fmt.Errorf("config reload rejected: changes to webhooks.listen require a full restart")
	}
	return nil
}

func resolveConfigDir(configPath string) string {
	configDir := configPath
	if stat, err := os.Stat(configPath); err == nil && !stat.IsDir() {
		configDir = filepath.Dir(configPath)
	}
	return configDir
}

// verifyReloadIntegrity validates the on-disk config against the .checksums manifest
// and plugin fingerprint locks. When failOnDrift is true, operational tier warnings
// (e.g. config.yaml or routes.yaml drift) are promoted to admission failures so the
// reload is rejected. When failOnDrift is false, operational warnings stay warnings —
// only high-security file mismatches and plugin fingerprint mismatches reject. The
// failOnDrift flag should come from the RUNNING config's admission policy so that an
// attacker cannot relax policy via the very reload they are trying to push.
//
// owner, when non-nil, is the vault already decrypted for this boot/reload
// (config.LoadWithVault); it is threaded to the plugin-fingerprint verify so the
// attestation nonce is taken from that one snapshot instead of a second decrypt
// (#43 single-decrypt). A nil owner — the reload-restore path — falls back to a
// fresh vault load, preserving prior behaviour.
func verifyReloadIntegrity(configPath string, failOnDrift bool, owner *vault.Vault) error {
	configDir := resolveConfigDir(configPath)
	files, err := config.DiscoverConfigFiles(configDir)
	if err != nil {
		return fmt.Errorf("config reload rejected: unlocked changes detected")
	}
	result, err := config.VerifyIntegrity(configDir, files)
	if err != nil {
		return fmt.Errorf("config reload rejected: unlocked changes detected")
	}
	if !result.Passed {
		return fmt.Errorf("config reload rejected: %s", strings.Join(result.Errors, "; "))
	}
	if failOnDrift && len(result.Warnings) > 0 {
		return fmt.Errorf("config reload rejected (admission.fail_on_drift): operational drift: %s", strings.Join(result.Warnings, "; "))
	}
	if err := verifyPluginFingerprintsForConfig(configPath, owner); err != nil {
		return fmt.Errorf("config reload rejected: %v", err)
	}
	return nil
}

func loadPluginFingerprintRecords(configPath string, cfg *config.Config, registry *plugin.Registry) []configsnapshot.PluginFingerprintRecord {
	manifest, err := config.LoadChecksums(resolveConfigDir(configPath))
	if err != nil || cfg == nil || len(cfg.Plugins) == 0 {
		return nil
	}
	locked := make(map[string]config.PluginFingerprint, len(manifest.PluginFingerprints))
	for _, fp := range manifest.PluginFingerprints {
		locked[fp.Name] = fp
	}

	names := make([]string, 0, len(cfg.Plugins))
	for name := range cfg.Plugins {
		names = append(names, name)
	}
	sort.Strings(names)

	records := make([]configsnapshot.PluginFingerprintRecord, 0, len(names))
	for _, name := range names {
		pluginConf := cfg.Plugins[name]
		fp, lockedOK := locked[name]
		discovered := false
		if registry != nil {
			_, discovered = registry.Get(name)
		}
		if lockedOK && discovered {
			record := configsnapshot.PluginFingerprintRecordFromLock(fp)
			record.Enabled = pluginConf.Enabled
			record.Uses = pluginConf.Uses
			records = append(records, record)
			continue
		}

		record := configsnapshot.PluginFingerprintRecord{
			Plugin:    name,
			Enabled:   pluginConf.Enabled,
			Uses:      pluginConf.Uses,
			Available: false,
		}
		switch {
		case !discovered:
			record.UnavailableReason = "configured plugin was not discovered"
		case !lockedOK:
			record.UnavailableReason = "configured plugin missing from .checksums plugin_fingerprints"
		}
		if lockedOK {
			record.ManifestPath = fp.ManifestPath
			record.ManifestResolvedPath = fp.ManifestResolvedPath
			record.ManifestHash = fp.ManifestHash
			record.EntrypointPath = fp.EntrypointPath
			record.EntrypointResolvedPath = fp.EntrypointResolvedPath
			record.EntrypointHash = fp.EntrypointHash
		}
		records = append(records, record)
	}
	return records
}

func snapshotVersion() string {
	v := strings.TrimSpace(version)
	commit := strings.TrimSpace(gitCommit)
	if commit != "" && commit != "unknown" {
		return v + "+commit." + commit
	}
	return v
}

func binaryPath() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return path
}

func buildRuntime(cfg *config.Config, configPath string, configSource string, reloadFunc func(context.Context) (api.ReloadResponse, error), errCh chan error, opts runtimeBuildOptions) (*runtimeState, error) {
	log.Setup(cfg.Service.LogLevel)
	logger := log.WithComponent("main")

	configPaths, err := config.CollectConfigPaths(configPath, cfg)
	if err != nil {
		logger.Error("config symlink scan failed", "error", err)
		return nil, err
	}
	symlinkWarnings, err := config.DetectSymlinks(configPaths)
	if err != nil {
		logger.Error("config symlink scan failed", "error", err)
		return nil, err
	}
	for _, warning := range symlinkWarnings {
		logger.Warn("symlink detected", "path", warning.Path, "resolved", warning.Resolved)
	}
	if len(symlinkWarnings) > 0 && !cfg.Service.AllowSymlinks {
		return nil, fmt.Errorf("symlinks detected in config paths but not allowed")
	}

	pluginRoots, err := resolvePluginRoots(cfg, configPath)
	if err != nil {
		logger.Error("plugin root resolution failed", "error", err)
		return nil, err
	}
	registry, err := plugin.DiscoverManyWithOptions(pluginRoots, func(level, msg string, args ...any) {
		switch level {
		case "debug":
			logger.Debug(msg, args...)
		case "info":
			logger.Info(msg, args...)
		case "warn":
			logger.Warn(msg, args...)
		case "error":
			logger.Error(msg, args...)
		}
	}, plugin.DiscoverOptions{AllowSymlinks: cfg.Service.AllowSymlinks})
	if err != nil {
		logger.Error("plugin discovery failed", "plugin_roots", pluginRoots, "error", err)
		return nil, err
	}
	aliases, err := plugin.ApplyAliases(registry, cfg.Plugins)
	if err != nil {
		logger.Error("plugin aliasing failed", "error", err)
		return nil, err
	}
	for _, alias := range aliases {
		logger.Info("plugin alias registered", "plugin", alias.Name, "uses", alias.Uses)
	}

	// Preflight: report which config files were loaded
	{
		logger.Info("config loaded", "path", configPath, "source", configSource)

		configDir := resolveConfigDir(configPath)

		var sourceFiles []string
		for f := range cfg.SourceFiles {
			sourceFiles = append(sourceFiles, f)
		}
		sort.Strings(sourceFiles)
		for _, f := range sourceFiles {
			rel, err := filepath.Rel(configDir, f)
			if err != nil || strings.HasPrefix(rel, "..") {
				rel = f
			}
			logger.Info("config file", "file", rel)
		}

		pluginsConfigured := len(cfg.Plugins)
		pluginsEnabled := 0
		for _, p := range cfg.Plugins {
			if p.Enabled {
				pluginsEnabled++
			}
		}
		logger.Info("config summary",
			"plugins_discovered", len(registry.All()),
			"plugins_configured", pluginsConfigured,
			"plugins_enabled", pluginsEnabled,
			"api_listen", cfg.API.Listen,
		)
	}

	// Admission-control enforcement. Each policy is independent (decomplected from
	// the former bundled strict_mode). At reload, fail_on_drift is sourced from the
	// running config; here at boot every policy reads from cfg directly.
	if w := cfg.Service.StrictModeDeprecationWarning(); w != "" {
		logger.Warn(w)
	}
	admission := cfg.Service.AdmissionPolicy()

	if admission.VerifyIntegrityOnBoot {
		logger.Info("admission: verifying config integrity at boot", "fail_on_drift", admission.FailOnDrift)
		if err := verifyReloadIntegrity(configPath, admission.FailOnDrift, opts.vaultOwner); err != nil {
			logger.Error("integrity check failed (admission.verify_integrity_on_boot)", "error", err)
			return nil, fmt.Errorf("integrity check failed: %w", err)
		}
	}

	if admission.ValidateConfigOnBoot {
		// Vault-aware: a from-scratch vault gateway with no api token yet is a
		// legitimate bootstrap posture (#129), not a config error — the boot owner
		// is opts.vaultOwner (reused from LoadWithVault).
		doc := doctor.New(cfg, registry).WithVaultPresent(opts.vaultOwner != nil)
		report := doc.Validate()
		if !report.Valid {
			logger.Error("configuration validation failed (admission.validate_config_on_boot)")
			for _, e := range report.Errors {
				logger.Error("config error", "detail", e)
			}
			return nil, fmt.Errorf("configuration validation failed")
		}
	}

	// Unknown-key handling (#26): the load-time YAML decode is lenient, so a
	// typo'd or unsupported key is silently dropped — the operator believes it is
	// active when it is not. Always surface each dropped key as a warning. When
	// admission.validate_config_on_boot is set, dropped keys are additionally an
	// admission FAILURE (the config/schema drift that once blocked this — #36 — is
	// resolved, so ductile's own configs decode clean); otherwise it stays
	// warn-only. buildRuntime runs at boot AND reload, so this gates both, matching
	// the doctor.Validate() check above.
	for _, w := range config.StrictDecodeWarnings(cfg) {
		logger.Warn("config: key ignored (not a known field)", "detail", w)
	}
	if admission.ValidateConfigOnBoot {
		if err := config.StrictDecodeError(cfg); err != nil {
			logger.Error("configuration validation failed: ignored config keys (admission.validate_config_on_boot)", "error", err)
			return nil, fmt.Errorf("configuration validation failed: %w; fix or remove the keys, or disable service.admission.validate_config_on_boot", err)
		}
	}

	// The RequireAPIAuth zero-token guard is enforced below, after the vault
	// owner is resolved — a from-scratch vault gateway with no api token yet is a
	// legitimate bootstrap (management) posture, not a misconfiguration (#129).

	logger.Info("ductile starting", "version", version, "config", configPath)

	ctx := context.Background()
	db, err := storage.OpenSQLite(ctx, cfg.State.Path)
	if err != nil {
		logger.Error("failed to open database", "path", cfg.State.Path, "error", err)
		return nil, err
	}
	logger.Info("database opened", "path", cfg.State.Path)

	logger.Info("plugin discovery complete", "count", len(registry.All()))
	if err := validateScheduledCommands(cfg, registry); err != nil {
		logger.Error("invalid scheduled command configuration", "error", err)
		return nil, err
	}

	configDir := configPath
	if stat, err := os.Stat(configDir); err != nil || !stat.IsDir() {
		configDir = filepath.Dir(configPath)
	}

	pipelineFiles := make([]string, 0, len(cfg.SourceFiles))
	for f := range cfg.SourceFiles {
		pipelineFiles = append(pipelineFiles, f)
	}
	sort.Strings(pipelineFiles)

	routerEngine, err := router.LoadFromConfigFiles(pipelineFiles, registry, logger)
	if err != nil {
		logger.Error("failed to load router pipelines", "config_dir", configDir, "error", err)
		return nil, err
	}
	if r, ok := routerEngine.(*router.Router); ok {
		pipelines := r.PipelineSummary()
		logger.Info("pipeline discovery complete", "config_dir", configDir, "pipelines_loaded", len(pipelines))
		for _, p := range pipelines {
			logger.Info("pipeline registered", "name", p.Name, "trigger", p.Trigger)
		}
	}

	activeSnapshotID := strings.TrimSpace(opts.existingSnapshotID)
	if activeSnapshotID == "" {
		reason := opts.snapshotReason
		if reason == "" {
			reason = configsnapshot.ReasonStartup
		}
		pluginFingerprints := loadPluginFingerprintRecords(configPath, cfg, registry)
		snapshot, err := configsnapshot.Build(configsnapshot.BuildInput{
			Config:             cfg,
			ConfigPath:         configPath,
			ConfigSource:       configSource,
			Reason:             reason,
			DuctileVersion:     snapshotVersion(),
			BinaryPath:         binaryPath(),
			PluginFingerprints: pluginFingerprints,
		})
		if err != nil {
			logger.Error("failed to build config snapshot", "error", err)
			return nil, err
		}
		if err := configsnapshot.Insert(ctx, db, snapshot); err != nil {
			logger.Error("failed to store config snapshot", "error", err)
			return nil, err
		}
		activeSnapshotID = snapshot.ID
		logger.Info("config snapshot recorded", "snapshot_id", activeSnapshotID, "reason", reason, "config_hash", snapshot.ConfigHash)
	}

	q := queue.New(
		db,
		queue.WithLogger(logger),
		queue.WithDedupeTTL(cfg.Service.DedupeTTL),
		queue.WithConfigSnapshotIDProvider(func() string {
			return activeSnapshotID
		}),
	)
	st := state.NewStore(db)
	contextStore := state.NewContextStore(db)
	hub := events.NewHub(256)

	rt := &runtimeState{
		cfg:                    cfg,
		configPath:             configPath,
		logger:                 logger,
		registry:               registry,
		router:                 routerEngine,
		stopDone:               make(chan struct{}),
		errCh:                  errCh,
		db:                     db,
		configSource:           configSource,
		activeConfigSnapshotID: activeSnapshotID,
	}

	rt.ctx, rt.cancel = newRuntimeContext()

	sched := scheduler.New(cfg, q, hub, logger,
		scheduler.WithCommandSupportChecker(func(pluginName, commandName string) bool {
			plug, ok := registry.Get(pluginName)
			if !ok {
				return false
			}
			return plug.SupportsCommand(commandName)
		}),
	)
	rt.scheduler = sched
	admitter := state.NewAdmitter(q, state.DefaultMaxContextBytes)

	// Load the vault owner — the single guarded holder of the decrypted model.
	// The daemon routes the spawn-time read path (Compose) and management writes
	// (SetSecret) through this one owner. A nil owner (no vault yet, or keyless)
	// leaves the composer unset so no secrets are delivered — back-compatible. We
	// assign the interface only for a non-nil owner to avoid a typed-nil
	// interface that would later panic in Compose.
	var secretComposer dispatch.SecretComposer
	// pluginVerifier stays a nil interface unless a vault is loaded, so the §3.3
	// compose-time gate is off for vault-less deployments (and we avoid a typed-nil
	// interface that would defeat the nil check in composePluginSecrets).
	var pluginVerifier dispatch.PluginVerifier
	// Reuse the owner the load-time graft already decrypted (passed via opts on the
	// daemon start path) rather than decrypting the blob a second time (#43
	// redundant decrypt; epic #48 slice 2). A nil opts owner — reload, restore, or
	// no vault — falls back to a fresh load, preserving prior behaviour exactly.
	vaultOwner := opts.vaultOwner
	if vaultOwner == nil {
		vaultOwner, err = config.LoadVault(configDir, cfg)
		if err != nil {
			logger.Error("failed to load vault", "error", err)
			return nil, fmt.Errorf("vault: %w", err)
		}
	}
	if vaultOwner != nil {
		secretComposer = vaultOwner
		// §3.3: re-verify a principal's live bytes against its recorded keyed
		// fingerprint right before delivering its secrets. The nonce comes from the
		// same loaded vault that holds the secrets.
		pluginVerifier = newPluginIdentityVerifier(registry, configDir, vaultOwner, logger)
		logger.Info("vault secret delivery enabled (compose-time attestation on)")
	}

	// Decide the gateway boot posture now that the vault owner is known. The
	// management posture (vault-operable / ductile-closed) is reached only when a
	// vault owner exists to operate and no api token is configured yet — see
	// docs/adr/vault-credential-ladder.md §4. The fail-closed RequireAPIAuth guard
	// (relocated from the top of buildRuntime) still refuses a from-scratch gateway
	// that has NO vault to bootstrap from.
	bootPosture := config.DecideBootPosture(cfg, vaultOwner != nil)
	if admission.RequireAPIAuth && cfg.API.Enabled && len(cfg.API.Auth.Tokens) == 0 && bootPosture != config.PostureManagementOnly {
		logger.Error("no API tokens configured (admission.require_api_auth requires at least one token when API is enabled, and no vault is present to bootstrap one)")
		return nil, fmt.Errorf("no API tokens configured")
	}

	// Privsep boot gate (#86): the capability to drop privilege and a configured
	// accounts table must agree, or the gateway refuses to start — no silent run at
	// gateway privilege. Evaluated once here (boot and reload); the result drives
	// whether the dispatcher drops each plugin to its account.
	privsepMode, gateErr := dispatch.BootGate(cfg)
	if gateErr != nil {
		logger.Error("privsep boot gate refused startup", "error", gateErr)
		return nil, gateErr
	}
	switch {
	case privsepMode == dispatch.BootEnforce:
		logger.Info("privsep enforcing: plugins drop to their resolved account", "accounts", len(cfg.Accounts))
		// Surface the conventional-tier dependencies at boot (luminary review T1/T2):
		// the `default`/`untrusted` tier roles are matched by NAME in code, so their
		// absence silently changes posture. Warn loudly rather than fail — both
		// directions are fail-safe (no default → ungranted run unconfined; no
		// untrusted → a mismatch fails closed), but the operator must not learn it
		// per-job at first spawn.
		if _, ok := cfg.Accounts["default"]; !ok {
			logger.Warn("privsep: no `default` account tier configured — ungranted plugins will run UNCONFINED (at the gateway uid), not behind a wall")
		}
		if _, ok := cfg.Accounts["untrusted"]; !ok {
			logger.Warn("privsep: no `untrusted` account tier configured — a fingerprint-mismatched plugin has no downgrade target, so its spawn fails closed")
		}
		// Name every credentialed (trusted) account loudly at boot — a plugin granted
		// one runs as that REAL user with their on-disk creds and NO wall, so its
		// compromise == that user's. This must be auditable, never inferred from a
		// `home:` field's presence (grill: Ousterhout/Armstrong informed-consent).
		for name, w := range cfg.Accounts {
			if w.Home != "" {
				logger.Warn("privsep: account is CREDENTIALED (trusted) — runs as a real user with their creds, NOT walled; its compromise equals that user's",
					"account", name, "uid", w.UID, "home", w.Home)
			}
		}
		// Reconcile the filesystem floor (#87) before any plugin can spawn: lock the
		// secrets surface (gateway-owned, 0600/0700) and give each account its private
		// 0700 dir. All-or-refuse — a failure here aborts the boot (never run
		// half-confined). The age key is already enforced fail-closed at load. The
		// surface is single-sourced in dispatch.SecretSurfacePaths and reconciles the
		// config DIRECTORY, so sibling secret files (tokens, vault blob, .checksums)
		// are covered even when the config path is a single file (review T4).
		secretPaths := dispatch.SecretSurfacePaths(cfg, configDir)
		if err := dispatch.ReconcileAccountFilesystem(cfg, secretPaths); err != nil {
			logger.Error("privsep filesystem reconciliation failed", "error", err)
			return nil, err
		}
		// Tier-aware root side-door audit (#111): probe each drop account for a host
		// root-escalation path (nopasswd sudo, docker/lxd/incus group, writable
		// secure_path, account-writable setuid-root). A CONFINED account with a
		// side-door has no real wall — warn loudly, and under strict mode
		// (admission.fail_on_sidedoor) refuse the boot. A CREDENTIALED account is
		// root-equivalent by design — informed-consent warn, always proceed.
		// Detection is best-effort: a false positive never bricks a non-strict boot.
		if err := dispatch.AuditAccountSideDoors(cfg, cfg.Service.AdmissionPolicy().FailOnSideDoor, logger); err != nil {
			logger.Error("privsep side-door audit refused startup", "error", err)
			return nil, err
		}
	case len(cfg.Accounts) > 0 || cfg.Service.Unconfined:
		// Unconfined despite a configured/privileged host is the explicit override —
		// say so loudly so it can never pass unnoticed.
		logger.Warn("privsep UNCONFINED: plugins run at the gateway uid despite configuration",
			"accounts", len(cfg.Accounts), "unconfined_override", cfg.Service.Unconfined)
	default:
		// Plain dev/hygiene-only host (no accounts, no capability, no override).
		// Log the posture explicitly so it is never a silent surprise (#101) —
		// "valid config" must never be mistaken for "enforcing".
		logger.Info("privsep posture: UNCONFINED — no accounts configured; plugins run at the gateway uid (hygiene-only)")
	}

	disp := dispatch.New(q, st, contextStore, routerEngine, registry, hub, cfg,
		dispatch.WithAdmitter(admitter), dispatch.WithSecretComposer(secretComposer),
		dispatch.WithPluginVerifier(pluginVerifier),
		dispatch.WithPrivsepEnforce(privsepMode == dispatch.BootEnforce))
	rt.dispatcher = disp

	relayReceiver, err := relay.NewReceiver(cfg, q, routerEngine, contextStore, admitter, log.WithComponent("relay"))
	if err != nil {
		logger.Error("failed to configure relay receiver", "error", err)
		return nil, err
	}

	// Wire recovery hooks: when the scheduler marks a dead orphan during crash
	// recovery, delegate to the dispatcher's hook-firing machinery so on-hook
	// pipelines (e.g. job-failure-notify → discord_notify) are triggered.
	sched.SetRecoveryHook(disp.FireRecoveryHook)

	if err := sched.Start(rt.ctx); err != nil && err != context.Canceled {
		return nil, fmt.Errorf("scheduler: %w", err)
	}

	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		if err := disp.Start(rt.ctx); err != nil && err != context.Canceled {
			rt.errCh <- fmt.Errorf("dispatcher: %w", err)
		}
	}()

	if cfg.API.Enabled || relayReceiver != nil {
		// Resolve secret_ref-backed bearer tokens against the vault projection
		// before the listener opens. ResolveAPITokens is fail-closed: an
		// unresolvable or empty secret_ref returns an error here, buildRuntime
		// aborts, and on reload the previous runtime is restored (the API never
		// opens — or stays open on its old config — authenticating against an
		// empty credential). buildRuntime is the named supervisor (card #94).
		//
		// The management posture has no api token to resolve by definition (zero
		// configured tokens), so resolution is skipped — but the fail-closed gate
		// above is NOT weakened: it fires whenever tokens ARE configured but do
		// not resolve, which is a gateway-posture condition, never management.
		var tokens []auth.TokenConfig
		if bootPosture != config.PostureManagementOnly {
			resolvedTokens, err := config.ResolveAPITokens(cfg)
			if err != nil {
				logger.Error("API token resolution failed", "error", err)
				return nil, fmt.Errorf("api tokens: %w", err)
			}
			tokens = make([]auth.TokenConfig, 0, len(resolvedTokens))
			for _, t := range resolvedTokens {
				tokens = append(tokens, auth.TokenConfig{
					Token:  t.Token,
					Scopes: t.Scopes,
				})
			}
		}
		binaryPath := ""
		if execPath, err := os.Executable(); err == nil {
			binaryPath = execPath
		}

		apiConfig := api.Config{
			Listen:            cfg.API.Listen,
			Tokens:            tokens,
			MaxConcurrentSync: cfg.API.MaxConcurrentSync,
			MaxSyncTimeout:    cfg.API.MaxSyncTimeout,
			ConfigPath:        configPath,
			BinaryPath:        binaryPath,
			Version:           version,
			RuntimeConfig:     cfg,
			ReloadFunc:        reloadFunc,
			DoctorFunc: func(_ context.Context) (*doctor.Result, error) {
				result, _, err := validateConfigAtPath(configPath)
				return result, err
			},
			SelfcheckFunc: func(_ context.Context) (api.SystemCheckReport, error) {
				return selfcheckReportForAPI(configPath), nil
			},
			RelayReceiver:    relayReceiver,
			AllowedOrigins:   cfg.API.AllowedOrigins,
			ManagementSocket: managementSocketPath(cfg),
		}
		// Expose the vault management API only when a vault owner exists. Assign
		// the interface field solely for a non-nil owner: a typed-nil *vault.Vault
		// would make the interface non-nil and register routes that then panic.
		if vaultOwner != nil {
			apiConfig.Vault = vaultOwner
			// The state store is the append-only audit sink for vault lifecycle
			// facts. Wired only alongside a vault owner; nil leaves audit disabled.
			apiConfig.VaultAuditor = st
		}
		apiServer := api.New(apiConfig, q, registry, routerEngine, disp, contextStore, admitter, st, hub, log.WithComponent("api"))
		rt.apiServer = apiServer
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			// The management posture serves /vault/* on the local socket only and
			// never opens the public gateway listener (ADR §5 invariant). The
			// gateway posture serves the public listener after fail-closed token
			// resolution succeeds above.
			var serveErr error
			if bootPosture == config.PostureManagementOnly {
				serveErr = apiServer.StartManagement(rt.ctx)
			} else {
				serveErr = apiServer.Start(rt.ctx)
			}
			if serveErr != nil && serveErr != context.Canceled {
				rt.errCh <- fmt.Errorf("api: %w", serveErr)
			}
		}()
		if bootPosture == config.PostureManagementOnly {
			// Anti-strand (#130): this is an intentional, named posture, not a
			// wedged half-boot. Log it loudly so an operator/AI can tell "waiting
			// for an api token" apart from "stuck."
			logger.Warn("booting in vault-operable / ductile-closed posture: no api token resolved yet — serving /vault/* on the local management socket only, public gateway listener NOT open. Mint an api token via the admin token over the socket, then `system reload` to activate the gateway.",
				"posture", bootPosture.String(), "management_socket", managementSocketPath(cfg))
		} else {
			logger.Info("HTTP ingress server enabled", "listen", cfg.API.Listen, "api_enabled", cfg.API.Enabled, "relay_enabled", relayReceiver != nil, "posture", bootPosture.String())
		}
	}

	if cfg.Webhooks != nil && len(cfg.Webhooks.Endpoints) > 0 {
		webhookConfig, err := webhook.FromGlobalConfig(cfg.Webhooks, cfg.ResolvedSecrets, cfg.Plugins)
		if err != nil {
			logger.Error("failed to configure webhooks", "error", err)
			return nil, err
		}

		webhookServer := webhook.New(webhookConfig, q, log.WithComponent("webhook"))
		rt.webhook = webhookServer
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			if err := webhookServer.Start(rt.ctx); err != nil && err != context.Canceled {
				rt.errCh <- fmt.Errorf("webhook: %w", err)
			}
		}()
		logger.Info("webhook server enabled", "listen", webhookConfig.Listen, "endpoints", len(webhookConfig.Endpoints))
	}

	return rt, nil
}

// managementSocketPath resolves the unix-domain socket the vault-operable
// bootstrap posture serves /vault/* on. An explicit api.management_socket wins;
// otherwise it defaults to a path beside the state DB (already a protected
// directory). Keep it short — unix sun_path is capped near 104 bytes.
func managementSocketPath(cfg *config.Config) string {
	if cfg.API.ManagementSocket != "" {
		return cfg.API.ManagementSocket
	}
	return filepath.Join(filepath.Dir(cfg.State.Path), "vault-admin.sock")
}

func runStart(args []string) int {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse flags: %v\n", err)
		return 1
	}

	configSource := "explicit"
	if *configPath == "" {
		if os.Getenv("DUCTILE_CONFIG_DIR") != "" {
			configSource = "env:DUCTILE_CONFIG_DIR"
		} else {
			configSource = "auto-discovered"
		}
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "no config found: %v\nHint: create ~/.config/ductile/config.yaml or use --config\n", err)
			return 1
		}
		*configPath = discovered
	}

	// LoadWithVault also returns the owner decrypted by the load-time projection, so
	// the daemon reuses that single decryption as its live owner (epic #48 slice 2).
	cfg, vaultOwner, err := config.LoadWithVault(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		return 1
	}

	pidLockPath := getPIDLockPath(cfg)
	pidLock, err := lock.AcquirePIDLock(pidLockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to acquire PID lock (another instance may be running): %v\n", err)
		return 1
	}
	defer func() { _ = pidLock.Release() }()

	manager := &reloadManager{
		configPath:   *configPath,
		configSource: configSource,
		errCh:        make(chan error, 4),
	}
	manager.reloadFunc = manager.Reload

	runtime, err := buildRuntime(cfg, *configPath, configSource, manager.reloadFunc, manager.errCh, runtimeBuildOptions{
		snapshotReason: configsnapshot.ReasonStartup,
		vaultOwner:     vaultOwner,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start runtime: %v\n", err)
		return 1
	}
	manager.runtime = runtime

	logger := runtime.logger
	logger.Info("acquired PID lock", "path", pidLockPath)
	runTCCPrewarm(cfg.TCCPaths, logger)
	logger.Info("ductile running (press Ctrl+C to stop)")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				if _, err := manager.Reload(context.Background()); err != nil {
					logger.Error("config reload failed", "error", err)
				} else {
					logger.Info("config reloaded successfully")
				}
				continue
			}
			logger.Info("received shutdown signal", "signal", sig)
			manager.Stop()
			logger.Info("ductile stopped")
			return 0
		case err := <-manager.errCh:
			logger.Error("component failed", "error", err)
			manager.Stop()
			logger.Info("ductile stopped")
			return 1
		}
	}
}
