package config

// TestAshlarParity runs load() and loadAshlar() over the parity corpus in two
// modes and requires every difference to be a reviewed golden entry
// (parityGolden). It fails on a difference with no entry, on an entry coded
// UNDECIDED, and on an entry whose difference no longer occurs. Entries coded
// UNDECIDED-pending are logged, not failed: they are differences this slice
// could not classify from the decision note, awaiting a code.
//
// To print the current difference set as golden entries:
//
//	go test ./internal/config -run TestAshlarParity -parity.dump

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/mattjoyce/ashlar"
)

var (
	parityDump    = flag.Bool("parity.dump", false, "print the current parity differences as golden entries")
	parityVerbose = flag.Bool("parity.verbose", false, "log both loaders' outcome for every case and mode")
)

// parityCode is the reason a difference is accepted (card 153 slice 3).
type parityCode string

const (
	codeBugFixed  parityCode = "BUG-FIXED"         // a Problem-now bug load() has and Ashlar does not
	codeTightened parityCode = "TIGHTENED"         // a decision-G tightening
	codeWidened   parityCode = "WIDENED"           // decision H: Ashlar reads files load() does not
	codeUndecided parityCode = "UNDECIDED"         // no decision; fails the harness
	codePending   parityCode = "UNDECIDED-pending" // not classifiable from the decision note; reported
)

type parityEntry struct {
	code parityCode
	note string
}

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

func runLegacyLoad(path string, m parityMode, env map[string]string) paritySide {
	var side paritySide
	side.env = withParityEnv(env, func() {
		side.cfg, _, side.err = load(path, m.verify, m.validate, m.defaults)
	})
	if side.cfg != nil {
		for f := range side.cfg.SourceFiles {
			side.files = append(side.files, f)
		}
	}
	return side
}

func runAshlarLoad(path string, m parityMode, env map[string]string) paritySide {
	var side paritySide
	var res *ashlarLoad
	side.env = withParityEnv(env, func() {
		res, side.err = loadAshlar(path, m.verify, m.validate, m.defaults)
	})
	if res != nil {
		side.unknown = res.unknown
	}
	if res != nil && side.err == nil {
		side.cfg, side.files = res.cfg, res.files
	}
	return side
}

