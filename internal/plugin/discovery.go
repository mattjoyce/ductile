package plugin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/mattjoyce/ductile/internal/stopwatch"

	"gopkg.in/yaml.v3"
)

const (
	supportedProtocol = 2
	manifestFilename  = "manifest.yaml"
)

// Registry holds discovered plugins indexed by name.
type Registry struct {
	plugins map[string]*Plugin
}

// NewRegistry creates an empty plugin registry.
func NewRegistry() *Registry {
	return &Registry{
		plugins: make(map[string]*Plugin),
	}
}

// Get retrieves a plugin by name.
func (r *Registry) Get(name string) (*Plugin, bool) {
	p, ok := r.plugins[name]
	return p, ok
}

// All returns all registered plugins.
func (r *Registry) All() map[string]*Plugin {
	return r.plugins
}

// Add registers a plugin in the registry.
func (r *Registry) Add(plugin *Plugin) error {
	if _, exists := r.plugins[plugin.Name]; exists {
		return fmt.Errorf("plugin %q already registered", plugin.Name)
	}
	r.plugins[plugin.Name] = plugin
	return nil
}

type DiscoverOptions struct {
	AllowSymlinks bool
}

// DiscoverManyWithOptions scans multiple plugin roots for manifest.yaml files
// and validates plugins. Roots are processed in input order; duplicate plugin
// names keep the first discovered plugin.
func DiscoverManyWithOptions(pluginRoots []string, logger func(level, msg string, args ...any), opts DiscoverOptions) (*Registry, error) {
	if logger == nil {
		logger = func(level, msg string, args ...any) {}
	}
	if len(pluginRoots) == 0 {
		return nil, fmt.Errorf("at least one plugin root is required")
	}

	absRoots := make([]string, 0, len(pluginRoots))
	seenRoots := make(map[string]struct{}, len(pluginRoots))
	for _, root := range pluginRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve plugin root %q: %w", root, err)
		}
		info, err := os.Stat(absRoot)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("plugin root does not exist: %s", absRoot)
			}
			return nil, fmt.Errorf("failed to stat plugin root %s: %w", absRoot, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("plugin root is not a directory: %s", absRoot)
		}
		resolvedRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve plugin root symlink %s: %w", absRoot, err)
		}
		if err := warnOrErrorOnSymlink(logger, "plugin_root", absRoot, resolvedRoot, opts.AllowSymlinks); err != nil {
			return nil, err
		}
		if _, ok := seenRoots[absRoot]; ok {
			continue
		}
		seenRoots[absRoot] = struct{}{}
		absRoots = append(absRoots, absRoot)
	}
	if len(absRoots) == 0 {
		return nil, fmt.Errorf("at least one plugin root is required")
	}

	registry := NewRegistry()
	for _, root := range absRoots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || d.Name() != manifestFilename {
				return nil
			}

			pluginPath := filepath.Dir(path)
			pluginDirName := filepath.Base(pluginPath)

			plugin, err := loadPlugin(pluginDirName, pluginPath, root, opts.AllowSymlinks, logger)
			if err != nil {
				logger("warn", "failed to load plugin", "root", root, "path", pluginPath, "error", err.Error())
				return nil
			}

			if err := registry.Add(plugin); err != nil {
				if existing, ok := registry.Get(plugin.Name); ok {
					logger(
						"warn",
						"duplicate plugin ignored (keeping first discovered)",
						"plugin", plugin.Name,
						"ignored_path", plugin.Path,
						"kept_path", existing.Path,
					)
				} else {
					logger("warn", "duplicate plugin", "plugin", plugin.Name, "error", err.Error())
				}
				return nil
			}

			logger("info", "loaded plugin", "plugin", plugin.Name, "path", plugin.Path, "version", plugin.Version, "protocol", plugin.Protocol)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan plugin root %s: %w", root, err)
		}

		// Warn on directories that look like plugins (contain a common entrypoint
		// file) but have no manifest.yaml — these are silently skipped by the walk
		// above and can cause confusing behaviour where edits go to the wrong copy.
		warnOrphanPluginDirs(root, logger)
	}

	return registry, nil
}

