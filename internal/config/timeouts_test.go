package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestTimeoutsConfigOverridesYAMLInline asserts that operator-defined per-command
// timeouts under plugins.<name>.timeouts.<custom> are captured in TimeoutsConfig.
// Overrides via the yaml:",inline" tag, while the four core lifecycle fields
// (poll/handle/health/init) continue to bind to their named struct fields. P2-05.
func TestTimeoutsConfigOverridesYAMLInline(t *testing.T) {
	in := []byte(`
poll: 10s
handle: 30s
health: 5s
init: 20s
cpu: 15s
io: 5s
`)
	var got TimeoutsConfig
	if err := yaml.Unmarshal(in, &got); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	if got.Poll != 10*time.Second {
		t.Errorf("Poll = %v, want 10s", got.Poll)
	}
	if got.Handle != 30*time.Second {
		t.Errorf("Handle = %v, want 30s", got.Handle)
	}
	if got.Health != 5*time.Second {
		t.Errorf("Health = %v, want 5s", got.Health)
	}
	if got.Init != 20*time.Second {
		t.Errorf("Init = %v, want 20s", got.Init)
	}
	if got.Overrides["cpu"] != 15*time.Second {
		t.Errorf("Overrides[cpu] = %v, want 15s", got.Overrides["cpu"])
	}
	if got.Overrides["io"] != 5*time.Second {
		t.Errorf("Overrides[io] = %v, want 5s", got.Overrides["io"])
	}
	if _, ok := got.Overrides["poll"]; ok {
		t.Errorf("Overrides leaked the poll key — must bind to fixed Poll field instead")
	}
}
