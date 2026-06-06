package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDiscover(t *testing.T) {
	tests := []struct {
		name      string
		setupFn   func(t *testing.T) string // Returns plugins directory
		wantCount int
		wantErr   bool
		checkFn   func(t *testing.T, reg *Registry)
	}{
		{
			name: "valid plugin discovered",
			setupFn: func(t *testing.T) string {
				dir := t.TempDir()
				pluginDir := filepath.Join(dir, "test-plugin")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: test-plugin
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
  - name: health
    type: read
`
				if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
					t.Fatalf("write manifest: %v", err)
				}

				// Create executable entrypoint
				entrypoint := filepath.Join(pluginDir, "run.sh")
				if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
					t.Fatalf("write entrypoint: %v", err)
				}

				return dir
			},
			wantCount: 1,
			wantErr:   false,
			checkFn: func(t *testing.T, reg *Registry) {
				plugin, ok := reg.Get("test-plugin")
				if !ok {
					t.Fatal("test-plugin not found")
				}
				if plugin.Protocol != 2 {
					t.Error("protocol version mismatch")
				}
				if !plugin.SupportsCommand("poll") {
					t.Error("should support poll command")
				}
			},
		},
		{
			name: "multiple valid plugins",
			setupFn: func(t *testing.T) string {
				dir := t.TempDir()

				for _, name := range []string{"plugin1", "plugin2"} {
					pluginDir := filepath.Join(dir, name)
					if err := os.Mkdir(pluginDir, 0o755); err != nil {
						t.Fatalf("mkdir plugin dir: %v", err)
					}

					manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: ` + name + `
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
`
					if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
						t.Fatalf("write manifest: %v", err)
					}
					entrypoint := filepath.Join(pluginDir, "run.sh")
					if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\n"), 0o755); err != nil {
						t.Fatalf("write entrypoint: %v", err)
					}
				}

				return dir
			},
			wantCount: 2,
			wantErr:   false,
		},
		{
			name: "directory without manifest skipped",
			setupFn: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "no-manifest"), 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}
				return dir
			},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "unsupported protocol skipped",
			setupFn: func(t *testing.T) string {
				dir := t.TempDir()
				pluginDir := filepath.Join(dir, "bad-protocol")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: bad-protocol
version: 1.0.0
protocol: 99
entrypoint: run.sh
commands:
  - name: poll
    type: write
`
				if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
					t.Fatalf("write manifest: %v", err)
				}
				return dir
			},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "non-executable entrypoint skipped",
			setupFn: func(t *testing.T) string {
				dir := t.TempDir()
				pluginDir := filepath.Join(dir, "non-exec")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: non-exec
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
`
				if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
					t.Fatalf("write manifest: %v", err)
				}
				// Create non-executable file
				if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
					t.Fatalf("write entrypoint: %v", err)
				}
				return dir
			},
			wantCount: 0,
			wantErr:   false,
		},
		{
			name: "nonexistent directory",
			setupFn: func(t *testing.T) string {
				return "/nonexistent/path"
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginsDir := tt.setupFn(t)

			logger := func(level, msg string, args ...any) {
				// Silent logger for tests
			}

			reg, err := DiscoverManyWithOptions([]string{pluginsDir}, logger, DiscoverOptions{AllowSymlinks: true})

			if (err != nil) != tt.wantErr {
				t.Errorf("Discover() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if len(reg.All()) != tt.wantCount {
					t.Errorf("Discover() found %d plugins, want %d", len(reg.All()), tt.wantCount)
				}

				if tt.checkFn != nil {
					tt.checkFn(t, reg)
				}
			}
		})
	}
}

