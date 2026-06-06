package dispatch

import (
	"errors"
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

func TestResolveWorker(t *testing.T) {
	cfg := &config.Config{
		Workers: map[string]config.WorkerConf{
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/workers/untrusted"},
		},
		Plugins: map[string]config.PluginConf{
			"sys_exec": {Worker: "untrusted"},
			"fetch":    {},                // no grant
			"broken":   {Worker: "ghost"}, // grants a worker that doesn't exist
		},
	}

	t.Run("granted plugin resolves to its worker (confined)", func(t *testing.T) {
		got, err := resolveWorker(cfg, "sys_exec")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got.Confined {
			t.Fatal("expected Confined=true for a granted plugin")
		}
		want := ResolvedWorker{Name: "untrusted", UID: 1002, GID: 1002, StateDir: "/app/data/workers/untrusted", Confined: true, Source: WorkerGranted}
		if got != want {
			t.Fatalf("resolved worker mismatch:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("ungranted plugin with no default tier is unconfined", func(t *testing.T) {
		// cfg defines only `untrusted`, no `default` — so an ungranted plugin has no
		// tier to fall back to and runs unconfined (the boot gate decides if that's allowed).
		got, err := resolveWorker(cfg, "fetch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Confined || got.Source != WorkerUnconfined {
			t.Fatalf("expected unconfined for an ungranted plugin with no default tier, got %+v", got)
		}
	})

	t.Run("unknown plugin with no default tier is unconfined", func(t *testing.T) {
		got, err := resolveWorker(cfg, "does-not-exist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Confined || got.Source != WorkerUnconfined {
			t.Fatalf("expected unconfined for an unknown plugin, got %+v", got)
		}
	})

	t.Run("grant to an undefined worker fails closed", func(t *testing.T) {
		_, err := resolveWorker(cfg, "broken")
		if !errors.Is(err, ErrWorkerGrantUndefined) {
			t.Fatalf("expected ErrWorkerGrantUndefined, got %v", err)
		}
	})

	t.Run("nil config is unconfined", func(t *testing.T) {
		got, err := resolveWorker(nil, "sys_exec")
		if err != nil || got.Confined {
			t.Fatalf("nil config must be unconfined with no error, got %+v err=%v", got, err)
		}
	})
}

// TestResolveWorkerDefaultTier covers the #85 Q2 switch: when a `default` tier is
// configured, an ungranted plugin falls back to it (confined), while an explicit
// grant still wins.
func TestResolveWorkerDefaultTier(t *testing.T) {
	cfg := &config.Config{
		Workers: map[string]config.WorkerConf{
			"default":   {UID: 1001, GID: 1001, StateDir: "/app/data/workers/default"},
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/workers/untrusted"},
		},
		Plugins: map[string]config.PluginConf{
			"sys_exec": {Worker: "untrusted"},
			"fetch":    {}, // no grant
		},
	}

	t.Run("ungranted plugin falls back to the shared default tier (confined)", func(t *testing.T) {
		got, err := resolveWorker(cfg, "fetch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := ResolvedWorker{Name: "default", UID: 1001, GID: 1001, StateDir: "/app/data/workers/default", Confined: true, Source: WorkerDefaultTier}
		if got != want {
			t.Fatalf("ungranted plugin should resolve to the default tier:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("an unknown plugin also falls back to default when the tier exists", func(t *testing.T) {
		got, err := resolveWorker(cfg, "never-heard-of-it")
		if err != nil || got.Source != WorkerDefaultTier {
			t.Fatalf("expected default tier for unknown plugin, got %+v err=%v", got, err)
		}
	})

	t.Run("an explicit grant wins over the default fallback", func(t *testing.T) {
		got, err := resolveWorker(cfg, "sys_exec")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Name != "untrusted" || got.Source != WorkerGranted {
			t.Fatalf("explicit grant must win over default, got %+v", got)
		}
	})
}
