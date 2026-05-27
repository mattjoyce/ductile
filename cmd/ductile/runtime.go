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

	newCfg, err := config.Load(rm.configPath)
	if err != nil {
		return api.ReloadResponse{Status: "error", Message: err.Error()}, err
	}
	// P2-10: source strict_mode policy from the RUNNING config (oldCfg), not the
	// proposed newCfg — otherwise an attacker could relax admission by setting
	// strict_mode: false in the very reload they are trying to push.
	if err := verifyReloadIntegrity(rm.configPath, oldCfg.Service.StrictMode); err != nil {
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
// and plugin fingerprint locks. P2-10: when strict is true, operational tier warnings
// (e.g. config.yaml or routes.yaml drift) are promoted to admission failures so the
// reload is rejected. When strict is false, operational warnings stay warnings —
// only high-security file mismatches and plugin fingerprint mismatches reject. The
// strict flag should come from the running config's service.strict_mode so that an
// attacker cannot relax policy via the very reload they are trying to push.
func verifyReloadIntegrity(configPath string, strict bool) error {
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
	if strict && len(result.Warnings) > 0 {
		return fmt.Errorf("config reload rejected (strict_mode): operational drift: %s", strings.Join(result.Warnings, "; "))
	}
	if err := verifyPluginFingerprintsForConfig(configPath); err != nil {
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

	// Strict mode enforcement. P2-10: route through verifyReloadIntegrity with
	// strict=true so startup and reload share one admission gate — operational
	// drift fails both, not just one.
	if cfg.Service.StrictMode {
		logger.Info("strict mode enabled, performing pre-flight checks")

		if err := verifyReloadIntegrity(configPath, true); err != nil {
			logger.Error("integrity check failed (strict mode)", "error", err)
			return nil, fmt.Errorf("integrity check failed: %w", err)
		}

		doc := doctor.New(cfg, registry)
		report := doc.Validate()
		if !report.Valid {
			logger.Error("configuration validation failed (strict mode)")
			for _, e := range report.Errors {
				logger.Error("config error", "detail", e)
			}
			return nil, fmt.Errorf("configuration validation failed")
		}

		if cfg.API.Enabled && len(cfg.API.Auth.Tokens) == 0 {
			logger.Error("no API tokens configured (strict mode requires at least one token when API is enabled)")
			return nil, fmt.Errorf("no API tokens configured")
		}
	}

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
	disp := dispatch.New(q, st, contextStore, routerEngine, registry, hub, cfg, dispatch.WithAdmitter(admitter))
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
		tokens := make([]auth.TokenConfig, 0, len(cfg.API.Auth.Tokens))
		for _, t := range cfg.API.Auth.Tokens {
			tokens = append(tokens, auth.TokenConfig{
				Token:  t.Token,
				Scopes: t.Scopes,
			})
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
			RelayReceiver:  relayReceiver,
			AllowedOrigins: cfg.API.AllowedOrigins,
		}
		apiServer := api.New(apiConfig, api.Deps{
			Queue:        q,
			Registry:     registry,
			Router:       routerEngine,
			Waiter:       disp,
			ContextStore: contextStore,
			Admitter:     admitter,
			Stopwatch:    st,
			Hub:          hub,
			Logger:       log.WithComponent("api"),
		})
		rt.apiServer = apiServer
		rt.wg.Add(1)
		go func() {
			defer rt.wg.Done()
			if err := apiServer.Start(rt.ctx); err != nil && err != context.Canceled {
				rt.errCh <- fmt.Errorf("api: %w", err)
			}
		}()
		logger.Info("HTTP ingress server enabled", "listen", cfg.API.Listen, "api_enabled", cfg.API.Enabled, "relay_enabled", relayReceiver != nil)
	}

	if cfg.Webhooks != nil && len(cfg.Webhooks.Endpoints) > 0 {
		tokensMap := make(map[string]string, len(cfg.Tokens))
		for _, t := range cfg.Tokens {
			tokensMap[t.Name] = t.Key
		}
		webhookConfig, err := webhook.FromGlobalConfig(cfg.Webhooks, tokensMap, cfg.Plugins)
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

	cfg, err := config.Load(*configPath)
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