func TestDiscoverMany_MultipleRootsAndDuplicatePrecedence(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()

	makePlugin := func(root, dirName, manifestName string) {
		pDir := filepath.Join(root, dirName)
		if err := os.MkdirAll(pDir, 0o755); err != nil {
			t.Fatalf("mkdir plugin dir: %v", err)
		}
		manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: ` + manifestName + `
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
`
		if err := os.WriteFile(filepath.Join(pDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		if err := os.WriteFile(filepath.Join(pDir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write entrypoint: %v", err)
		}
	}

	makePlugin(rootA, "echo", "echo")
	makePlugin(rootA, "from-a", "from-a")
	makePlugin(rootB, "echo-alt", "echo") // duplicate manifest name; should be ignored
	makePlugin(rootB, "from-b", "from-b")

	reg, err := DiscoverManyWithOptions([]string{rootA, rootB}, nil, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("DiscoverMany() error = %v", err)
	}
	if len(reg.All()) != 3 {
		t.Fatalf("DiscoverMany() found %d plugins, want 3", len(reg.All()))
	}

	echo, ok := reg.Get("echo")
	if !ok {
		t.Fatalf("expected echo to be discovered")
	}
	wantEchoPath := filepath.Join(rootA, "echo")
	if echo.Path != wantEchoPath {
		t.Fatalf("expected first-root echo path %q, got %q", wantEchoPath, echo.Path)
	}
}

func TestDiscoverMany_RecursiveManifestScan(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "team", "plugin-x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested plugin dir: %v", err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: plugin-x
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(nested, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nested, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	reg, err := DiscoverManyWithOptions([]string{root}, nil, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("DiscoverMany() error = %v", err)
	}
	if _, ok := reg.Get("plugin-x"); !ok {
		t.Fatalf("expected nested plugin to be discovered")
	}
}

func TestValidateManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest *Manifest
		wantErr  bool
	}{
		{
			name: "valid manifest",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: false,
		},
		{
			name: "missing manifest spec",
			manifest: &Manifest{
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
		{
			name: "unsupported manifest version",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: 99,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
		{
			name: "missing name",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
		{
			name: "missing protocol",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
		{
			name: "missing entrypoint",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        1,
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
		{
			name: "missing commands",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
			},
			wantErr: true,
		},
		{
			name: "path traversal in entrypoint",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "../evil/run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
		{
			name: "custom command is valid",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "token_refresh", Type: CommandTypeWrite}},
			},
			wantErr: false,
		},
		{
			name: "fact output declaration is valid",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
				FactOutputs: []FactOutputRule{{
					When:              FactOutputWhen{Command: "poll"},
					From:              "state_updates",
					FactType:          "test.snapshot",
					CompatibilityView: "mirror_object",
				}},
			},
			wantErr: false,
		},
		{
			name: "fact output must reference declared command",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "poll", Type: CommandTypeWrite}},
				FactOutputs: []FactOutputRule{{
					When:     FactOutputWhen{Command: "handle"},
					From:     "state_updates",
					FactType: "test.snapshot",
				}},
			},
			wantErr: true,
		},
		{
			name: "names-only values are valid",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands: Commands{{
					Name: "handle",
					Type: CommandTypeWrite,
					Values: &Values{
						Consume: []string{"payload.url", "payload.*"},
						Emit: []EmittedValues{{
							Event:  "content_ready",
							Values: []string{"payload.url", "payload.content_hash"},
						}},
					},
				}},
			},
			wantErr: false,
		},
		{
			name: "values emit event is required",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands: Commands{{
					Name: "handle",
					Type: CommandTypeWrite,
					Values: &Values{
						Emit: []EmittedValues{{Values: []string{"payload.result"}}},
					},
				}},
			},
			wantErr: true,
		},
		{
			name: "values consume must be payload rooted",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands: Commands{{
					Name: "handle",
					Type: CommandTypeWrite,
					Values: &Values{
						Consume: []string{"context.url"},
					},
				}},
			},
			wantErr: true,
		},
		{
			name: "invalid command format",
			manifest: &Manifest{
				ManifestSpec:    SupportedManifestSpec,
				ManifestVersion: SupportedManifestVersion,
				Name:            "test",
				Protocol:        2,
				Entrypoint:      "run.sh",
				Commands:        Commands{{Name: "bad command!", Type: CommandTypeWrite}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateManifest(tt.manifest)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateManifest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCommandValuesUnmarshalYAML(t *testing.T) {
	data := []byte(`manifest_spec: ductile.plugin
manifest_version: 1
name: named-values
version: 0.1.0
protocol: 2
entrypoint: run.sh
commands:
  - name: handle
    type: write
    input_schema:
      url: string
      message: string
    values:
      consume:
        - payload.url
        - payload.message
      emit:
        - event: content_ready
          values:
            - payload.url
            - payload.content_hash
`)
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if err := validateManifest(&manifest); err != nil {
		t.Fatalf("validateManifest: %v", err)
	}
	values := manifest.Commands[0].Values
	if values == nil {
		t.Fatal("values missing")
		return
	}
	if got, want := values.Consume[1], "payload.message"; got != want {
		t.Fatalf("Consume[1] = %q, want %q", got, want)
	}
	if got, want := values.Emit[0].Event, "content_ready"; got != want {
		t.Fatalf("Emit[0].Event = %q, want %q", got, want)
	}
	if got, want := values.Emit[0].Values[1], "payload.content_hash"; got != want {
		t.Fatalf("Emit[0].Values[1] = %q, want %q", got, want)
	}
}

func TestValidateTrust(t *testing.T) {
	tests := []struct {
		name    string
		setupFn func(t *testing.T) (entrypoint, pluginPath, pluginsDir string)
		wantErr bool
	}{
		{
			name: "valid executable",
			setupFn: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				// Resolve macOS /var → /private/var so the symlink check
				// (intentionally false here) doesn't false-positive.
				if resolved, err := filepath.EvalSymlinks(dir); err == nil {
					dir = resolved
				}
				pluginDir := filepath.Join(dir, "test")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				entrypoint := filepath.Join(pluginDir, "run.sh")
				if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatalf("write entrypoint: %v", err)
				}

				return entrypoint, pluginDir, dir
			},
			wantErr: false,
		},
		{
			name: "non-executable",
			setupFn: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				pluginDir := filepath.Join(dir, "test")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				entrypoint := filepath.Join(pluginDir, "run.sh")
				if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\n"), 0o644); err != nil {
					t.Fatalf("write entrypoint: %v", err)
				} // Not executable

				return entrypoint, pluginDir, dir
			},
			wantErr: true,
		},
		{
			name: "world-writable plugin directory",
			setupFn: func(t *testing.T) (string, string, string) {
				// Note: This test may not work on all filesystems (e.g., macOS APFS temp dirs)
				// where permissions are restricted. Skip if chmod fails to set world-writable.
				dir := t.TempDir()
				pluginDir := filepath.Join(dir, "test")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				// Explicitly chmod to world-writable
				if err := os.Chmod(pluginDir, 0777); err != nil {
					t.Skip("cannot set world-writable on this filesystem")
				}

				// Verify it actually became world-writable
				info, _ := os.Stat(pluginDir)
				if info.Mode().Perm()&0002 == 0 {
					t.Skip("filesystem does not support world-writable directories")
				}

				entrypoint := filepath.Join(pluginDir, "run.sh")
				if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatalf("write entrypoint: %v", err)
				}

				return entrypoint, pluginDir, dir
			},
			wantErr: true,
		},
		{
			name: "nonexistent entrypoint",
			setupFn: func(t *testing.T) (string, string, string) {
				dir := t.TempDir()
				pluginDir := filepath.Join(dir, "test")
				if err := os.Mkdir(pluginDir, 0o755); err != nil {
					t.Fatalf("mkdir plugin dir: %v", err)
				}

				entrypoint := filepath.Join(pluginDir, "nonexistent.sh")

				return entrypoint, pluginDir, dir
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entrypoint, pluginPath, pluginsDir := tt.setupFn(t)

			logger := func(string, string, ...any) {}

			err := validateTrust(entrypoint, pluginPath, pluginsDir, false, logger)

			if (err != nil) != tt.wantErr {
				t.Errorf("validateTrust() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTrustInRoots(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	// Resolve macOS /var → /private/var symlinks so the (false) symlink
	// guard doesn't reject the happy path.
	if resolved, err := filepath.EvalSymlinks(rootA); err == nil {
		rootA = resolved
	}
	if resolved, err := filepath.EvalSymlinks(rootB); err == nil {
		rootB = resolved
	}
	pluginDir := filepath.Join(rootB, "test")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	entrypoint := filepath.Join(pluginDir, "run.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	logger := func(string, string, ...any) {}

	if err := validateTrustInRoots(entrypoint, pluginDir, []string{rootA, rootB}, false, logger); err != nil {
		t.Fatalf("validateTrustInRoots() error = %v, want nil", err)
	}

	if err := validateTrustInRoots(entrypoint, pluginDir, []string{rootA}, false, logger); err == nil {
		t.Fatal("validateTrustInRoots() expected error when rootB is not configured")
	}
}

func TestPluginSupportsCommand(t *testing.T) {
	plugin := &Plugin{
		Commands: Commands{
			{Name: "poll", Type: CommandTypeWrite},
			{Name: "health", Type: CommandTypeRead},
		},
	}

	if !plugin.SupportsCommand("poll") {
		t.Error("should support poll")
	}

	if !plugin.SupportsCommand("health") {
		t.Error("should support health")
	}

	if plugin.SupportsCommand("handle") {
		t.Error("should not support handle")
	}
}

func TestDiscover_TypedCommandMetadata(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "test-plugin")
	if err := os.Mkdir(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}

	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: test-plugin
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
  - name: health
    type: read
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	logger := func(level, msg string, args ...any) {}
	reg, err := DiscoverManyWithOptions([]string{dir}, logger, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	p, ok := reg.Get("test-plugin")
	if !ok {
		t.Fatalf("test-plugin not discovered")
	}
	if got := p.GetReadCommands(); len(got) != 1 || got[0] != "health" {
		t.Fatalf("GetReadCommands() = %v, want [health]", got)
	}
	if got := p.GetWriteCommands(); len(got) != 1 || got[0] != "poll" {
		t.Fatalf("GetWriteCommands() = %v, want [poll]", got)
	}
}

func TestDiscover_ConcurrencySafe_DefaultTrue(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "safe-default")
	if err := os.Mkdir(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}

	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: safe-default
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	reg, err := DiscoverManyWithOptions([]string{dir}, func(level, msg string, args ...any) {}, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	p, ok := reg.Get("safe-default")
	if !ok {
		t.Fatalf("plugin not discovered")
	}
	if !p.ConcurrencySafe {
		t.Fatalf("expected default ConcurrencySafe=true when manifest field omitted")
	}
}

func TestDiscover_ConcurrencySafe_FalseHint(t *testing.T) {
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "serial-plugin")
	if err := os.Mkdir(pluginDir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}

	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: serial-plugin
version: 1.0.0
protocol: 2
entrypoint: run.sh
concurrency_safe: false
commands:
  - name: poll
    type: write
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}

	reg, err := DiscoverManyWithOptions([]string{dir}, func(level, msg string, args ...any) {}, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	p, ok := reg.Get("serial-plugin")
	if !ok {
		t.Fatalf("plugin not discovered")
	}
	if p.ConcurrencySafe {
		t.Fatalf("expected ConcurrencySafe=false from manifest hint")
	}
}

func TestDiscover_FactOutputsLoaded(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "watcher")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: watcher
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: poll
    type: write
fact_outputs:
  - when:
      command: poll
    from: state_updates
    fact_type: watcher.snapshot
    compatibility_view: mirror_object
`
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(run.sh): %v", err)
	}

	reg, err := DiscoverManyWithOptions([]string{tmpDir}, func(level, msg string, args ...any) {}, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("DiscoverManyWithOptions: %v", err)
	}

	p, ok := reg.Get("watcher")
	if !ok {
		t.Fatal("expected plugin watcher to be discovered")
	}
	if len(p.FactOutputs) != 1 {
		t.Fatalf("len(FactOutputs) = %d, want 1", len(p.FactOutputs))
	}
	if p.FactOutputs[0].FactType != "watcher.snapshot" || p.FactOutputs[0].CompatibilityView != "mirror_object" {
		t.Fatalf("unexpected FactOutputs[0] = %+v", p.FactOutputs[0])
	}
}

