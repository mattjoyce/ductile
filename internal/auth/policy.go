package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mattjoyce/shearline/policy"
	"github.com/mattjoyce/tarski"
)

// Actions a route asks the policy about. The router knows which one a request
// is before it asks: a GET reads, a POST writes, and a plugin command's class
// comes from its manifest.
const (
	ActionRead  = "read"
	ActionWrite = "write"
)

// Capabilities are what the API lets a token do, named for what a route does
// rather than where it lives: five GET routes read job status, so five routes
// share CapJobsStatus. Grants are string prefixes with no path semantics, so
// every capability ends in "/": jobs/status/ cannot cover a later
// jobs/status/history/ by accident.
const (
	CapPluginCatalog   = "plugin/catalog/"
	CapPluginInvoke    = "plugin/invoke/"
	CapPipelineTrigger = "pipeline/trigger/"
	CapSystemTopology  = "system/topology/"
	CapSystemConfig    = "system/config/"
	CapSystemDoctor    = "system/doctor/"
	CapSystemSelfcheck = "system/selfcheck/"
	CapSystemReload    = "system/reload/"
	CapJobsStatus      = "jobs/status/"
	CapJobsTree        = "jobs/tree/"
	CapJobsLogs        = "jobs/logs/"
	CapJobsResult      = "jobs/result/"
	CapEventsStream    = "events/stream/"
)

// inherits is the scope lattice: a scope and one scope it inherits. A scope is a
// role; the wildcard is a role like any other, one that inherits the four
// read-write scopes. The rows carry the decisions the scopes were split under:
// P2-07 made plugin:ro catalog-only, so read-class invocation needs
// plugin:invoke:ro explicitly; D1 made jobs:ro and events:ro back-compat
// super-scopes over their narrower :ro scopes.
var inherits = [][2]string{
	{"*", "plugin:rw"}, {"*", "jobs:rw"}, {"*", "events:rw"}, {"*", "system:rw"},
	{"plugin:rw", "plugin:ro"}, {"plugin:rw", "plugin:invoke:ro"},
	{"plugin:ro", "plugin:catalog:ro"},
	{"jobs:rw", "jobs:ro"},
	{"jobs:ro", "jobs:status:ro"}, {"jobs:ro", "jobs:result:ro"}, {"jobs:ro", "jobs:logs:ro"}, {"jobs:ro", "jobs:tree:ro"},
	{"events:rw", "events:ro"},
	{"events:ro", "events:meta:ro"}, {"events:ro", "events:payload:ro"},
	{"system:rw", "system:ro"},
}

// grant is one row of the capability table: the narrowest scope that reaches
// an action on a capability. Wider scopes reach it through inherits, so each
// capability is written once, not once per scope.
type grant struct {
	scope, action, capability string
}

// grants is the capability table, the single source for what a scope reaches.
// Routes name a capability (internal/api/server.go); this table says who gets it.
var grants = []grant{
	{"plugin:catalog:ro", ActionRead, CapPluginCatalog},
	{"plugin:invoke:ro", ActionRead, CapPluginInvoke},
	{"plugin:rw", ActionWrite, CapPluginInvoke},
	{"plugin:rw", ActionWrite, CapPipelineTrigger},
	{"system:ro", ActionRead, CapSystemTopology},
	{"system:ro", ActionRead, CapSystemConfig},
	{"system:ro", ActionRead, CapSystemDoctor},
	{"system:ro", ActionRead, CapSystemSelfcheck},
	{"system:rw", ActionWrite, CapSystemReload},
	{"jobs:status:ro", ActionRead, CapJobsStatus},
	{"jobs:tree:ro", ActionRead, CapJobsTree},
	{"jobs:logs:ro", ActionRead, CapJobsLogs},
	// Not a route: the result, log and tree fields of a job response, shaped
	// per token (D1). A field class is a capability decided once per response.
	{"jobs:result:ro", ActionRead, CapJobsResult},
	{"events:ro", ActionRead, CapEventsStream},
}

// knownScopes is every scope the lattice or the capability table names. A
// token holding any other scope is refused: in the old scope map it would
// have been a scope that silently granted nothing.
var knownScopes = func() map[string]bool {
	known := map[string]bool{}
	for _, im := range inherits {
		known[im[0]], known[im[1]] = true, true
	}
	for _, g := range grants {
		known[g.scope] = true
	}
	return known
}()

