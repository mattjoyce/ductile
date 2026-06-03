package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/mattjoyce/ductile/internal/config"
)

// backupScope is a nested ladder of what a backup archive contains.
// Each level adds the previous level's contents.
type backupScope int

const (
	scopeDB backupScope = iota
	scopeConfig
	scopePlugins
	scopeAll
)

func (s backupScope) name() string {
	switch s {
	case scopeDB:
		return "db"
	case scopeConfig:
		return "config"
	case scopePlugins:
		return "plugins"
	case scopeAll:
		return "all"
	}
	return "unknown"
}

func parseScope(raw string) (backupScope, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "config":
		return scopeConfig, nil
	case "db":
		return scopeDB, nil
	case "plugins":
		return scopePlugins, nil
	case "all":
		return scopeAll, nil
	}
	return scopeConfig, fmt.Errorf("unknown scope %q (want db|config|plugins|all)", raw)
}

// pluginExcludeNames are directory/file names skipped when walking plugin
// roots. Build artefacts and VCS metadata get rebuilt on the target host.
var pluginExcludeNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
	".DS_Store":    true,
}

// pluginExcludeSuffixes are file extensions skipped within plugin roots.
var pluginExcludeSuffixes = []string{".pyc", ".pyo"}

// configFiles are the files inside the ductile config dir that go into
// every scope ≥ config. Listed explicitly per the "no wildcards" feedback.
var configFiles = []string{
	"config.yaml",
	"api.yaml",
	"plugins.yaml",
	"pipelines.yaml",
	"webhooks.yaml",
	".checksums",
}

// configRuntimeExcludes are explicitly enumerated for the EXCLUDED report.
var configRuntimeExcludes = []string{
	"ductile.db-shm",
	"ductile.db-wal",
	"ductile.pid",
	"backups",
}

// backupManifest is serialised to BACKUP_MANIFEST.txt inside the archive.
// It is the self-documenting record of what the archive contains, when, and
// from where.
type backupManifest struct {
	DuctileVersion string               `yaml:"ductile_version"`
	DuctileCommit  string               `yaml:"ductile_commit"`
	Hostname       string               `yaml:"hostname"`
	CreatedAt      string               `yaml:"created_at"`
	Scope          string               `yaml:"scope"`
	SourceDBPath   string               `yaml:"source_db_path"`
	SourceDBSHA256 string               `yaml:"source_db_sha256"`
	SourceConfig   string               `yaml:"source_config_dir,omitempty"`
	Included       []string             `yaml:"included"`
	Excluded       []manifestExcluded   `yaml:"excluded,omitempty"`
	PluginRoots    []manifestPluginRoot `yaml:"plugin_roots,omitempty"`
	EnvFiles       []manifestEnvFile    `yaml:"env_files,omitempty"`
	Warnings       []string             `yaml:"warnings,omitempty"`
}

type manifestExcluded struct {
	Reason string   `yaml:"reason"`
	Items  []string `yaml:"items"`
}

type manifestPluginRoot struct {
	ArchivePath string `yaml:"archive_path"`
	SourcePath  string `yaml:"source_path"`
}

type manifestEnvFile struct {
	ArchivePath string `yaml:"archive_path"`
	SourcePath  string `yaml:"source_path"`
}

// runSystemBackup writes a consistent, scoped snapshot of ductile state to
// a single .tar.gz file. Scopes nest as a ladder: db ⊂ config ⊂ plugins ⊂
// all. Default scope is `config`.
//
// Database snapshot uses SQLite VACUUM INTO — atomic, point-in-time, safe
// against concurrent writers (gateway can be running). The temp snapshot
// is written to a sibling of the destination, added to the archive, then
// removed.
//
// Operator owns naming and retention via cron/launchd shell glue.
func runSystemBackup(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to configuration file or directory")
	destPath := fs.String("to", "", "Destination .tar.gz path for the archive")
	scopeFlag := fs.String("scope", "config", "Backup scope: db|config|plugins|all (default config)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Flag error: %v\n", err)
		return 1
	}

	if *destPath == "" {
		fmt.Fprintln(os.Stderr, "--to <file> is required")
		printSystemBackupHelp()
		return 1
	}

	scope, err := parseScope(*scopeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	resolvedDest, err := filepath.Abs(*destPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve destination path: %v\n", err)
		return 1
	}

	if _, err := os.Stat(resolvedDest); err == nil {
		fmt.Fprintf(os.Stderr,
			"destination already exists: %s (refusing to overwrite)\n", resolvedDest)
		return 1
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "stat destination: %v\n", err)
		return 1
	}

	cfg, configDir, err := loadBackupConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}

	srcDB, err := filepath.Abs(cfg.State.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve source DB path: %v\n", err)
		return 1
	}

	plan, err := buildBackupPlan(scope, cfg, configDir, srcDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build backup plan: %v\n", err)
		return 1
	}

	printBackupPlan(plan)

	if err := os.MkdirAll(filepath.Dir(resolvedDest), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "create destination directory: %v\n", err)
		return 1
	}

	start := time.Now()
	if err := writeBackupArchive(resolvedDest, plan); err != nil {
		fmt.Fprintf(os.Stderr, "backup failed: %v\n", err)
		_ = os.Remove(resolvedDest)
		return 1
	}

	info, err := os.Stat(resolvedDest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stat written archive: %v\n", err)
		return 1
	}

	fmt.Printf("\nbackup written: %s (%d bytes, %.2fs)\n",
		resolvedDest, info.Size(), time.Since(start).Seconds())
	return 0
}