// withParityEnv sets env, runs fn, and returns the variables fn added or
// changed; the process environment is restored afterwards.
func withParityEnv(env map[string]string, fn func()) map[string]string {
	saved := environMap()
	defer restoreEnviron(saved)
	for k, v := range env {
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

// parityDifference is one way the two sides differ. key is stable across runs;
// detail is for the reader.
type parityDifference struct {
	key, detail string
}

func diffParitySides(legacy, next paritySide) []parityDifference {
	var out []parityDifference
	for _, u := range next.unknown {
		out = append(out, parityDifference{key: "unknown-key:" + u, detail: "decision C emulation pruned this key"})
	}
	switch {
	case legacy.err == nil && next.err != nil:
		return append(out, parityDifference{key: "outcome:ashlar-error:" + ashlarErrorClass(next.err), detail: next.err.Error()})
	case legacy.err != nil && next.err == nil:
		return append(out, parityDifference{key: "outcome:legacy-error", detail: legacy.err.Error()})
	case legacy.err != nil:
		return out
	}
	for _, p := range diffJSON("", canonicalConfig(legacy.cfg), canonicalConfig(next.cfg)) {
		out = append(out, parityDifference{key: "field:" + p.path, detail: p.detail})
	}
	if l, n := sortedCopy(legacy.files), sortedCopy(next.files); !reflect.DeepEqual(l, n) {
		out = append(out, parityDifference{key: "files", detail: fmt.Sprintf("legacy=%v ashlar=%v", l, n)})
	}
	for _, name := range unionKeys(legacy.env, next.env) {
		lv, lok := legacy.env[name]
		nv, nok := next.env[name]
		if lok != nok || lv != nv {
			out = append(out, parityDifference{key: "env:" + name, detail: fmt.Sprintf("legacy=%q(%t) ashlar=%q(%t)", lv, lok, nv, nok)})
		}
	}
	return out
}

// ashlarErrorClass names an error by its Ashlar category and pointer, or
// "ductile" when it is not an Ashlar error. Paths and messages stay out of the
// key so it is stable across machines.
func ashlarErrorClass(err error) string {
	classes := []struct {
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
		{"validation", ashlar.ErrValidation},
		{"not-found", ashlar.ErrNotFound},
	}
	for _, c := range classes {
		if errors.Is(err, c.err) {
			var aerr *ashlar.Error
			if errors.As(err, &aerr) && aerr.Pointer != "" {
				return c.name + ":" + string(aerr.Pointer)
			}
			return c.name
		}
	}
	return "ductile"
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

type jsonDifference struct{ path, detail string }

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
	return []jsonDifference{{path: at, detail: fmt.Sprintf("legacy=%s ashlar=%s", compactJSON(a), compactJSON(b))}}
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

// isolateParityEnv points HOME at an empty directory and clears the age key
// override, so the operator's own ~/.config/ductile cannot leak into a run.
func isolateParityEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(ageKeyEnvVar, "")
	t.Setenv("DUCTILE_CONFIG_DIR", "")
}

// outcomeName labels a pair of results for the coverage tally.
func outcomeName(legacy, next paritySide) string {
	switch {
	case legacy.err == nil && next.err == nil:
		return "both loaded"
	case legacy.err != nil && next.err != nil:
		return "both refused"
	case legacy.err != nil:
		return "only ashlar loaded"
	default:
		return "only legacy loaded"
	}
}

func TestAshlarParity(t *testing.T) {
	isolateParityEnv(t)
	seen := make(map[string]bool)
	tally := make(map[string]int)
	var dump []string
	pending := 0
	corpus := parityCorpus(t)
	for _, c := range corpus {
		path := c.materialize(t)
		for _, m := range parityModes {
			legacy := runLegacyLoad(path, m, c.env)
			next := runAshlarLoad(path, m, c.env)
			tally[m.name+": "+outcomeName(legacy, next)]++
			if *parityVerbose {
				t.Logf("%s %s: %s\n    legacy: %v\n    ashlar: %v", c.id, m.name, outcomeName(legacy, next), legacy.err, next.err)
			}
			for _, d := range diffParitySides(legacy, next) {
				key := c.id + " " + m.name + " " + d.key
				seen[key] = true
				entry, ok := parityGolden[key]
				switch {
				case *parityDump:
					dump = append(dump, fmt.Sprintf("\t%q: {codeX, %q},", key, d.detail))
				case !ok:
					t.Errorf("unrecorded difference %s\n    %s", key, d.detail)
				case entry.code == codeUndecided:
					t.Errorf("UNDECIDED difference %s: %s", key, entry.note)
				case entry.code == codePending:
					pending++
					t.Logf("UNDECIDED-pending %s: %s", key, entry.note)
				}
			}
		}
	}
	if *parityDump {
		t.Logf("current differences:\n%s", strings.Join(dump, "\n"))
		return
	}
	for key := range parityGolden {
		if !seen[key] {
			t.Errorf("golden entry no longer occurs (remove it, or restore the case): %s", key)
		}
	}
	outcomes := mapKeys(boolKeys(tally))
	sort.Strings(outcomes)
	var lines []string
	for _, o := range outcomes {
		lines = append(lines, fmt.Sprintf("%s=%d", o, tally[o]))
	}
	t.Logf("parity: %d cases x %d modes; %s; %d golden entries, %d UNDECIDED-pending",
		len(corpus), len(parityModes), strings.Join(lines, ", "), len(parityGolden), pending)
}

func boolKeys(m map[string]int) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}
