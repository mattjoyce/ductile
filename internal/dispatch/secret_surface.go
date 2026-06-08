package dispatch

import "github.com/mattjoyce/ductile/internal/config"

// SecretSurfacePaths is the single source of truth for "what must be gateway-owned
// and unreadable by an account uid" at the privsep filesystem floor (#87; luminary
// review Hickey A4 / Armstrong B5). Previously this set was an inline slice at the
// reconcile call site, which (a) could drift and (b) — if the config path is a single
// FILE — left sibling secret files in the same directory (tokens, the age-encrypted
// vault blob, .checksums) un-reconciled. Naming it here and reconciling the config
// DIRECTORY closes both: tightening the directory to 0700 gateway-owned covers every
// secret-bearing file inside it.
//
// The returned paths are reconciled by reconcileSecretPath, which tightens a
// gateway-owned path in place, skips a non-existent one, and FAILS CLOSED on anything
// foreign-owned — so adding a path here can only ever add protection, never widen it.
func SecretSurfacePaths(cfg *config.Config, configDir string) []string {
	paths := make([]string, 0, 2)
	if configDir != "" {
		paths = append(paths, configDir)
	}
	if cfg != nil && cfg.State.Path != "" {
		paths = append(paths, cfg.State.Path)
	}
	return paths
}