// loadPlugin reads and validates a single plugin.
func loadPlugin(name, pluginPath, pluginsDir string, allowSymlinks bool, logger func(level, msg string, args ...any)) (*Plugin, error) {
	manifestPath := filepath.Join(pluginPath, manifestFilename)

	// Read manifest file
	// #nosec G304 -- plugin manifests are operator-controlled local inputs.
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// Parse manifest
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	// Validate manifest fields
	if err := validateManifest(&manifest); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	// Check protocol version
	if manifest.Protocol != supportedProtocol {
		return nil, fmt.Errorf("unsupported protocol version %d (supported: %d)", manifest.Protocol, supportedProtocol)
	}

	// Construct entrypoint path
	entrypointPath := filepath.Join(pluginPath, manifest.Entrypoint)

	// Trust checks (SPEC §5.5)
	if err := validateTrust(entrypointPath, pluginPath, pluginsDir, allowSymlinks, logger); err != nil {
		return nil, fmt.Errorf("trust validation failed: %w", err)
	}

	concurrencySafe := true
	if manifest.ConcurrencySafe != nil {
		concurrencySafe = *manifest.ConcurrencySafe
	}

	return &Plugin{
		ManifestSpec:    manifest.ManifestSpec,
		ManifestVersion: manifest.ManifestVersion,
		Name:            manifest.Name,
		Path:            pluginPath,
		Entrypoint:      entrypointPath,
		Protocol:        manifest.Protocol,
		Version:         manifest.Version,
		Description:     manifest.Description,
		ConcurrencySafe: concurrencySafe,
		Commands:        manifest.Commands,
		FactOutputs:     manifest.FactOutputs,
		ConfigKeys:      manifest.ConfigKeys,
		Stopwatch:       manifest.Stopwatch,
	}, nil
}

// validateManifest checks required manifest fields.
func validateManifest(m *Manifest) error {
	if strings.TrimSpace(m.ManifestSpec) == "" {
		return fmt.Errorf("manifest_spec is required")
	}
	if m.ManifestSpec != SupportedManifestSpec {
		return fmt.Errorf("unsupported manifest_spec %q (supported: %q)", m.ManifestSpec, SupportedManifestSpec)
	}
	if m.ManifestVersion == 0 {
		return fmt.Errorf("manifest_version is required")
	}
	if m.ManifestVersion != SupportedManifestVersion {
		return fmt.Errorf("unsupported manifest_version %d (supported: %d)", m.ManifestVersion, SupportedManifestVersion)
	}

	if m.Name == "" {
		return fmt.Errorf("name is required")
	}

	if m.Protocol == 0 {
		return fmt.Errorf("protocol version is required")
	}

	if m.Entrypoint == "" {
		return fmt.Errorf("entrypoint is required")
	}

	// Check for path traversal in entrypoint
	if strings.Contains(m.Entrypoint, "..") {
		return fmt.Errorf("entrypoint contains path traversal: %s", m.Entrypoint)
	}

	if len(m.Commands) == 0 {
		return fmt.Errorf("at least one command must be declared")
	}

	for _, cmd := range m.Commands {
		if cmd.Name == "" {
			return fmt.Errorf("command name is required")
		}
		if !validCommandName(cmd.Name) {
			return fmt.Errorf("invalid command %q (must start with a letter and contain only letters, digits, '_' or '-')", cmd.Name)
		}
		if !cmd.Type.valid() {
			return fmt.Errorf("invalid command type %q for %q (valid: read, write)", cmd.Type, cmd.Name)
		}
		if err := validateCommandValues(cmd); err != nil {
			return fmt.Errorf("command %q values: %w", cmd.Name, err)
		}
	}
	for i, rule := range m.FactOutputs {
		if err := validateFactOutputRule(m.Commands, rule); err != nil {
			return fmt.Errorf("fact_outputs[%d]: %w", i, err)
		}
	}

	if m.Stopwatch != nil && m.Stopwatch.MaxSubs != 0 {
		if m.Stopwatch.MaxSubs < 1 || m.Stopwatch.MaxSubs > stopwatch.MaxSubsHardUpper {
			return fmt.Errorf(
				"stopwatch.max_subs %d out of range [1, %d]",
				m.Stopwatch.MaxSubs, stopwatch.MaxSubsHardUpper,
			)
		}
	}

	return nil
}

func validateFactOutputRule(commands Commands, rule FactOutputRule) error {
	command := strings.TrimSpace(rule.When.Command)
	if command == "" {
		return fmt.Errorf("when.command is required")
	}
	if !commands.hasName(command) {
		return fmt.Errorf("when.command %q is not declared in commands", command)
	}

	from := strings.TrimSpace(rule.From)
	if from == "" {
		return fmt.Errorf("from is required")
	}
	if from != "state_updates" {
		return fmt.Errorf("unsupported from %q", from)
	}

	if strings.TrimSpace(rule.FactType) == "" {
		return fmt.Errorf("fact_type is required")
	}

	view := strings.TrimSpace(rule.CompatibilityView)
	switch view {
	case "", "mirror_object":
		return nil
	default:
		return fmt.Errorf("unsupported compatibility_view %q", view)
	}
}

func (c Commands) hasName(name string) bool {
	for _, cmd := range c {
		if cmd.Name == name {
			return true
		}
	}
	return false
}

func validateCommandValues(cmd Command) error {
	if cmd.Values == nil {
		return nil
	}
	for _, name := range cmd.Values.Consume {
		if !validValueName(name, "payload") {
			return fmt.Errorf("invalid consume value %q (expected payload.<name> or payload.*)", name)
		}
	}
	for _, emitted := range cmd.Values.Emit {
		if strings.TrimSpace(emitted.Event) == "" {
			return fmt.Errorf("emit event is required")
		}
		for _, name := range emitted.Values {
			if !validValueName(name, "payload") {
				return fmt.Errorf("invalid emit value %q for event %q (expected payload.<name> or payload.*)", name, emitted.Event)
			}
		}
	}
	return nil
}

