package vault

import (
	"fmt"
	"regexp"
	"sort"
)

// principalNameRE enforces kebab-case principal names (ADR §3.2: registered,
// unique kebab-case names).
var principalNameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func validKind(kind string) bool {
	switch kind {
	case KindPlugin, KindConsumer, KindGateway:
		return true
	default:
		return false
	}
}

func validStatus(status string) bool {
	return status == StatusActive || status == StatusRevoked
}

// RegisterPrincipal adds a new principal (status active). It is a pure model
// mutation — persistence is the caller's separate Save. Duplicate names, bad
// kinds, and non-kebab names are typed errors, not silent overwrites.
//
// Fingerprint binding in Rung 1 is by *name*: the gateway verifies a plugin's
// identity against the existing `.checksums` (plain-fingerprint) machinery at
// Compose/spawn time; the vault stores no fingerprint here. Keyed-nonce binding
// is the Rung 4 upgrade (Attestation ADR).
func (s *Store) RegisterPrincipal(name, kind string) error {
	if isReservedPrincipal(name) {
		// Defense-in-depth + parity with the lifecycle mutators (RevokePrincipal /
		// PurgePrincipal), which already refuse reserved names. `core` is seeded at
		// genesis and is never re-registered through the data plane.
		return fmt.Errorf("%w: principal %q", ErrReservedEntity, name)
	}
	if !principalNameRE.MatchString(name) {
		return fmt.Errorf("%w: principal %q must be kebab-case", ErrInvalidName, name)
	}
	if !validKind(kind) {
		return fmt.Errorf("%w: %q", ErrInvalidKind, kind)
	}
	if _, exists := s.Principals[name]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicatePrincipal, name)
	}
	s.Principals[name] = &Principal{Kind: kind, Status: StatusActive}
	return nil
}

// Principal returns a principal by name.
func (s *Store) Principal(name string) (*Principal, bool) {
	p, ok := s.Principals[name]
	return p, ok
}

// PrincipalNames returns the registered principal names, sorted.
func (s *Store) PrincipalNames() []string {
	names := make([]string, 0, len(s.Principals))
	for n := range s.Principals {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
