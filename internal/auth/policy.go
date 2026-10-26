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

// implies is the scope lattice: a scope and one scope it implies. A scope is a
// role; the wildcard is a role like any other, one that inherits the four
// read-write scopes. The rows carry the decisions the scopes were split under:
// P2-07 made plugin:ro catalog-only, so read-class invocation needs
// plugin:invoke:ro explicitly; D1 made jobs:ro and events:ro back-compat
// super-scopes over their narrower :ro scopes.
var implies = [][2]string{
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
// an action on a capability. Wider scopes reach it through implies, so each
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
	for _, im := range implies {
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
}

// Decision is one answer. Why is the engine's account of a denial: the rule
// it walked, the literal it stopped at and the fact it did not find. It is
// zero when allowed and when the decision errored.
type Decision struct {
	Allow bool
	Why   tarski.Absence
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
	return Policy{p: p, loaded: true}, nil
}

func standingFacts(tokens []TokenConfig) ([]tarski.Fact, error) {
	out := make([]tarski.Fact, 0, len(implies)+len(grants)+len(tokens))
	for _, im := range implies {
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
	return Decision{Allow: d.Allow, Why: d.Why}, nil
}
