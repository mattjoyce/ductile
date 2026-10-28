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

// TestPolicyMargin decides every token against every capability-table row once
// and records the costliest decision against the engine's bounds. The sweep is
// tiny by construction (a handful of tokens, ~20 capabilities); the log line is
// the measured number, not a guess.
func TestPolicyMargin(t *testing.T) {
	pol, err := NewPolicy([]TokenConfig{
		{Name: "admin", Scopes: []string{"*"}},
		{Name: "status", Scopes: []string{"jobs:status:ro"}},
	})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	m, err := pol.Margin(context.Background())
	if err != nil {
		t.Fatalf("Margin: %v", err)
	}
	if want := 2 * len(grants); m.Decisions != want {
		t.Errorf("Decisions = %d, want %d (2 principals x %d capability rows)", m.Decisions, want, len(grants))
	}
	if m.Steps <= 0 {
		t.Errorf("Steps = %d, want > 0", m.Steps)
	}
	if m.Bytes <= 0 {
		t.Errorf("Bytes = %d, want > 0", m.Bytes)
	}
	bounds := pol.p.Bounds()
	if m.MaxSteps != bounds.MaxSteps || m.MaxBytes != bounds.MaxBytes {
		t.Errorf("bounds = %d/%d, want the Shearline bounds %d/%d",
			m.MaxSteps, m.MaxBytes, bounds.MaxSteps, bounds.MaxBytes)
	}
	if err := m.Check(); err != nil {
		t.Errorf("Check() = %v, want nil", err)
	}
	if got := m.String(); !strings.HasPrefix(got, "token policy: worst decision") {
		t.Errorf("String() = %q, want prefix %q", got, "token policy: worst decision")
	}
	t.Logf("margin: %s", m.String())
}

// TestMarginCheckRefusesPastLimit pins the refusal boundary: strictly past half
// of either bound is refused, exactly half is not.
func TestMarginCheckRefusesPastLimit(t *testing.T) {
	cases := []struct {
		name string
		m    Margin
		want error
	}{
		{"steps past half", Margin{Decisions: 1, Steps: 60, MaxSteps: 100, Bytes: 1, MaxBytes: 100}, ErrPolicyMargin},
		{"bytes past half", Margin{Decisions: 1, Steps: 1, MaxSteps: 100, Bytes: 60, MaxBytes: 100}, ErrPolicyMargin},
		{"exactly half", Margin{Decisions: 1, Steps: 50, MaxSteps: 100, Bytes: 50, MaxBytes: 100}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.m.Check()
			if c.want == nil {
				if err != nil {
					t.Fatalf("Check() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("Check() = %v, want errors.Is(_, %v)", err, c.want)
			}
		})
	}
}

// TestPolicyMarginNoTokens covers the empty policy: nothing to decide is not an
// error, and a zero Policy (never loaded) is ErrNoPolicy.
func TestPolicyMarginNoTokens(t *testing.T) {
	pol, err := NewPolicy(nil)
	if err != nil {
		t.Fatalf("NewPolicy(nil): %v", err)
	}
	m, err := pol.Margin(context.Background())
	if err != nil {
		t.Fatalf("Margin: %v", err)
	}
	if m.Decisions != 0 {
		t.Errorf("Decisions = %d, want 0", m.Decisions)
	}
	if err := m.Check(); err != nil {
		t.Errorf("Check() = %v, want nil", err)
	}
	if got, want := m.String(), "token policy: no tokens, nothing to decide"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	var zero Policy
	if _, err := zero.Margin(context.Background()); !errors.Is(err, ErrNoPolicy) {
		t.Errorf("zero Policy Margin err = %v, want ErrNoPolicy", err)
	}
}

// TestDecideReportsCost holds Decide to carrying the engine's cost on both an
// allow and a deny: the margin is only meaningful if every answer is measured.
func TestDecideReportsCost(t *testing.T) {
	pol, err := NewPolicy([]TokenConfig{{Name: "status", Scopes: []string{"jobs:status:ro"}}})
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	ctx := context.Background()
	allowed, err := pol.Decide(ctx, Principal{Name: "status"}, ActionRead, CapJobsStatus)
	if err != nil {
		t.Fatalf("Decide(allow): %v", err)
	}
	if !allowed.Allow {
		t.Fatalf("status on jobs/status must be allowed\n%s", allowed.Why)
	}
	denied, err := pol.Decide(ctx, Principal{Name: "status"}, ActionWrite, CapSystemReload)
	if err != nil {
		t.Fatalf("Decide(deny): %v", err)
	}
	if denied.Allow {
		t.Fatal("status on system/reload write must be denied")
	}
	if allowed.Steps <= 0 || denied.Steps <= 0 {
		t.Errorf("both decisions must report Steps > 0: allow=%d deny=%d", allowed.Steps, denied.Steps)
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
