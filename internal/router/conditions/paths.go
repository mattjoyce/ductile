package conditions

import (
	"fmt"
	"strings"
)

// ReferencesRoot reports whether any atomic predicate in the tree reads from the
// named scope root (e.g. "context"). Used to detect predicates whose scope root
// is structurally unavailable on a given dispatch path, so an unsatisfiable
// predicate can be made observable instead of silently evaluating false.
func ReferencesRoot(cond *Condition, root string) bool {
	if cond == nil {
		return false
	}
	if trimmed := strings.TrimSpace(cond.Path); trimmed != "" {
		if before, _, _ := strings.Cut(trimmed, "."); before == root {
			return true
		}
	}
	for i := range cond.All {
		if ReferencesRoot(&cond.All[i], root) {
			return true
		}
	}
	for i := range cond.Any {
		if ReferencesRoot(&cond.Any[i], root) {
			return true
		}
	}
	if cond.Not != nil {
		return ReferencesRoot(cond.Not, root)
	}
	return false
}

// ResolvePath resolves a dotted path from the supported scope roots.
func ResolvePath(scope Scope, path string) (bool, any, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false, nil, fmt.Errorf("path is empty")
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 {
		return false, nil, fmt.Errorf("path is empty")
	}

	var current any
	switch parts[0] {
	case "payload":
		current = scope.Payload
	case "context":
		current = scope.Context
	case "config":
		current = scope.Config
	default:
		return false, nil, fmt.Errorf("unsupported path root %q", parts[0])
	}

	if len(parts) == 1 {
		return current != nil, current, nil
	}

	for _, segment := range parts[1:] {
		if strings.TrimSpace(segment) == "" {
			return false, nil, fmt.Errorf("path contains empty segment")
		}
		obj, ok := current.(map[string]any)
		if !ok {
			return false, nil, nil
		}
		next, exists := obj[segment]
		if !exists {
			return false, nil, nil
		}
		current = next
	}

	return true, current, nil
}