// loadBackupConfig resolves the config dir and loads enough to find paths.
// Returns the loaded config and the resolved config directory.
func loadBackupConfig(configPath string) (*config.Config, string, error) {
	resolved := configPath
	if resolved == "" {
		discovered, err := config.DiscoverConfigDir()
		if err != nil {
			return nil, "", fmt.Errorf("discover config: %w", err)
		}
		resolved = discovered
	}
	cfg, err := config.Load(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("load config: %w", err)
	}
	if cfg.State.Path == "" {
		return nil, "", fmt.Errorf("config does not declare state.path")
	}
	absConfig, err := filepath.Abs(resolved)
	if err != nil {
		return nil, "", fmt.Errorf("resolve config dir: %w", err)
	}
	info, err := os.Stat(absConfig)
	if err == nil && !info.IsDir() {
		absConfig = filepath.Dir(absConfig)
	}
	return cfg, absConfig, nil
}

// backupPlan captures everything a backup invocation will do, ahead of
// execution. Built once, printed for operator awareness, then executed.
type backupPlan struct {
	scope       backupScope
	dest        string
	srcDB       string
	configDir   string
	pluginRoots []manifestPluginRoot
	envFiles    []manifestEnvFile
	manifest    backupManifest
}

func buildBackupPlan(
	scope backupScope, cfg *config.Config, configDir, srcDB string,
) (*backupPlan, error) {
	plan := &backupPlan{
		scope:     scope,
		srcDB:     srcDB,
		configDir: configDir,
	}

	hostname, _ := os.Hostname()
	plan.manifest = backupManifest{
		DuctileVersion: version,
		DuctileCommit:  gitCommit,
		Hostname:       hostname,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		Scope:          scope.name(),
		SourceDBPath:   srcDB,
	}

	plan.manifest.Included = append(plan.manifest.Included, "db/ductile.sqlite")

	if scope >= scopeConfig {
		plan.manifest.SourceConfig = configDir
		for _, f := range configFiles {
			path := filepath.Join(configDir, f)
			if _, err := os.Stat(path); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return nil, fmt.Errorf("stat %s: %w", path, err)
			}
			plan.manifest.Included = append(plan.manifest.Included, "config/"+f)
		}
		plan.manifest.Excluded = append(plan.manifest.Excluded, manifestExcluded{
			Reason: "runtime sidecars / pid / nested backups dir",
			Items:  configRuntimeExcludes,
		})
		plan.manifest.Warnings = append(plan.manifest.Warnings,
			"archive contains api.yaml bearer token; treat as secret")
	}

	if scope >= scopePlugins {
		usedNames := map[string]int{}
		for _, root := range cfg.EffectivePluginRoots() {
			absRoot, err := filepath.Abs(root)
			if err != nil {
				return nil, fmt.Errorf("resolve plugin root %s: %w", root, err)
			}
			base := filepath.Base(absRoot)
			if usedNames[base] > 0 {
				base = fmt.Sprintf("%s-%d", base, usedNames[base])
			}
			usedNames[filepath.Base(absRoot)]++
			archivePath := "plugins/" + base
			plan.pluginRoots = append(plan.pluginRoots, manifestPluginRoot{
				ArchivePath: archivePath,
				SourcePath:  absRoot,
			})
			plan.manifest.PluginRoots = append(plan.manifest.PluginRoots,
				manifestPluginRoot{ArchivePath: archivePath, SourcePath: absRoot})
			plan.manifest.Included = append(plan.manifest.Included, archivePath+"/...")
		}
		plan.manifest.Excluded = append(plan.manifest.Excluded, manifestExcluded{
			Reason: "VCS / build artefacts inside plugin roots",
			Items: []string{
				".git", "node_modules", ".venv", "venv",
				"__pycache__", ".DS_Store", "*.pyc", "*.pyo",
			},
		})
	} else {
		plan.manifest.Excluded = append(plan.manifest.Excluded, manifestExcluded{
			Reason: fmt.Sprintf("scope=%s < plugins", scope.name()),
			Items:  []string{"plugin_roots"},
		})
	}

	if scope >= scopeAll {
		usedNames := map[string]int{}
		for _, envPath := range cfg.EnvironmentVars.Include {
			absEnv, err := filepath.Abs(envPath)
			if err != nil {
				return nil, fmt.Errorf("resolve env file %s: %w", envPath, err)
			}
			parent := filepath.Base(filepath.Dir(absEnv))
			fname := filepath.Base(absEnv)
			ns := parent
			if usedNames[ns] > 0 {
				ns = fmt.Sprintf("%s-%d", ns, usedNames[ns])
			}
			usedNames[parent]++
			archivePath := "env/" + ns + "/" + fname
			plan.envFiles = append(plan.envFiles, manifestEnvFile{
				ArchivePath: archivePath,
				SourcePath:  absEnv,
			})
			plan.manifest.EnvFiles = append(plan.manifest.EnvFiles,
				manifestEnvFile{ArchivePath: archivePath, SourcePath: absEnv})
			plan.manifest.Included = append(plan.manifest.Included, archivePath)
		}
		if len(cfg.EnvironmentVars.Include) > 0 {
			plan.manifest.Warnings = append(plan.manifest.Warnings,
				"archive contains environment_vars files (secrets); encrypt at rest")
		}
	} else {
		plan.manifest.Excluded = append(plan.manifest.Excluded, manifestExcluded{
			Reason: fmt.Sprintf("scope=%s < all", scope.name()),
			Items:  []string{"environment_vars files"},
		})
	}

	sort.Strings(plan.manifest.Included)
	return plan, nil
}