func validValueName(name, root string) bool {
	name = strings.TrimSpace(name)
	prefix := root + "."
	if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
		return false
	}
	if name == prefix+"*" {
		return true
	}
	if strings.ContainsAny(name, " \t\r\n{}[]") {
		return false
	}
	for _, segment := range strings.Split(name[len(prefix):], ".") {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			switch {
			case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

func validCommandName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case i == 0 && unicode.IsLetter(r):
		case i > 0 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

// validateTrust enforces security constraints (SPEC §5.5).
func validateTrust(entrypointPath, pluginPath, pluginsDir string, allowSymlinks bool, logger func(level, msg string, args ...any)) error {
	return validateTrustInRoots(entrypointPath, pluginPath, []string{pluginsDir}, allowSymlinks, logger)
}

func validateTrustInRoots(entrypointPath, pluginPath string, pluginRoots []string, allowSymlinks bool, logger func(level, msg string, args ...any)) error {
	if len(pluginRoots) == 0 {
		return fmt.Errorf("no plugin roots configured")
	}

	// Resolve symlinks
	resolvedEntrypoint, err := filepath.EvalSymlinks(entrypointPath)
	if err != nil {
		return fmt.Errorf("failed to resolve entrypoint symlink: %w", err)
	}

	resolvedPluginPath, err := filepath.EvalSymlinks(pluginPath)
	if err != nil {
		return fmt.Errorf("failed to resolve plugin path symlink: %w", err)
	}

	if err := warnOrErrorOnSymlink(logger, "plugin_entrypoint", entrypointPath, resolvedEntrypoint, allowSymlinks); err != nil {
		return err
	}
	if err := warnOrErrorOnSymlink(logger, "plugin_dir", pluginPath, resolvedPluginPath, allowSymlinks); err != nil {
		return err
	}

	// Check entrypoint is under one of the configured plugin roots
	inApprovedRoot := false
	for _, root := range pluginRoots {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("failed to resolve plugin root symlink %s: %w", root, err)
		}
		if strings.HasPrefix(resolvedEntrypoint, resolvedRoot+string(os.PathSeparator)) {
			inApprovedRoot = true
			break
		}
	}
	if !inApprovedRoot {
		return fmt.Errorf("entrypoint %s is not under any configured plugin root", resolvedEntrypoint)
	}

	// Check entrypoint is under plugin directory
	if !strings.HasPrefix(resolvedEntrypoint, resolvedPluginPath+string(os.PathSeparator)) {
		return fmt.Errorf("entrypoint %s is not under plugin directory %s", resolvedEntrypoint, resolvedPluginPath)
	}

	// Check entrypoint is executable
	info, err := os.Stat(resolvedEntrypoint)
	if err != nil {
		return fmt.Errorf("entrypoint not found: %w", err)
	}

	mode := info.Mode()
	if mode&0111 == 0 {
		return fmt.Errorf("entrypoint is not executable: %s", resolvedEntrypoint)
	}

	// Check plugin directory is not world-writable
	pluginInfo, err := os.Stat(resolvedPluginPath)
	if err != nil {
		return fmt.Errorf("plugin directory not found: %w", err)
	}

	if pluginInfo.Mode().Perm()&0002 != 0 {
		return fmt.Errorf("plugin directory is world-writable: %s", resolvedPluginPath)
	}

	return nil
}

// warnOrphanPluginDirs warns when a subdirectory of root contains a common
// plugin entrypoint file (run.py, run.sh, main.go, etc.) but no manifest.yaml.
// These dirs are invisible to discovery and edits to them have no effect.
func warnOrphanPluginDirs(root string, logger func(level, msg string, args ...any)) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	commonEntrypoints := map[string]struct{}{
		"run.py": {}, "run.sh": {}, "main.go": {}, "main.py": {},
		"index.js": {}, "index.ts": {}, "run.js": {}, "run.ts": {},
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(dir, manifestFilename)); err == nil {
			continue // has manifest — already handled by main walk
		}
		// Check for any common entrypoint file
		for name := range commonEntrypoints {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				logger("warn", "plugin directory missing manifest.yaml — directory will be ignored",
					"path", dir,
					"hint", "add a manifest.yaml or remove the directory from the plugin root")
				break
			}
		}
	}
}

func warnOrErrorOnSymlink(logger func(level, msg string, args ...any), kind, path, resolved string, allowSymlinks bool) error {
	if filepath.Clean(resolved) == filepath.Clean(path) {
		return nil
	}

	if allowSymlinks {
		logger("warn", "symlink detected", "kind", kind, "path", path, "resolved", resolved)
		return nil
	}
	return fmt.Errorf("symlink detected for %s (%s -> %s); set service.allow_symlinks=true to allow", kind, path, resolved)
}
