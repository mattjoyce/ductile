package config

// The corpus TestAshlarParity runs both loaders over (card 153 slice 3):
//   - every YAML raw-string literal in this package's other tests that has at
//     least one top-level Config key, each loaded as a one-file config;
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
// root file to load, environment variables set for the run, and the files to
// age-encrypt with a key written beside the root.
type parityCase struct {
	id      string
	src     string
	files   map[string]string
	root    string
	env     map[string]string
	encrypt []string
	ageKey  bool
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
	if c.ageKey || len(c.encrypt) > 0 {
		writeParityAgeKey(t, dir, c.encrypt)
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

// parityCorpus returns every case, sorted by id.
func parityCorpus(t *testing.T) []parityCase {
	t.Helper()
	cases := parityLiteralCases(t)
	cases = append(cases, parityFixtureCases(t)...)
	cases = append(cases, parityConfigInitCase())
	cases = append(cases, parityHandBuiltCases()...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].id < cases[j].id })
	for i := 1; i < len(cases); i++ {
		if cases[i].id == cases[i-1].id {
			t.Fatalf("duplicate parity case id %q", cases[i].id)
		}
	}
	return cases
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
// its golden entries but editing the literal does.
func parityLiteralCases(t *testing.T) []parityCase {
	t.Helper()
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	known := configTopLevelKeys()
	seen := make(map[string]bool)
	var cases []parityCase
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
			if err != nil || !isConfigShapedYAML(text, known) {
				return true
			}
			sum := sha256.Sum256([]byte(text))
			id := hex.EncodeToString(sum[:6])
			if seen[id] {
				return true
			}
			seen[id] = true
			cases = append(cases, parityCase{
				id:    "literal/" + strings.TrimSuffix(p, "_test.go") + "/" + id,
				files: map[string]string{"config.yaml": text},
			})
			return true
		})
	}
	return cases
}

func isConfigShapedYAML(text string, known map[string]bool) bool {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return false
	}
	for k := range doc {
		if known[k] {
			return true
		}
	}
	return false
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
