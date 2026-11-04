package config

// The corpus TestAshlarParity runs both loaders over (card 153 slice 3):
//   - every YAML raw-string literal in this package's other tests that has at
//     least one top-level Config key and no include list, each loaded as a
//     one-file config. A literal with an include list is one file of a
//     directory a test builds around it; alone, both loaders stop at the
//     missing include, which proves nothing, so it is left out and counted
//     (parityLiteralCases). The directory tests that matter are rebuilt whole
//     in parityDirectoryTestCases;
//   - every config directory under test/fixtures/docker (config-shapes holds
//     the three posture shapes and the ro-mount shape);
//   - the files `ductile config init` writes;
//   - hand-built multi-file cases for each Problem-now bug and each decision
//     note item (parityHandBuiltCases).

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/mattjoyce/ductile/internal/secrets"
	"go.yaml.in/yaml/v3"
)

// parityFixtureRoot is test/fixtures/docker, relative to this package.
const parityFixtureRoot = "../../test/fixtures/docker"

// parityPlaceholders are substituted into every corpus file before a run, so a
// fixture config can name an absolute path or a uid without fixing one here.
var parityPlaceholders = map[string]string{
	"__WORKER_UID__":    "4181",
	"__WORKER_GID__":    "4181",
	"__UNTRUSTED_UID__": "4182",
	"__UNTRUSTED_GID__": "4182",
	"__API_PORT__":      "18799",
}

// parityCase is one corpus entry: a tree of files (from src, then files), the
// root file to load, environment variables set for the run, the files to
// age-encrypt with a key written beside the root, vault secrets to seed (which
// also writes the key), and scope files to lock into .checksums.
type parityCase struct {
	id      string
	src     string
	files   map[string]string
	root    string
	env     map[string]string
	encrypt []string
	ageKey  bool
	vault   map[string]string
	lock    []string
}

// parityEnvRef matches a ${NAME} reference; parityDotEnvName matches the name
// on a KEY=VALUE line, dashes included because load() accepts them.
var (
	parityEnvRef     = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	parityDotEnvName = regexp.MustCompile(`(?m)^[ \t]*(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_-]*)[ \t]*=`)
)

// parityEnvGlobals are the variables load() reads directly.
var parityEnvGlobals = []string{ageKeyEnvVar, "DUCTILE_CONFIG_DIR"}

// referencedEnv lists every variable a materialized case can read: each
// ${NAME} in any of its files, each name a .env file in it defines, and the
// variables load() reads directly. A hermetic run unsets all of them except
// the ones the case sets.
func referencedEnv(t *testing.T, dir string) []string {
	t.Helper()
	names := make(map[string]bool)
	for _, g := range parityEnvGlobals {
		names[g] = true
	}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range parityEnvRef.FindAllSubmatch(data, -1) {
			names[string(m[1])] = true
		}
		if strings.HasSuffix(p, ".env") {
			for _, m := range parityDotEnvName.FindAllSubmatch(data, -1) {
				names[string(m[1])] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s for environment references: %v", dir, err)
	}
	return sortedCopy(mapKeys(names))
}

func (c parityCase) rootFile() string {
	if c.root == "" {
		return "config.yaml"
	}
	return c.root
}

// materialize writes the case into a fresh directory and returns the absolute
// path of its root file. "__CASE_DIR__" in any file becomes that directory.
func (c parityCase) materialize(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "case")
	if c.src != "" {
		copyParityTree(t, c.src, dir)
	}
	for rel, content := range c.files {
		writeParityFile(t, filepath.Join(dir, rel), []byte(content))
	}
	substituteParityPlaceholders(t, dir)
	switch {
	case c.vault != nil:
		seedVault(t, dir, c.vault)
	case c.ageKey || len(c.encrypt) > 0:
		writeParityAgeKey(t, dir, c.encrypt)
	}
	if len(c.lock) > 0 {
		if _, err := GenerateChecksumsWithReport(dir, c.lock, false); err != nil {
			t.Fatalf("lock %v in %s: %v", c.lock, c.id, err)
		}
	}
	return filepath.Join(dir, c.rootFile())
}

func writeParityFile(t *testing.T, p string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyParityTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

func substituteParityPlaceholders(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yaml") {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "__CASE_DIR__", dir)
		text = strings.ReplaceAll(text, "__STATE_ROOT__", filepath.Join(dir, "accounts"))
		for k, v := range parityPlaceholders {
			text = strings.ReplaceAll(text, k, v)
		}
		return os.WriteFile(p, []byte(text), 0o600)
	})
	if err != nil {
		t.Fatalf("substitute placeholders in %s: %v", dir, err)
	}
}