// stopwatchManifestFixture writes a manifest with the supplied stopwatch
// YAML block (or omits the block when blockYAML is empty) and an
// executable run.sh, then returns the temp dir.
func stopwatchManifestFixture(t *testing.T, blockYAML string) string {
	t.Helper()
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "p")
	if err := os.Mkdir(pluginDir, 0o755); err != nil {
		t.Fatalf("Mkdir(pluginDir): %v", err)
	}
	manifest := `manifest_spec: ductile.plugin
manifest_version: 1
name: p
version: 1.0.0
protocol: 2
entrypoint: run.sh
commands:
  - name: handle
    type: write
`
	if blockYAML != "" {
		manifest += blockYAML
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest): %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile(run.sh): %v", err)
	}
	return tmpDir
}

func TestDiscover_StopwatchMaxSubs_Parsed(t *testing.T) {
	tmp := stopwatchManifestFixture(t, "stopwatch:\n  max_subs: 64\n")
	reg, err := DiscoverManyWithOptions([]string{tmp}, func(level, msg string, args ...any) {}, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("DiscoverManyWithOptions: %v", err)
	}
	p, ok := reg.Get("p")
	if !ok {
		t.Fatal("plugin not discovered")
	}
	if p.Stopwatch == nil {
		t.Fatal("Stopwatch field nil after parsing manifest with stopwatch block")
	}
	if p.Stopwatch.MaxSubs != 64 {
		t.Errorf("MaxSubs = %d, want 64", p.Stopwatch.MaxSubs)
	}
}