func printBackupPlan(plan *backupPlan) {
	fmt.Printf("INCLUDED (scope=%s):\n", plan.scope.name())
	for _, line := range plan.manifest.Included {
		fmt.Printf("  + %s\n", line)
	}
	fmt.Println()
	fmt.Println("EXCLUDED:")
	for _, ex := range plan.manifest.Excluded {
		fmt.Printf("  - %s\n", ex.Reason)
		for _, item := range ex.Items {
			fmt.Printf("      %s\n", item)
		}
	}
	if len(plan.manifest.Warnings) > 0 {
		fmt.Println()
		for _, w := range plan.manifest.Warnings {
			fmt.Printf("WARNING: %s\n", w)
		}
	}
	fmt.Println()
}

func writeBackupArchive(dest string, plan *backupPlan) error {
	tmpDB, err := os.CreateTemp(filepath.Dir(dest), "ductile-snapshot-*.sqlite")
	if err != nil {
		return fmt.Errorf("create temp snapshot: %w", err)
	}
	tmpDBPath := tmpDB.Name()
	_ = tmpDB.Close()
	_ = os.Remove(tmpDBPath) // VACUUM INTO requires the path to not yet exist
	defer func() { _ = os.Remove(tmpDBPath) }()

	if err := runVacuumInto(plan.srcDB, tmpDBPath); err != nil {
		return err
	}

	dbHash, err := sha256File(tmpDBPath)
	if err != nil {
		return fmt.Errorf("hash snapshot: %w", err)
	}
	plan.manifest.SourceDBSHA256 = dbHash

	out, err := openFileWithinParent(dest, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	defer func() { _ = out.Close() }()

	gz := gzip.NewWriter(out)
	defer func() { _ = gz.Close() }()

	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }()

	if err := tarAddFile(tw, tmpDBPath, "db/ductile.sqlite"); err != nil {
		return err
	}

	if plan.scope >= scopeConfig {
		for _, f := range configFiles {
			path := filepath.Join(plan.configDir, f)
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}
			if err := tarAddFile(tw, path, "config/"+f); err != nil {
				return err
			}
		}
	}

	if plan.scope >= scopePlugins {
		for _, root := range plan.pluginRoots {
			if err := tarAddPluginRoot(tw, root.SourcePath, root.ArchivePath); err != nil {
				return err
			}
		}
	}

	if plan.scope >= scopeAll {
		for _, env := range plan.envFiles {
			if _, err := os.Stat(env.SourcePath); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return fmt.Errorf("stat env %s: %w", env.SourcePath, err)
			}
			if err := tarAddFile(tw, env.SourcePath, env.ArchivePath); err != nil {
				return err
			}
		}
	}

	manifestYAML, err := yaml.Marshal(plan.manifest)
	if err != nil {
		return fmt.Errorf("render manifest: %w", err)
	}
	if err := tarAddBytes(tw, "BACKUP_MANIFEST.txt", manifestYAML, 0o644); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("close gzip: %w", err)
	}
	return nil
}