// KnownScope reports whether s is a scope the policy can give meaning to.
// Config validation uses it so `config check` refuses a typo before a restart.
func KnownScope(s string) bool { return knownScopes[strings.TrimSpace(s)] }

// KnownScopes lists every scope the policy knows, sorted, for error messages
// and documentation.
func KnownScopes() []string {
	out := make([]string, 0, len(knownScopes))
	for s := range knownScopes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// reaches is every scope that leads to at least one grant, directly or through
// the scopes it inherits.
var reaches = func() map[string]bool {
	out := map[string]bool{}
	for _, g := range grants {
		out[g.scope] = true
	}
	for changed := true; changed; {
		changed = false
		for _, im := range inherits {
			if out[im[1]] && !out[im[0]] {
				out[im[0]], changed = true, true
			}
		}
	}
	return out
}()

// ReservedScope reports whether s is a scope the lattice names but no
// capability reaches, and the scope that inherits it. Such a scope is accepted
// so existing tokens keep loading, but it grants nothing yet: events:meta:ro and
// events:payload:ro are held for per-subscriber payload shaping. `config check`
// warns on one so a token holding only it is not a silent 403 on every route.
func ReservedScope(s string) (parent string, reserved bool) {
	s = strings.TrimSpace(s)
	if !knownScopes[s] || reaches[s] {
		return "", false
	}
	for _, im := range inherits {
		if im[1] == s {
			return im[0], true
		}
	}
	return "", true
}

// ErrUnknownScope marks a token scope the capability table gives no meaning to.
var ErrUnknownScope = errors.New("auth: unknown scope")

// ErrNoPolicy is the deny a zero Policy returns: nothing was loaded, so nothing
// is allowed. It is the fail-closed state of a Server built without a policy.
var ErrNoPolicy = errors.New("auth: no policy loaded")

// Policy decides whether a principal may take an action on a capability. It is
// a value: built once from the configured tokens, safe to share between
// goroutines, and swapped whole on reload. The zero Policy denies everything.
type Policy struct {
	p      policy.Policy
	loaded bool
	// principals is each token's principalName in token order. Margin sweeps
	// them, so it is fixed at build time with the roles that name them.
	principals []string
}

// Decision is one answer. Why is the engine's account of a denial: the rule
// it walked, the literal it stopped at and the fact it did not find. It is
// zero when allowed and when the decision errored.
//
// Steps and Bytes are the evaluation cost Shearline reports: zero on error;
// for observation only, never compared across builds.
type Decision struct {
	Allow bool
	Why   tarski.Absence
	Steps int
	Bytes int
}

// NewPolicy builds the Policy for a set of tokens: the lattice, the capability
// table, and a role per token per scope. It fails closed on a scope nothing
// grants and on two tokens that would share a principal name, since their
// roles would merge.
func NewPolicy(tokens []TokenConfig) (Policy, error) {
	facts, err := standingFacts(tokens)
	if err != nil {
		return Policy{}, err
	}
	p, err := policy.Load(policy.Config{Standing: facts, Sets: []tarski.RuleSet{policy.RBAC()}})
	if err != nil {
		return Policy{}, fmt.Errorf("auth: load policy: %w", err)
	}
	principals := make([]string, len(tokens))
	for i, t := range tokens {
		principals[i] = principalName(i, t)
	}
	return Policy{p: p, loaded: true, principals: principals}, nil
}

func standingFacts(tokens []TokenConfig) ([]tarski.Fact, error) {
	out := make([]tarski.Fact, 0, len(inherits)+len(grants)+len(tokens))
	for _, im := range inherits {
		out = append(out, policy.Inherits(im[0], im[1]))
	}
	for _, g := range grants {
		out = append(out, policy.Grant(g.scope, g.action, g.capability))
	}
	seen := map[string]int{}
	for i, t := range tokens {
		name := principalName(i, t)
		if j, dup := seen[name]; dup {
			return nil, fmt.Errorf("auth: tokens[%d] and tokens[%d] share the principal name %q", j, i, name)
		}
		seen[name] = i
		for _, s := range t.Scopes {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if !knownScopes[s] {
				return nil, fmt.Errorf("%w: tokens[%d] (%s) holds %q, which no route grants; known scopes: %s",
					ErrUnknownScope, i, name, s, strings.Join(KnownScopes(), ", "))
			}
			out = append(out, policy.Role(name, s))
		}
	}
	return out, nil
}

// Loaded reports whether the policy was built; a zero Policy denies everything.
func (pol Policy) Loaded() bool { return pol.loaded }

// Decide answers whether p may take action on capability. Every error is a
// deny; the error says whether it was the request, the policy or the engine.
func (pol Policy) Decide(ctx context.Context, p Principal, action, capability string) (Decision, error) {
	if !pol.loaded {
		return Decision{}, ErrNoPolicy
	}
	d, err := pol.p.Decide(ctx, policy.Request{Principal: p.Name, Action: action, Resource: capability})
	if err != nil {
		return Decision{}, fmt.Errorf("auth: decide %s %s for %s: %w", action, capability, p.Name, err)
	}
	return Decision{Allow: d.Allow, Why: d.Why, Steps: d.Steps, Bytes: d.Bytes}, nil
}

// MarginLimit is the share of either evaluation bound a token policy may use
// at its costliest decision. A policy past it is a design problem, refused at
// load rather than discovered under load.
const MarginLimit = 0.5

// ErrPolicyMargin marks a token policy whose costliest decision uses more than
// MarginLimit of one of its evaluation bounds.
var ErrPolicyMargin = errors.New("auth: token policy past its cost margin")

// Margin is a token policy's costliest decision against its evaluation bounds.
type Margin struct {
	Decisions                     int    // decisions swept: principals x capability-table rows
	Steps, Bytes                  int    // worst of each, over every decision
	MaxSteps, MaxBytes            int    // Policy bounds
	Principal, Action, Capability string // the decision with the most steps
}

// Margin sweeps every principal against every row of the capability table
// exactly once and records the costliest decision of each kind. Any decision
// error is returned wrapped: a *TooLarge surfaces this way and is a hard
// refusal. A zero Policy returns ErrNoPolicy.
func (pol Policy) Margin(ctx context.Context) (Margin, error) {
	if !pol.loaded {
		return Margin{}, ErrNoPolicy
	}
	bounds := pol.p.Bounds()
	m := Margin{MaxSteps: bounds.MaxSteps, MaxBytes: bounds.MaxBytes}
	for _, principal := range pol.principals {
		for _, g := range grants {
			d, err := pol.Decide(ctx, Principal{Name: principal}, g.action, g.capability)
			if err != nil {
				return Margin{}, fmt.Errorf("auth: policy margin: %w", err)
			}
			m.Decisions++
			if d.Steps > m.Steps {
				m.Steps = d.Steps
				m.Principal, m.Action, m.Capability = principal, g.action, g.capability
			}
			if d.Bytes > m.Bytes {
				m.Bytes = d.Bytes
			}
		}
	}
	return m, nil
}

// String renders the margin for a log line or `config check`: the costliest
// decision of each kind against its bound, as a share and as a count.
func (m Margin) String() string {
	if m.Decisions == 0 {
		return "token policy: no tokens, nothing to decide"
	}
	return fmt.Sprintf(
		"token policy: worst decision %d/%d steps (%.1f%%), %d/%d bytes (%.1f%%) over %d decisions; costliest: %s %s for %s",
		m.Steps, m.MaxSteps, share(m.Steps, m.MaxSteps),
		m.Bytes, m.MaxBytes, share(m.Bytes, m.MaxBytes),
		m.Decisions, m.Principal, m.Action, m.Capability)
}

// share is value as a percentage of bound, or 0 when bound is 0.
func share(value, bound int) float64 {
	if bound == 0 {
		return 0
	}
	return 100 * float64(value) / float64(bound)
}

// Check reports whether either costliest decision uses more than MarginLimit of
// its bound. A nil return is a policy with room; a non-nil one is refused at
// load, naming the limit and the costliest decision.
func (m Margin) Check() error {
	if float64(m.Steps) > MarginLimit*float64(m.MaxSteps) || float64(m.Bytes) > MarginLimit*float64(m.MaxBytes) {
		return fmt.Errorf("%w (limit %.0f%% of either bound): %s", ErrPolicyMargin, MarginLimit*100, m)
	}
	return nil
}
