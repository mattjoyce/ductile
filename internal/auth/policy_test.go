package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
)

// oldNormalizeScopes is the scope map the policy replaced (auth.go before card
// 145), kept verbatim as the oracle: the implications it hard-coded, in the
// order it applied them.
func oldNormalizeScopes(scopes []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range scopes {
		if s = strings.TrimSpace(s); s != "" {
			out[s] = true
		}
	}
	if out["plugin:rw"] {
		out["plugin:ro"], out["plugin:catalog:ro"], out["plugin:invoke:ro"] = true, true, true
	}
	if out["plugin:ro"] {
		out["plugin:catalog:ro"] = true
	}
	if out["jobs:rw"] {
		out["jobs:ro"] = true
	}
	if out["jobs:ro"] {
		out["jobs:status:ro"], out["jobs:result:ro"], out["jobs:logs:ro"], out["jobs:tree:ro"] = true, true, true, true
	}
	if out["events:rw"] {
		out["events:ro"] = true
	}
	if out["events:ro"] {
		out["events:meta:ro"], out["events:payload:ro"] = true, true
	}
	return out
}

// oldHasAnyScope is the old gate: the wildcard passes everything, else any one
// of the required scopes.
func oldHasAnyScope(held map[string]bool, required []string) bool {
	if held["*"] {
		return true
	}
	for _, s := range required {
		if held[s] {
			return true
		}
	}
	return false
}

// oldRoutes is the route table as server.go and the three in-handler checks
// wrote it before card 145: the action and capability each now asks for, and
// the scope list it used to accept. The parity test holds the policy to it.
var oldRoutes = []struct {
	action, capability string
	required           []string
}{
	{ActionRead, CapPluginCatalog, []string{"plugin:catalog:ro", "plugin:rw", "*"}},
	{ActionRead, CapPluginInvoke, []string{"plugin:invoke:ro", "plugin:rw", "*"}},
	{ActionWrite, CapPluginInvoke, []string{"plugin:rw", "*"}},
	{ActionWrite, CapPipelineTrigger, []string{"plugin:rw", "*"}},
	{ActionRead, CapSystemTopology, []string{"system:ro", "system:rw", "*"}},
	{ActionRead, CapSystemConfig, []string{"system:ro", "system:rw", "*"}},
	{ActionRead, CapSystemDoctor, []string{"system:ro", "system:rw", "*"}},
	{ActionRead, CapSystemSelfcheck, []string{"system:ro", "system:rw", "*"}},
	{ActionWrite, CapSystemReload, []string{"system:rw", "*"}},
	{ActionRead, CapJobsStatus, []string{"jobs:status:ro", "jobs:rw", "*"}},
	{ActionRead, CapJobsStatus, []string{"jobs:status:ro", "*"}}, // /analytics/*
	{ActionRead, CapJobsTree, []string{"jobs:tree:ro", "jobs:rw", "*"}},
	{ActionRead, CapJobsLogs, []string{"jobs:logs:ro", "jobs:rw", "*"}},
	{ActionRead, CapJobsResult, []string{"jobs:result:ro", "jobs:rw", "*"}},
	{ActionRead, CapEventsStream, []string{"events:ro", "events:rw", "*"}},
}

// TestPolicy_ParityWithScopeMap holds the policy to the old semantics: for
// every single scope and every pair of scopes, for every route, the two agree.
func TestPolicy_ParityWithScopeMap(t *testing.T) {
	scopes := KnownScopes()
	var sets [][]string
	for i, a := range scopes {
		sets = append(sets, []string{a})
		for _, b := range scopes[i+1:] {
			sets = append(sets, []string{a, b})
		}
	}
	sets = append(sets, nil) // a token with no scopes reaches nothing
	ctx := context.Background()
	for _, held := range sets {
		name := "tok:" + strings.Join(held, "+")
		pol, err := NewPolicy([]TokenConfig{{Name: name, Token: "t", Scopes: held}})
		if err != nil {
			t.Fatalf("NewPolicy(%v): %v", held, err)
		}
		want := oldNormalizeScopes(held)
		for _, r := range oldRoutes {
			d, err := pol.Decide(ctx, Principal{Name: name}, r.action, r.capability)
			if err != nil {
				t.Fatalf("Decide(%s, %s %s): %v", name, r.action, r.capability, err)
			}
			if got, exp := d.Allow, oldHasAnyScope(want, r.required); got != exp {
				t.Errorf("%s on %s %s: policy %v, scope map %v\n%s", name, r.action, r.capability, got, exp, d.Why)
			}
		}
	}
}