func TestDiscover_StopwatchAbsent_LeavesFieldNil(t *testing.T) {
	tmp := stopwatchManifestFixture(t, "")
	reg, err := DiscoverManyWithOptions([]string{tmp}, func(level, msg string, args ...any) {}, DiscoverOptions{AllowSymlinks: true})
	if err != nil {
		t.Fatalf("DiscoverManyWithOptions: %v", err)
	}
	p, _ := reg.Get("p")
	if p.Stopwatch != nil {
		t.Errorf("Stopwatch should be nil when not declared; got %+v", p.Stopwatch)
	}
}

func TestDiscover_StopwatchMaxSubs_RejectsOutOfRange(t *testing.T) {
	// Discovery logs invalid manifests and continues (doesn't error the
	// whole walk), so the contract is: rejected plugins do not appear in
	// the registry. Direct validateManifest path is exercised by
	// TestValidateManifest_StopwatchMaxSubsRange.
	for _, value := range []string{"-1", "257", "1000"} {
		t.Run("value="+value, func(t *testing.T) {
			tmp := stopwatchManifestFixture(t, "stopwatch:\n  max_subs: "+value+"\n")
			var loggedErrors []string
			logger := func(level, msg string, args ...any) {
				if level == "warn" {
					loggedErrors = append(loggedErrors, msg)
				}
			}
			reg, err := DiscoverManyWithOptions([]string{tmp}, logger, DiscoverOptions{AllowSymlinks: true})
			if err != nil {
				t.Fatalf("DiscoverManyWithOptions returned hard error: %v", err)
			}
			if _, ok := reg.Get("p"); ok {
				t.Fatalf("plugin with max_subs=%s should have been rejected, but is in registry", value)
			}
			if len(loggedErrors) == 0 {
				t.Errorf("expected a warn-level log when rejecting invalid manifest")
			}
		})
	}
}

