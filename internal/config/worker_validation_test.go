package config

import (
	"strings"
	"testing"
)

func TestValidateWorkers(t *testing.T) {
	valid := func() map[string]WorkerConf {
		return map[string]WorkerConf{
			"default":   {UID: 1001, GID: 1001, StateDir: "/app/data/workers/default"},
			"untrusted": {UID: 1002, GID: 1002, StateDir: "/app/data/workers/untrusted"},
		}
	}

	t.Run("two-tier default posture is valid", func(t *testing.T) {
		if err := validateWorkers(&Config{Workers: valid()}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("absent/empty map is valid (resolves via the boot gate, not here)", func(t *testing.T) {
		if err := validateWorkers(&Config{}); err != nil {
			t.Fatalf("nil map: %v", err)
		}
		if err := validateWorkers(&Config{Workers: map[string]WorkerConf{}}); err != nil {
			t.Fatalf("empty map: %v", err)
		}
	})

	t.Run("a third row loads fine — open map, not capped at two", func(t *testing.T) {
		w := valid()
		w["isolated"] = WorkerConf{UID: 1003, GID: 1003, StateDir: "/app/data/workers/isolated"}
		if err := validateWorkers(&Config{Workers: w}); err != nil {
			t.Fatalf("third worker rejected: %v", err)
		}
	})

	t.Run("duplicate uid is false isolation → rejected", func(t *testing.T) {
		w := valid()
		w["sneaky"] = WorkerConf{UID: 1001, GID: 1009, StateDir: "/app/data/workers/sneaky"}
		err := validateWorkers(&Config{Workers: w})
		if err == nil || !strings.Contains(err.Error(), "false isolation") {
			t.Fatalf("expected duplicate-uid rejection, got %v", err)
		}
	})

	cases := []struct {
		name   string
		worker WorkerConf
		want   string
	}{
		{"uid zero (root) rejected", WorkerConf{UID: 0, GID: 1001, StateDir: "/s"}, "uid must be positive"},
		{"negative uid rejected", WorkerConf{UID: -5, GID: 1001, StateDir: "/s"}, "uid must be positive"},
		{"gid zero rejected", WorkerConf{UID: 1001, GID: 0, StateDir: "/s"}, "gid must be positive"},
		{"relative state_dir rejected", WorkerConf{UID: 1001, GID: 1001, StateDir: "data/w"}, "must be an absolute path"},
		{"empty state_dir rejected", WorkerConf{UID: 1001, GID: 1001, StateDir: ""}, "must be an absolute path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkers(&Config{Workers: map[string]WorkerConf{"w": tc.worker}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}