func tarAddFile(tw *tar.Writer, srcPath, archivePath string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", srcPath, err)
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("tar header for %s: %w", srcPath, err)
	}
	hdr.Name = archivePath
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header for %s: %w", archivePath, err)
	}
	f, err := openFileWithinParent(srcPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", srcPath, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("write tar body for %s: %w", archivePath, err)
	}
	return nil
}

func tarAddBytes(tw *tar.Writer, archivePath string, body []byte, mode int64) error {
	hdr := &tar.Header{
		Name:    archivePath,
		Size:    int64(len(body)),
		Mode:    mode,
		ModTime: time.Now().UTC(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write tar header for %s: %w", archivePath, err)
	}
	if _, err := tw.Write(body); err != nil {
		return fmt.Errorf("write tar body for %s: %w", archivePath, err)
	}
	return nil
}

// tarAddPluginRoot walks src and adds files to the archive under archiveBase.
// Skips VCS / build-artefact directories and files.
func tarAddPluginRoot(tw *tar.Writer, src, archiveBase string) error {
	src = filepath.Clean(src)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		name := info.Name()
		if pluginExcludeNames[name] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		for _, suf := range pluginExcludeSuffixes {
			if !info.IsDir() && strings.HasSuffix(name, suf) {
				return nil
			}
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		archivePath := archiveBase + "/" + filepath.ToSlash(rel)

		if info.IsDir() {
			hdr := &tar.Header{
				Name:    archivePath + "/",
				Mode:    int64(info.Mode().Perm()),
				ModTime: info.ModTime(),
			}
			hdr.Typeflag = tar.TypeDir
			return tw.WriteHeader(hdr)
		}
		if !info.Mode().IsRegular() {
			// Skip symlinks, sockets, devices — they don't transport meaningfully.
			return nil
		}
		return tarAddFile(tw, path, archivePath)
	})
}

func sha256File(path string) (string, error) {
	f, err := openFileWithinParent(path, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func openFileWithinParent(path string, flag int, perm os.FileMode) (*os.File, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(absPath))
	if err != nil {
		return nil, fmt.Errorf("open parent root: %w", err)
	}
	defer func() { _ = root.Close() }()
	return root.OpenFile(filepath.Base(absPath), flag, perm)
}

// runVacuumInto opens a fresh connection to the source DB and executes
// VACUUM INTO against the destination. Uses a parallel connection so this
// is safe to run while the gateway is up.
func runVacuumInto(srcPath, destPath string) error {
	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000;"); err != nil {
		return fmt.Errorf("busy_timeout pragma: %w", err)
	}
	// VACUUM INTO is the documented atomic-snapshot mechanism. It cannot run
	// inside a transaction. SQLite accepts a bound scalar expression for the
	// destination path, so keep the operator-provided path out of SQL text.
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?;", destPath); err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", destPath, err)
	}
	return nil
}

func printSystemBackupHelp() {
	// Use os.Stdout.WriteString so go vet does not flag the date-format
	// strings in the cron example as accidental printf directives.
	_, _ = os.Stdout.WriteString(`Usage: ductile system backup --to <file.tar.gz> [--scope SCOPE] [--config PATH]
Write a consistent, scoped snapshot of ductile state to a single tar.gz.
Database snapshot uses SQLite VACUUM INTO — safe under concurrent writers.

Scopes (nested ladder; each level adds to the previous):
  db        VACUUM INTO snapshot only
  config    db + ductile config dir (config.yaml, api.yaml, ...) [default]
  plugins   config + every directory under plugin_roots
            (excludes .git, node_modules, .venv, __pycache__, *.pyc)
  all       plugins + every file under environment_vars.include

Each invocation prints its INCLUDED / EXCLUDED list before doing the work
and embeds a BACKUP_MANIFEST.txt inside the archive documenting the same.

NOTE: the vault blob (vault.age) and its age key are NOT yet in any scope.
Back up the vault blob separately and custody the key out-of-band (e.g. a
password manager); a vault backup is restorable only with its matching key.

Refuses if --to file already exists. Operator owns naming and retention.

Example cron pattern:
  STAMP=$(date -u +%Y%m%dT%H%M%SZ)
  ductile system backup --to /backups/ductile-$STAMP.tar.gz --scope config
  find /backups -mtime +7 -delete
`)
}
