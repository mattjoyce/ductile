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
		want := ResolvedWorker{Name: "untrusted", UID: 1002, GID: 1002, StateDir: "/app/data/workers/untrusted", Confined: true}
		if got != want {
			t.Fatalf("resolved worker mismatch:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("ungranted plugin is unconfined (no drop)", func(t *testing.T) {
		got, err := resolveWorker(cfg, "fetch")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Confined {
			t.Fatalf("expected unconfined for an ungranted plugin, got %+v", got)
		}
	})

	t.Run("unknown plugin is unconfined (no drop)", func(t *testing.T) {
		got, err := resolveWorker(cfg, "does-not-exist")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.Confined {
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