// writeParityAgeKey writes a fresh age identity to <dir>/age.key and encrypts
// the named files to it in place.
func writeParityAgeKey(t *testing.T, dir string, encrypt []string) {
	t.Helper()
	id, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	writeParityFile(t, filepath.Join(dir, "age.key"), []byte(id.String()+"\n"))
	for _, rel := range encrypt {
		p := filepath.Join(dir, rel)
		plain, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		cipher, err := secrets.Encrypt(plain, []age.Recipient{id.Recipient()})
		if err != nil {
			t.Fatal(err)
		}
		writeParityFile(t, p, cipher)
	}
}

// parityCorpus returns every case, sorted by id, and the number of literals
// left out as fragments of a directory setup.
func parityCorpus(t *testing.T) ([]parityCase, int) {
	t.Helper()
	cases, skipped := parityLiteralCases(t)
	cases = append(cases, parityFixtureCases(t)...)
	cases = append(cases, parityConfigInitCase())
	cases = append(cases, parityHandBuiltCases()...)
	cases = append(cases, parityDirectoryTestCases()...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].id < cases[j].id })
	for i := 1; i < len(cases); i++ {
		if cases[i].id == cases[i-1].id {
			t.Fatalf("duplicate parity case id %q", cases[i].id)
		}
	}
	return cases, skipped
}

// configTopLevelKeys is the set of YAML keys Config decodes at the top level.
func configTopLevelKeys() map[string]bool {
	keys := make(map[string]bool)
	rt := reflect.TypeFor[Config]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// parityLiteralCases extracts every raw-string literal from this package's
// other test files that parses as a YAML mapping with a Config key. The id is
// the source file plus a hash of the literal, so moving a test does not move
// its golden entries but editing the literal does. Literals with an include
// list are skipped and counted.
func parityLiteralCases(t *testing.T) (cases []parityCase, skipped int) {
	t.Helper()
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	known := configTopLevelKeys()
	seen := make(map[string]bool)
	fset := token.NewFileSet()
	for _, p := range paths {
		if strings.HasPrefix(p, "parity") || p == "helpers_parity_test.go" {
			continue
		}
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.HasPrefix(lit.Value, "`") {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			shaped, includes := configShapedYAML(text, known)
			if !shaped {
				return true
			}
			sum := sha256.Sum256([]byte(text))
			id := hex.EncodeToString(sum[:6])
			if seen[id] {
				return true
			}
			seen[id] = true
			if includes {
				skipped++
				return true
			}
			cases = append(cases, parityCase{
				id:    "literal/" + strings.TrimSuffix(p, "_test.go") + "/" + id,
				files: map[string]string{"config.yaml": text},
			})
			return true
		})
	}
	return cases, skipped
}

// configShapedYAML reports whether text is a YAML mapping with a Config key,
// and whether it carries an include list (a fragment of a directory setup).
func configShapedYAML(text string, known map[string]bool) (shaped, includes bool) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return false, false
	}
	for k := range doc {
		if known[k] {
			shaped = true
		}
	}
	_, includes = doc["include"]
	return shaped, includes
}

// parityFixtureCases is every directory under test/fixtures/docker holding a
// config.yaml. Fixture configs name age.key as their key file, so each case
// gets a fresh key beside it.
func parityFixtureCases(t *testing.T) []parityCase {
	t.Helper()
	var cases []parityCase
	err := filepath.WalkDir(parityFixtureRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "config.yaml" {
			return nil
		}
		dir := filepath.Dir(p)
		rel, err := filepath.Rel(parityFixtureRoot, dir)
		if err != nil {
			return err
		}
		cases = append(cases, parityCase{id: "fixture/" + filepath.ToSlash(rel), src: dir, ageKey: true})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", parityFixtureRoot, err)
	}
	if len(cases) == 0 {
		t.Fatalf("no fixture configs under %s; the corpus would be incomplete", parityFixtureRoot)
	}
	return cases
}

// parityConfigInitCase is what `ductile config init` writes
// (cmd/ductile/config_manage.go runConfigInit), with its absolute plugins path
// pointed at the case directory. The card names cmd/ductile/templates for this;
// those templates are the CLI skill text, and config init writes these files.
func parityConfigInitCase() parityCase {
	return parityCase{
		id: "config-init",
		files: map[string]string{
			"config.yaml": `service:
  name: ductile
  tick_interval: 60s
  log_level: info
  log_format: json
state:
  path: ./data/ductile.db
api:
  enabled: false
  listen: "127.0.0.1:8080"
plugin_roots:
  - __CASE_DIR__/plugins
plugins: {}
`,
			"routes.yaml":   "routes: []\n",
			"webhooks.yaml": "webhooks: []\n",
		},
	}
}
