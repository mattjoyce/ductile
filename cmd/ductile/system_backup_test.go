package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/mattjoyce/ductile/internal/config"
)

func TestParseScope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want backupScope
		err  bool
	}{
		{"", scopeConfig, false},
		{"db", scopeDB, false},
		{"config", scopeConfig, false},
		{"plugins", scopePlugins, false},
		{"all", scopeAll, false},
		{"DB", scopeDB, false},
		{"  config  ", scopeConfig, false},
		{"everything", 0, true},
	}
	for _, c := range cases {
		got, err := parseScope(c.in)
		if c.err {
			if err == nil {
				t.Errorf("parseScope(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseScope(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseScope(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// fixture builds a minimal config dir + state DB + plugin root + env file
// for round-tripping each backup scope through writeBackupArchive.
type backupFixture struct {
	configDir  string
	dbPath     string
	pluginRoot string
	envFile    string
}

func newBackupFixture(t *testing.T) *backupFixture {
	t.Helper()
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}

	for _, name := range configFiles {
		path := filepath.Join(cfgDir, name)
		if err := os.WriteFile(path, []byte("# "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for _, name := range []string{"ductile.db-shm", "ductile.db-wal", "ductile.pid"} {
		path := filepath.Join(cfgDir, name)
		if err := os.WriteFile(path, []byte("runtime sidecar"), 0o600); err != nil {
			t.Fatalf("write sidecar %s: %v", path, err)
		}
	}

	dbPath := filepath.Join(cfgDir, "ductile.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		"CREATE TABLE marker (id INTEGER PRIMARY KEY); INSERT INTO marker VALUES (42);"); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	pluginRoot := filepath.Join(root, "plugin-root-A")
	echoDir := filepath.Join(pluginRoot, "echo")
	gitDir := filepath.Join(pluginRoot, "echo", ".git")
	venvDir := filepath.Join(pluginRoot, "echo", ".venv")
	pycacheDir := filepath.Join(pluginRoot, "echo", "__pycache__")
	for _, d := range []string{echoDir, gitDir, venvDir, pycacheDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	for path, body := range map[string]string{
		filepath.Join(echoDir, "run.sh"):             "#!/bin/sh\n",
		filepath.Join(echoDir, "manifest.yaml"):      "name: echo\n",
		filepath.Join(gitDir, "HEAD"):                "ref: refs/heads/main\n",
		filepath.Join(venvDir, "pyvenv.cfg"):         "version=3.12\n",
		filepath.Join(pycacheDir, "run.cpython.pyc"): "garbage\n",
		filepath.Join(echoDir, "run.pyc"):            "garbage\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	envFile := filepath.Join(root, "secrets", "demo", ".env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
		t.Fatalf("mkdir env: %v", err)
	}
	if err := os.WriteFile(envFile, []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	return &backupFixture{
		configDir:  cfgDir,
		dbPath:     dbPath,
		pluginRoot: pluginRoot,
		envFile:    envFile,
	}
}

func (fx *backupFixture) plan(scope backupScope, dest string) *backupPlan {
	roots := []manifestPluginRoot{}
	envFiles := []manifestEnvFile{}

	if scope >= scopePlugins {
		roots = append(roots, manifestPluginRoot{
			ArchivePath: "plugins/plugin-root-A",
			SourcePath:  fx.pluginRoot,
		})
	}
	if scope >= scopeAll {
		envFiles = append(envFiles, manifestEnvFile{
			ArchivePath: "env/demo/.env",
			SourcePath:  fx.envFile,
		})
	}

	mf := backupManifest{
		DuctileVersion: "test",
		DuctileCommit:  "abc1234",
		Hostname:       "test-host",
		CreatedAt:      "2026-05-06T00:00:00Z",
		Scope:          scope.name(),
		SourceDBPath:   fx.dbPath,
		SourceConfig:   fx.configDir,
		PluginRoots:    roots,
		EnvFiles:       envFiles,
		Included:       []string{"db/ductile.sqlite"},
	}
	if scope >= scopeConfig {
		for _, f := range configFiles {
			mf.Included = append(mf.Included, "config/"+f)
		}
	}

	return &backupPlan{
		scope:       scope,
		dest:        dest,
		srcDB:       fx.dbPath,
		configDir:   fx.configDir,
		pluginRoots: roots,
		envFiles:    envFiles,
		manifest:    mf,
	}
}

func TestWriteBackupArchiveScopeDB(t *testing.T) {
	t.Parallel()
	fx := newBackupFixture(t)
	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeBackupArchive(dest, fx.plan(scopeDB, dest)); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}
	got := tarPaths(t, dest)
	want := []string{"BACKUP_MANIFEST.txt", "uid.txt", "db/ductile.sqlite"}
	if !equalSorted(got, want) {
		t.Fatalf("scope=db archive contents\n got: %v\nwant: %v", got, want)
	}
}

func TestWriteBackupArchiveScopeConfig(t *testing.T) {
	t.Parallel()
	fx := newBackupFixture(t)
	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeBackupArchive(dest, fx.plan(scopeConfig, dest)); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}
	got := tarPaths(t, dest)
	want := []string{
		"BACKUP_MANIFEST.txt",
		"uid.txt",
		"db/ductile.sqlite",
		"config/.checksums",
		"config/api.yaml",
		"config/config.yaml",
		"config/pipelines.yaml",
		"config/plugins.yaml",
		"config/webhooks.yaml",
	}
	if !equalSorted(got, want) {
		t.Fatalf("scope=config archive contents\n got: %v\nwant: %v", got, want)
	}
	for _, runtimeFile := range []string{"ductile.db-shm", "ductile.db-wal", "ductile.pid"} {
		for _, p := range got {
			if strings.HasSuffix(p, runtimeFile) {
				t.Errorf("runtime file %s should be excluded but appeared in archive", runtimeFile)
			}
		}
	}
}

func TestWriteBackupArchiveScopePlugins(t *testing.T) {
	t.Parallel()
	fx := newBackupFixture(t)
	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeBackupArchive(dest, fx.plan(scopePlugins, dest)); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}
	got := tarPaths(t, dest)

	mustContain := []string{
		"plugins/plugin-root-A/echo/run.sh",
		"plugins/plugin-root-A/echo/manifest.yaml",
	}
	for _, p := range mustContain {
		if !contains(got, p) {
			t.Errorf("expected %s in archive; not found", p)
		}
	}

	mustNotContain := []string{
		"plugins/plugin-root-A/echo/.git/HEAD",
		"plugins/plugin-root-A/echo/.venv/pyvenv.cfg",
		"plugins/plugin-root-A/echo/__pycache__/run.cpython.pyc",
		"plugins/plugin-root-A/echo/run.pyc",
	}
	for _, p := range mustNotContain {
		if contains(got, p) {
			t.Errorf("expected %s to be excluded; found in archive", p)
		}
	}
}

func TestWriteBackupArchiveScopeAll(t *testing.T) {
	t.Parallel()
	fx := newBackupFixture(t)
	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeBackupArchive(dest, fx.plan(scopeAll, dest)); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}
	got := tarPaths(t, dest)
	if !contains(got, "env/demo/.env") {
		t.Errorf("expected env/demo/.env in scope=all archive; got: %v", got)
	}
}

func TestWriteBackupArchiveManifestRecordsSHA256(t *testing.T) {
	t.Parallel()
	fx := newBackupFixture(t)
	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	plan := fx.plan(scopeDB, dest)
	if err := writeBackupArchive(dest, plan); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}
	body := tarReadFile(t, dest, "BACKUP_MANIFEST.txt")
	var m backupManifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(m.SourceDBSHA256) != 64 {
		t.Errorf("manifest source_db_sha256 should be 64 hex chars, got %q", m.SourceDBSHA256)
	}
	if m.Scope != "db" {
		t.Errorf("manifest scope = %q, want db", m.Scope)
	}
}

func TestBackupIncludesVaultBlobNotKey(t *testing.T) {
	fx := newBackupFixture(t)

	// A vault blob and its age key live in the config dir.
	if err := os.WriteFile(filepath.Join(fx.configDir, "vault.age"), []byte("AGE-ENCRYPTED-VAULT-BLOB"), 0o600); err != nil {
		t.Fatalf("write vault.age: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fx.configDir, "age.key"), []byte("AGE-SECRET-KEY-PRIVATE"), 0o600); err != nil {
		t.Fatalf("write age.key: %v", err)
	}
	// Make the env var deterministic so the key resolves to the config-dir file.
	t.Setenv("DUCTILE_AGE_KEY_FILE", "")

	cfg := &config.Config{}
	cfg.State.Path = fx.dbPath
	cfg.Secrets.AgeKeyFile = "age.key"

	plan, err := buildBackupPlan(scopeConfig, cfg, fx.configDir, fx.dbPath)
	if err != nil {
		t.Fatalf("buildBackupPlan: %v", err)
	}

	// Manifest: the blob is included; the age key is recorded as out-of-band excluded.
	if !contains(plan.manifest.Included, "config/vault.age") {
		t.Errorf("manifest must include the vault blob; got %v", plan.manifest.Included)
	}
	keyExcluded := false
	for _, ex := range plan.manifest.Excluded {
		if strings.Contains(ex.Reason, "out-of-band") {
			for _, it := range ex.Items {
				if strings.HasSuffix(it, "age.key") {
					keyExcluded = true
				}
			}
		}
	}
	if !keyExcluded {
		t.Errorf("manifest must record the age key as excluded out-of-band; got %+v", plan.manifest.Excluded)
	}

	// Archive: contains the blob, NEVER the age key.
	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeBackupArchive(dest, plan); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}
	got := tarPaths(t, dest)
	if !contains(got, "config/vault.age") {
		t.Errorf("archive must contain config/vault.age; got %v", got)
	}
	for _, p := range got {
		if strings.HasSuffix(p, "age.key") {
			t.Fatalf("age key must NEVER be in the archive; found %s", p)
		}
	}

	// The in-archive manifest tells the truth about the pairing.
	body := string(tarReadFile(t, dest, "BACKUP_MANIFEST.txt"))
	if !strings.Contains(body, "vault.age") || !strings.Contains(body, "out-of-band") {
		t.Errorf("manifest should record the vault.age + out-of-band-key pairing; got:\n%s", body)
	}
}

func TestBackupUIDWrittenAndPaired(t *testing.T) {
	fx := newBackupFixture(t)
	cfg := &config.Config{}
	cfg.State.Path = fx.dbPath

	plan, err := buildBackupPlan(scopeConfig, cfg, fx.configDir, fx.dbPath)
	if err != nil {
		t.Fatalf("buildBackupPlan: %v", err)
	}

	uid := plan.manifest.BackupUID
	if len(uid) != backupUIDLen {
		t.Fatalf("backup uid %q length = %d, want %d", uid, len(uid), backupUIDLen)
	}
	for _, r := range uid {
		if !strings.ContainsRune(backupUIDAlphabet, r) {
			t.Fatalf("backup uid %q contains out-of-alphabet char %q", uid, r)
		}
	}

	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := writeBackupArchive(dest, plan); err != nil {
		t.Fatalf("writeBackupArchive: %v", err)
	}

	// uid.txt is present and matches the manifest UID.
	if got := strings.TrimSpace(string(tarReadFile(t, dest, "uid.txt"))); got != uid {
		t.Errorf("uid.txt = %q, want manifest uid %q", got, uid)
	}
	// The manifest carries the same UID for the pairing record.
	body := string(tarReadFile(t, dest, "BACKUP_MANIFEST.txt"))
	if !strings.Contains(body, "backup_uid: "+uid) {
		t.Errorf("manifest must record backup_uid %q; got:\n%s", uid, body)
	}

	// Two backups get distinct UIDs.
	plan2, err := buildBackupPlan(scopeConfig, cfg, fx.configDir, fx.dbPath)
	if err != nil {
		t.Fatalf("buildBackupPlan (2): %v", err)
	}
	if plan2.manifest.BackupUID == uid {
		t.Errorf("expected distinct UIDs across backups; both = %q", uid)
	}
}

// tarPaths lists archive entry names, sorted.
func tarPaths(t *testing.T, archive string) []string {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		names = append(names, hdr.Name)
	}
	sort.Strings(names)
	return names
}

func tarReadFile(t *testing.T, archive, target string) []byte {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		if hdr.Name == target {
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			return body
		}
	}
	t.Fatalf("entry %q not found in archive", target)
	return nil
}

func equalSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string{}, a...)
	bc := append([]string{}, b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// #104: lock the secret-zero exclusion under the ADR filesystem layout, where the
// age key lives in a `secret/` SUBDIR of the config dir (secrets.age_key_file points
// inside the config tree). A `--scope config`/`all` archive must contain NEITHER the
// key file NOR any `secret/` entry — the key's safety is enforced, not incidental.
func TestBackupNeverContainsSecretZeroADRLayout(t *testing.T) {
	for _, scope := range []backupScope{scopeConfig, scopeAll} {
		t.Run(scope.name(), func(t *testing.T) {
			fx := newBackupFixture(t)
			// ADR layout: vault.age in the config dir; age key in <configDir>/secret/age.key.
			if err := os.WriteFile(filepath.Join(fx.configDir, "vault.age"), []byte("AGE-ENCRYPTED-VAULT-BLOB"), 0o600); err != nil {
				t.Fatalf("write vault.age: %v", err)
			}
			if err := os.MkdirAll(filepath.Join(fx.configDir, "secret"), 0o700); err != nil {
				t.Fatalf("mkdir secret: %v", err)
			}
			if err := os.WriteFile(filepath.Join(fx.configDir, "secret", "age.key"), []byte("AGE-SECRET-KEY-PRIVATE"), 0o600); err != nil {
				t.Fatalf("write secret/age.key: %v", err)
			}
			t.Setenv("DUCTILE_AGE_KEY_FILE", "")

			cfg := &config.Config{}
			cfg.State.Path = fx.dbPath
			cfg.Secrets.AgeKeyFile = "secret/age.key" // co-located, sshd-style (ADR)

			plan, err := buildBackupPlan(scope, cfg, fx.configDir, fx.dbPath)
			if err != nil {
				t.Fatalf("buildBackupPlan: %v", err)
			}
			dest := filepath.Join(t.TempDir(), "out.tar.gz")
			if err := writeBackupArchive(dest, plan); err != nil {
				t.Fatalf("writeBackupArchive: %v", err)
			}
			for _, p := range tarPaths(t, dest) {
				if strings.HasSuffix(p, "age.key") || strings.Contains(p, "/secret/") || strings.HasPrefix(p, "secret/") {
					t.Fatalf("secret-zero must NEVER be in a %s archive; found %q", scope.name(), p)
				}
			}
		})
	}
}
