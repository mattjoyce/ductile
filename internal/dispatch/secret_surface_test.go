package dispatch

import (
	"testing"

	"github.com/mattjoyce/ductile/internal/config"
)

func TestSecretSurfacePaths(t *testing.T) {
	t.Run("includes the config directory and the state db", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.State.Path = "/var/lib/ductile/ductile.db"
		got := SecretSurfacePaths(cfg, "/etc/ductile")
		want := map[string]bool{"/etc/ductile": true, "/var/lib/ductile/ductile.db": true}
		if len(got) != len(want) {
			t.Fatalf("got %v, want the config dir + state db", got)
		}
		for _, p := range got {
			if !want[p] {
				t.Fatalf("unexpected path %q in surface %v", p, got)
			}
		}
	})

	t.Run("the config directory is the single secrets home (closes file-form gap)", func(t *testing.T) {
		// Reconciling the directory (not a single config file) is what covers sibling
		// secrets — tokens, the vault blob, .checksums — in the same dir.
		cfg := &config.Config{}
		cfg.State.Path = "/s/db"
		got := SecretSurfacePaths(cfg, "/etc/ductile")
		found := false
		for _, p := range got {
			if p == "/etc/ductile" {
				found = true
			}
		}
		if !found {
			t.Fatalf("config directory missing from surface %v", got)
		}
	})

	t.Run("empty inputs contribute nothing (no empty paths to reconcile)", func(t *testing.T) {
		got := SecretSurfacePaths(&config.Config{}, "")
		if len(got) != 0 {
			t.Fatalf("expected no paths, got %v", got)
		}
		if got := SecretSurfacePaths(nil, ""); len(got) != 0 {
			t.Fatalf("nil cfg + empty dir should yield no paths, got %v", got)
		}
	})
}
