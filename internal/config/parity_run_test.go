package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// parityObservation is one pass over the corpus: every difference keyed
// "<case id> <mode> <difference key>", an outcome tally per mode, and every
// variable name some case references.
type parityObservation struct {
	diffs      map[string]parityDifference
	tally      map[string]int
	referenced map[string]bool
	cases      int
	skipped    int
}

func observeParity(t *testing.T) parityObservation {
	t.Helper()
	corpus, skipped := parityCorpus(t)
	obs := parityObservation{
		diffs:      make(map[string]parityDifference),
		tally:      make(map[string]int),
		referenced: make(map[string]bool),
		cases:      len(corpus),
		skipped:    skipped,
	}
	for _, c := range corpus {
		r := newParityRun(t, c)
		for _, name := range r.unset {
			obs.referenced[name] = true
		}
		for _, m := range parityModes {
			legacy := runLegacyLoad(r, m)
			next := runAshlarLoad(r, m)
			obs.tally[m.name+": "+outcomeName(legacy, next)]++
			if *parityVerbose {
				t.Logf("%s %s: %s\n    legacy: %v\n    ashlar: %v", c.id, m.name, outcomeName(legacy, next), legacy.err, next.err)
			}
			for _, d := range diffParitySides(r, legacy, next) {
				obs.diffs[c.id+" "+m.name+" "+d.key] = d
			}
		}
	}
	return obs
}

// outcomeName labels a pair of results for the coverage tally. Only "both
// loaded" compares whole configs; "both refused" compares error classes.
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
	obs := observeParity(t)
	if *parityDump {
		t.Logf("current differences:\n%s", dumpParityRows(obs.diffs))
		return
	}
	gated := 0
	for _, key := range sortedDiffKeys(obs.diffs) {
		d := obs.diffs[key]
		entry, ok := parityGolden[key]
		switch {
		case !ok:
			t.Errorf("unrecorded difference %s\n    legacy: %s\n    ashlar: %s", key, d.legacy, d.ashlar)
		case entry.legacy != d.legacy || entry.ashlar != d.ashlar:
			t.Errorf("pinned value moved for %s\n    legacy: pinned %s, now %s\n    ashlar: pinned %s, now %s",
				key, entry.legacy, d.legacy, entry.ashlar, d.ashlar)
		case entry.class.code == codeUndecided:
			t.Errorf("UNDECIDED difference %s: %s", key, entry.class.note)
		case entry.class.code == codeGated:
			gated++
		}
	}
	for key := range parityGolden {
		if _, ok := obs.diffs[key]; !ok {
			t.Errorf("golden entry no longer occurs (remove it, or restore the case): %s", key)
		}
	}
	t.Logf("parity: %d cases x %d modes (%d literals skipped as directory-setup fragments); %s; %d golden entries, %d GATED",
		obs.cases, len(parityModes), obs.skipped, formatTally(obs.tally), len(parityGolden), gated)
}

// cutoverReadinessEnv switches TestAshlarCutoverReadiness from checking that
// GATED entries are well formed to failing while any exist. Slice 4 sets it.
const cutoverReadinessEnv = "DUCTILE_CUTOVER_READINESS"

// gatedDecision matches a decision that opens with the decision note's item
// letter, "C: ..." or "G (empty-env prerequisite): ...".
var gatedDecision = regexp.MustCompile(`^[A-M]( \([^)]+\))?: \S`)

// TestAshlarCutoverReadiness has two modes. By default it checks that every
// GATED entry names its decision letter, says what it waits on (an Ashlar
// card, or that no Ashlar card exists and the work is ductile's), and pins
// both values. With DUCTILE_CUTOVER_READINESS=1 it fails while any GATED
// entry exists: each is a decided behaviour the Ashlar path does not provide
// yet, so the loader cutover (card 153 slice 4) cannot proceed.
func TestAshlarCutoverReadiness(t *testing.T) {
	var gated []string
	for _, key := range sortedGoldenKeys() {
		e := parityGolden[key]
		if e.class.code != codeGated {
			continue
		}
		gated = append(gated, fmt.Sprintf("%s [%s; waits on %s]", key, e.class.decision, e.class.waitsOn))
		if !gatedDecision.MatchString(e.class.decision) {
			t.Errorf("GATED %s: decision %q does not open with its decision-note letter", key, e.class.decision)
		}
		if !strings.Contains(e.class.waitsOn, "Ashlar card") {
			t.Errorf("GATED %s: waitsOn %q names neither an Ashlar card nor the absence of one", key, e.class.waitsOn)
		}
		if e.legacy == "" || e.ashlar == "" {
			t.Errorf("GATED %s: both values must be pinned (legacy %q, ashlar %q)", key, e.legacy, e.ashlar)
		}
	}
	if os.Getenv(cutoverReadinessEnv) != "1" {
		t.Logf("%d GATED entries; set %s=1 to fail while any remain", len(gated), cutoverReadinessEnv)
		return
	}
	if len(gated) > 0 {
		t.Errorf("%d GATED parity entries block the cutover:\n  %s", len(gated), strings.Join(gated, "\n  "))
	}
}

func sortedGoldenKeys() []string {
	keys := make([]string, 0, len(parityGolden))
	for k := range parityGolden {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestParityHermeticEnv proves no inherited variable reaches a case: every
// name any case references (and TOKEN) is set to junk in the process, and the
// difference set must not change.
func TestParityHermeticEnv(t *testing.T) {
	isolateParityEnv(t)
	clean := observeParity(t)
	polluted := map[string]bool{"TOKEN": true}
	for name := range clean.referenced {
		polluted[name] = true
	}
	for name := range polluted {
		t.Setenv(name, "parity-polluted-"+name)
	}
	dirty := observeParity(t)
	for _, key := range sortedDiffKeys(clean.diffs) {
		if d, ok := dirty.diffs[key]; !ok || d != clean.diffs[key] {
			t.Errorf("inherited environment changed %s: clean %+v, polluted %+v (present=%t)", key, clean.diffs[key], d, ok)
		}
	}
	for _, key := range sortedDiffKeys(dirty.diffs) {
		if _, ok := clean.diffs[key]; !ok {
			t.Errorf("inherited environment added %s: %+v", key, dirty.diffs[key])
		}
	}
}

func sortedDiffKeys(m map[string]parityDifference) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatTally(tally map[string]int) string {
	keys := make([]string, 0, len(tally))
	for k := range tally {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, tally[k])
	}
	return strings.Join(parts, ", ")
}

func dumpParityRows(diffs map[string]parityDifference) string {
	var b strings.Builder
	for _, key := range sortedDiffKeys(diffs) {
		d := diffs[key]
		caseID, rest, _ := strings.Cut(key, " ")
		mode, diffKey, _ := strings.Cut(rest, " ")
		fmt.Fprintf(&b, "\t{%q, %q, %q, %q, %q, classX},\n", caseID, mode, diffKey, d.legacy, d.ashlar)
	}
	return b.String()
}