func TestValidateManifest_StopwatchMaxSubsRange(t *testing.T) {
	// Direct path-coverage on validateManifest (avoids filesystem fixture
	// for boundary values, especially 0 and 1).
	base := func(maxSubs int) *Manifest {
		return &Manifest{
			ManifestSpec:    SupportedManifestSpec,
			ManifestVersion: SupportedManifestVersion,
			Name:            "p",
			Version:         "1.0.0",
			Protocol:        2,
			Entrypoint:      "run.sh",
			Commands:        Commands{{Name: "handle", Type: CommandTypeWrite}},
			Stopwatch:       &StopwatchManifest{MaxSubs: maxSubs},
		}
	}

	// 0 means "use default" — no error, but the field is preserved as 0
	// so the dispatcher's "use default" branch fires.
	if err := validateManifest(base(0)); err != nil {
		t.Errorf("max_subs=0 should be accepted as 'use default': %v", err)
	}
	if err := validateManifest(base(1)); err != nil {
		t.Errorf("max_subs=1 (boundary low) should be accepted: %v", err)
	}
	if err := validateManifest(base(256)); err != nil {
		t.Errorf("max_subs=256 (boundary high) should be accepted: %v", err)
	}

	// Out-of-range values must fail.
	for _, bad := range []int{-1, 257, 10000} {
		if err := validateManifest(base(bad)); err == nil {
			t.Errorf("max_subs=%d should fail validation, got nil", bad)
		}
	}
}
