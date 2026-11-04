package config

// TestAshlarParity runs load() and loadAshlar() over the parity corpus in two
// modes and requires every difference to be a reviewed golden entry
// (parityGolden) whose pinned legacy and Ashlar values match what each loader
// produced. It fails on a difference with no entry, on a pinned value that
// moved, on an entry coded UNDECIDED, and on an entry whose difference no
// longer occurs. GATED entries pass here and fail TestAshlarCutoverReadiness.
//
// Each case runs with a hermetic environment: every variable the case's files
// reference is unset first, then the case's own variables are set.
//
// To print the current difference set as golden rows:
//
//	go test ./internal/config -run TestAshlarParity -parity.dump

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mattjoyce/ashlar"
	"go.yaml.in/yaml/v3"
)

var (
	parityDump    = flag.Bool("parity.dump", false, "print the current parity differences as golden rows")
	parityVerbose = flag.Bool("parity.verbose", false, "log both loaders' outcome for every case and mode")
)

// parityMode is one load() variant: lenient is LoadLenient, full is Load.
type parityMode struct {
	name                       string
	verify, validate, defaults bool
}

var parityModes = []parityMode{
	{name: "lenient"},
	{name: "full", verify: true, validate: true, defaults: true},
}

// paritySide is one loader's observable result.
type paritySide struct {
	cfg     *Config
	files   []string
	unknown []string
	env     map[string]string
	err     error
}

// parityRun is one case materialized: its root path, its directory (for
// normalising paths out of values), and its environment plan.
type parityRun struct {
	path, dir string
	set       map[string]string
	unset     []string
}

func newParityRun(t *testing.T, c parityCase) parityRun {
	t.Helper()
	path := c.materialize(t)
	dir := strings.TrimSuffix(path, string(filepath.Separator)+filepath.FromSlash(c.rootFile()))
	var unset []string
	for _, name := range referencedEnv(t, dir) {
		if _, ok := c.env[name]; !ok {
			unset = append(unset, name)
		}
	}
	return parityRun{path: path, dir: dir, set: c.env, unset: unset}
}

func runLegacyLoad(r parityRun, m parityMode) paritySide {
	var side paritySide
	side.env = withParityEnv(r, func() {
		side.cfg, _, side.err = load(r.path, m.verify, m.validate, m.defaults)
	})
	if side.cfg != nil {
		for f := range side.cfg.SourceFiles {
			side.files = append(side.files, f)
		}
	}
	return side
}

func runAshlarLoad(r parityRun, m parityMode) paritySide {
	var side paritySide
	var res *ashlarLoad
	side.env = withParityEnv(r, func() {
		res, side.err = loadAshlar(r.path, m.verify, m.validate, m.defaults)
	})
	if res != nil {
		side.unknown = res.unknown
	}
	if res != nil && side.err == nil {
		side.cfg, side.files = res.cfg, res.files
	}
	return side
}

// withParityEnv applies the run's environment plan, runs fn, and returns the
// variables fn added or changed; the process environment is restored after.
func withParityEnv(r parityRun, fn func()) map[string]string {
	saved := environMap()
	defer restoreEnviron(saved)
	for _, k := range r.unset {
		_ = os.Unsetenv(k)
	}
	for k, v := range r.set {
		_ = os.Setenv(k, v)
	}
	before := environMap()
	fn()
	added := make(map[string]string)
	for k, v := range environMap() {
		if old, ok := before[k]; !ok || old != v {
			added[k] = v
		}
	}
	return added
}

func environMap() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func restoreEnviron(saved map[string]string) {
	for k := range environMap() {
		if _, ok := saved[k]; !ok {
			_ = os.Unsetenv(k)
		}
	}
	for k, v := range saved {
		if cur, ok := os.LookupEnv(k); !ok || cur != v {
			_ = os.Setenv(k, v)
		}
	}
}

// parityDifference is one way the two sides differ: a stable key and each
// side's value, with the case directory replaced by $CASE.
type parityDifference struct {
	key, legacy, ashlar string
}

func diffParitySides(r parityRun, legacy, next paritySide) []parityDifference {
	norm := caseNormaliser(r.dir)
	var out []parityDifference
	for _, u := range next.unknown {
		out = append(out, parityDifference{key: "unknown-key:" + u, legacy: "ignored", ashlar: "collected (v0.4.0 DecodeStrict refuses)"})
	}
	for _, name := range unionKeys(legacy.env, next.env) {
		lv, lok := legacy.env[name]
		nv, nok := next.env[name]
		if lok != nok || lv != nv {
			out = append(out, parityDifference{key: "env:" + name, legacy: envValue(lv, lok), ashlar: envValue(nv, nok)})
		}
	}
	lc, nc := classifyParityError(legacy.err, norm), classifyParityError(next.err, norm)
	if lc.class != nc.class {
		out = append(out, parityDifference{key: "outcome", legacy: lc.detail, ashlar: nc.detail})
	}
	if legacy.err != nil || next.err != nil {
		return out
	}
	for _, p := range diffJSON("", canonicalConfig(legacy.cfg), canonicalConfig(next.cfg)) {
		out = append(out, parityDifference{key: "field:" + p.path, legacy: norm(p.legacy), ashlar: norm(p.ashlar)})
	}
	if l, n := relFiles(legacy.files, norm), relFiles(next.files, norm); l != n {
		out = append(out, parityDifference{key: "files", legacy: l, ashlar: n})
	}
	return out
}

func envValue(v string, set bool) string {
	if !set {
		return "(not exported)"
	}
	return fmt.Sprintf("%q", v)
}

