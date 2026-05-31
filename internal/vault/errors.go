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
	ErrUnknownSecret      = errors.New("vault: unknown secret")
	ErrSecretRevoked      = errors.New("vault: secret is revoked")
)
