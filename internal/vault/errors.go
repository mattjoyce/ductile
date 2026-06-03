package vault

import "errors"

// Sentinel errors for the model operations. Named so callers (and tests) can
// branch with errors.Is rather than string-matching — a failure is a typed
// signal, not prose.
var (
	ErrInvalidName        = errors.New("vault: invalid name")
	ErrInvalidKind        = errors.New("vault: invalid principal kind")
	ErrInvalidStatus      = errors.New("vault: invalid status")
	ErrInvalidPattern     = errors.New("vault: invalid secret pattern")
	ErrDuplicatePrincipal = errors.New("vault: principal already registered")
	ErrUnknownPrincipal   = errors.New("vault: unknown principal")
	ErrPrincipalInactive  = errors.New("vault: principal is not active")
	ErrUnknownSecret      = errors.New("vault: unknown secret")
	ErrSecretRevoked      = errors.New("vault: secret is revoked")
	ErrReservedEntity     = errors.New("vault: reserved entity cannot be mutated via the data plane")

	// ErrVaultModifiedExternally signals that the on-disk blob changed underneath
	// the owner since it last wrote or loaded it — an out-of-band writer the
	// sole-writer guarantee does not cover. Save fails loud with this rather than
	// silently clobbering the other writer's bytes.
	ErrVaultModifiedExternally = errors.New("vault: blob was modified externally")
)