func relFiles(files []string, norm func(string) string) string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = norm(f)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// caseNormaliser replaces the case directory, with or without its leading
// slash (Ashlar's origins are root-relative), by $CASE.
func caseNormaliser(dir string) func(string) string {
	return func(s string) string {
		s = strings.ReplaceAll(s, dir, "$CASE")
		return strings.ReplaceAll(s, strings.TrimPrefix(dir, "/"), "$CASE")
	}
}

// parityUnsetVarName matches the variable load()'s validation names when a
// plugin config holds an unresolved ${VAR}. It walks a map, so with two unset
// variables the one it names changes between runs; the harness masks it.
var parityUnsetVarName = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\} is not set`)

// parityErrorShape is an error's cause class, which decides equality between
// the two loaders, and a detail string, which golden entries pin.
type parityErrorShape struct{ class, detail string }

// classifyParityError sorts an error into a cause class both loaders share:
// ok, validation (load()'s own validation, run by both), not-found,
// include-cycle, syntax, duplicate-key, trailing-content, type,
// unknown-field, unresolved, composition, or ductile (any other loader-stage
// error, compared by message). Legacy loader-stage errors are plain
// fmt.Errorf values, so this test-only classifier reads their text.
func classifyParityError(err error, norm func(string) string) parityErrorShape {
	if err == nil {
		return parityErrorShape{"ok", "ok"}
	}
	msg := parityUnsetVarName.ReplaceAllString(norm(firstLine(err.Error())), "${…} is not set")
	if strings.HasPrefix(msg, "invalid configuration: ") || strings.HasPrefix(msg, "configuration validation failed: ") {
		return parityErrorShape{"validation:" + msg, "validation: " + msg}
	}
	var aerr *ashlar.Error
	if errors.As(err, &aerr) {
		name := ashlarCategory(err)
		return parityErrorShape{name, "ashlar " + name + " at " + string(aerr.Pointer)}
	}
	var te *yaml.TypeError
	switch {
	case errors.As(err, &te):
		return parityErrorShape{"type", "yaml type error: " + norm(strings.Join(te.Errors, "; "))}
	case strings.Contains(msg, "circular dependency"):
		return parityErrorShape{"include-cycle", "legacy include cycle"}
	case errors.Is(err, fs.ErrNotExist) || strings.Contains(msg, "file not found"):
		return parityErrorShape{"not-found", "legacy not found"}
	case strings.Contains(msg, "already defined"):
		return parityErrorShape{"duplicate-key", "legacy duplicate key"}
	case strings.HasPrefix(msg, "failed to parse YAML: "):
		return parityErrorShape{"syntax", "legacy YAML syntax error"}
	}
	return parityErrorShape{"ductile:" + msg, "ductile: " + msg}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// ashlarCategory names the most specific Ashlar sentinel err matches. A decode
// ErrValidation is a type mismatch, the same class as a yaml.v3 TypeError.
func ashlarCategory(err error) string {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"unknown-field", ashlar.ErrUnknownField},
		{"duplicate-key", ashlar.ErrDuplicateKey},
		{"trailing-content", ashlar.ErrTrailingContent},
		{"include-cycle", ashlar.ErrIncludeCycle},
		{"unresolved", ashlar.ErrUnresolved},
		{"syntax", ashlar.ErrSyntax},
		{"composition", ashlar.ErrComposition},
		{"type", ashlar.ErrValidation},
		{"not-found", ashlar.ErrNotFound},
	} {
		if errors.Is(err, c.err) {
			return c.name
		}
	}
	return "ashlar"
}

// canonicalConfig is cfg as a JSON tree with the fields the card excludes from
// the comparison (SourceFiles, ResolvedSecrets, Pipelines, ConfigDir) and
// Include, which Ashlar strips from every fragment by design (decision K).
func canonicalConfig(cfg *Config) any {
	c := *cfg
	c.SourceFiles, c.ResolvedSecrets, c.Pipelines, c.ConfigDir, c.Include = nil, nil, nil, "", nil
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Sprintf("marshal error: %v", err)
	}
	var tree any
	if err := json.Unmarshal(data, &tree); err != nil {
		return fmt.Sprintf("unmarshal error: %v", err)
	}
	return tree
}

type jsonDifference struct{ path, legacy, ashlar string }

// diffJSON lists the paths at which two JSON trees differ, descending through
// objects and equal-length arrays.
func diffJSON(at string, a, b any) []jsonDifference {
	am, aIsMap := a.(map[string]any)
	bm, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		var out []jsonDifference
		for _, k := range unionKeys(am, bm) {
			out = append(out, diffJSON(at+"/"+k, am[k], bm[k])...)
		}
		return out
	}
	as, aIsList := a.([]any)
	bs, bIsList := b.([]any)
	if aIsList && bIsList && len(as) == len(bs) {
		var out []jsonDifference
		for i := range as {
			out = append(out, diffJSON(fmt.Sprintf("%s/%d", at, i), as[i], bs[i])...)
		}
		return out
	}
	if reflect.DeepEqual(a, b) {
		return nil
	}
	if at == "" {
		at = "/"
	}
	return []jsonDifference{{path: at, legacy: compactJSON(a), ashlar: compactJSON(b)}}
}

func compactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

func unionKeys[V any](a, b map[string]V) []string {
	set := make(map[string]bool, len(a)+len(b))
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	return sortedCopy(mapKeys(set))
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// isolateParityEnv points HOME at an empty directory and clears the variables
// load() reads directly, so the operator's own ~/.config/ductile and
// environment cannot leak into a run.
func isolateParityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(ageKeyEnvVar, "")
	t.Setenv("DUCTILE_CONFIG_DIR", "")
}