// TestPolicy_Matrix pins the documented tokens against the cells an operator
// checks by hand, so a change to the lattice or the table says which cell moved.
func TestPolicy_Matrix(t *testing.T) {
	pol, err := NewPolicy([]TokenConfig{
		{Name: "admin", Token: "a", Scopes: []string{"*"}},
		{Name: "readonly", Token: "r", Scopes: []string{"plugin:ro", "jobs:ro", "events:ro"}},
		{Name: "operator", Token: "o", Scopes: []string{"plugin:rw", "jobs:rw", "events:ro"}},
		{Name: "status", Token: "s", Scopes: []string{"jobs:status:ro"}},
		{Name: "invoker", Token: "i", Scopes: []string{"plugin:invoke:ro"}},
		{Name: "none", Token: "n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		principal, action, capability string
		allow                         bool
	}{
		{"admin", ActionWrite, CapSystemReload, true},
		{"operator", ActionWrite, CapPipelineTrigger, true},
		{"operator", ActionWrite, CapSystemReload, false},
		{"operator", ActionRead, CapSystemConfig, false},
		{"readonly", ActionRead, CapPluginCatalog, true},
		{"readonly", ActionRead, CapPluginInvoke, false}, // P2-07: plugin:ro is catalog only
		{"readonly", ActionRead, CapJobsResult, true},
		{"status", ActionRead, CapJobsStatus, true},
		{"status", ActionRead, CapJobsResult, false}, // D1: the field class is its own decision
		{"invoker", ActionRead, CapPluginInvoke, true},
		{"invoker", ActionWrite, CapPluginInvoke, false},
		{"invoker", ActionRead, CapPluginCatalog, false},
		{"none", ActionRead, CapJobsStatus, false},
		{"nobody", ActionRead, CapJobsStatus, false},
	}
	ctx := context.Background()
	for _, c := range cases {
		d, err := pol.Decide(ctx, Principal{Name: c.principal}, c.action, c.capability)
		if err != nil {
			t.Fatalf("Decide(%s %s %s): %v", c.principal, c.action, c.capability, err)
		}
		if d.Allow != c.allow {
			t.Errorf("%s %s %s: allow %v, want %v\n%s", c.principal, c.action, c.capability, d.Allow, c.allow, d.Why)
		}
		if !d.Allow && d.Why.Relation == "" {
			t.Errorf("%s %s %s: a denial must carry why", c.principal, c.action, c.capability)
		}
	}
}

func TestNewPolicy_RefusesUnknownScope(t *testing.T) {
	_, err := NewPolicy([]TokenConfig{{Token: "t", Scopes: []string{"jobs:rww"}}})
	if !errors.Is(err, ErrUnknownScope) {
		t.Fatalf("a scope no route grants must be refused with ErrUnknownScope, got %v", err)
	}
	if !strings.Contains(err.Error(), "tokens[0]") || !strings.Contains(err.Error(), "jobs:rww") {
		t.Errorf("the refusal must name the token and the scope: %v", err)
	}
}

func TestNewPolicy_RefusesSharedPrincipalName(t *testing.T) {
	_, err := NewPolicy([]TokenConfig{
		{Name: "api", Token: "t1", Scopes: []string{"jobs:ro"}},
		{Name: "api", Token: "t2", Scopes: []string{"*"}},
	})
	if err == nil {
		t.Fatal("two tokens sharing a principal name would merge their roles; must be refused")
	}
}

func TestNewPolicy_IgnoresBlankScopes(t *testing.T) {
	pol, err := NewPolicy([]TokenConfig{{Token: "t", Scopes: []string{" jobs:ro ", "", "  "}}})
	if err != nil {
		t.Fatal(err)
	}
	d, err := pol.Decide(context.Background(), Principal{Name: "tokens[0]"}, ActionRead, CapJobsStatus)
	if err != nil || !d.Allow {
		t.Fatalf("trimmed scope must grant: allow=%v err=%v", d.Allow, err)
	}
}

func TestPolicy_ZeroDeniesEverything(t *testing.T) {
	var pol Policy
	d, err := pol.Decide(context.Background(), Principal{Name: "admin"}, ActionRead, CapJobsStatus)
	if !errors.Is(err, ErrNoPolicy) || d.Allow {
		t.Fatalf("zero policy must deny with ErrNoPolicy: allow=%v err=%v", d.Allow, err)
	}
}

func TestKnownScopes_CoverTable(t *testing.T) {
	for _, g := range grants {
		if !KnownScope(g.scope) {
			t.Errorf("grant scope %q not known", g.scope)
		}
	}
	for _, im := range inherits {
		if !KnownScope(im[0]) || !KnownScope(im[1]) {
			t.Errorf("inherits row %v not known", im)
		}
	}
	if KnownScope("admin") || KnownScope("") {
		t.Error("admin and the empty string are not scopes")
	}
	got := KnownScopes()
	if !sort.StringsAreSorted(got) {
		t.Errorf("KnownScopes() must be sorted: %v", got)
	}
}

// The reserved set is derived, not listed: a scope is reserved when the lattice
// names it and no grant is reachable from it. Today that is exactly the two
// events payload-shaping scopes, each inherited from events:ro.
func TestReservedScope(t *testing.T) {
	var got []string
	for _, s := range KnownScopes() {
		parent, reserved := ReservedScope(s)
		if !reserved {
			continue
		}
		got = append(got, s)
		if parent != "events:ro" {
			t.Errorf("ReservedScope(%q) parent = %q, want events:ro", s, parent)
		}
	}
	want := []string{"events:meta:ro", "events:payload:ro"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("reserved scopes = %v, want %v", got, want)
	}
	for _, s := range []string{"*", "events:ro", "jobs:status:ro", "admin", ""} {
		if _, reserved := ReservedScope(s); reserved {
			t.Errorf("ReservedScope(%q) = reserved, want not", s)
		}
	}
}
