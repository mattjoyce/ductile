package config

import "testing"

func TestDecideBootPosture(t *testing.T) {
	withToken := APIConfig{Enabled: true, Auth: APIAuthConfig{Tokens: []APIToken{{SecretRef: "core-api-token"}}}}
	noToken := APIConfig{Enabled: true}
	disabled := APIConfig{Enabled: false}

	tests := []struct {
		name          string
		cfg           *Config
		hasVaultOwner bool
		want          BootPosture
	}{
		{"nil config is closed", nil, true, PostureClosed},
		{"api disabled is closed even with vault owner", &Config{API: disabled}, true, PostureClosed},
		{"api disabled is closed even with tokens", &Config{API: withToken.disable()}, false, PostureClosed},
		// The from-scratch bootstrap case: gateway enabled, no api token yet, a
		// genesis vault owner to operate -> vault-operable / ductile-closed.
		{"enabled + zero tokens + vault owner is management-only", &Config{API: noToken}, true, PostureManagementOnly},
		// No vault owner: nothing to manage. Stay gateway; the caller's
		// RequireAPIAuth abort (runtime.go) still fails closed.
		{"enabled + zero tokens + no vault owner is gateway", &Config{API: noToken}, false, PostureGateway},
		{"enabled + tokens + vault owner is gateway", &Config{API: withToken}, true, PostureGateway},
		{"enabled + tokens + no vault owner is gateway", &Config{API: withToken}, false, PostureGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideBootPosture(tc.cfg, tc.hasVaultOwner); got != tc.want {
				t.Fatalf("DecideBootPosture() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBootPostureString(t *testing.T) {
	cases := map[BootPosture]string{
		PostureClosed:         "closed",
		PostureGateway:        "gateway",
		PostureManagementOnly: "management-only",
		BootPosture(99):       "unknown",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Errorf("BootPosture(%d).String() = %q, want %q", int(p), got, want)
		}
	}
}

// disable returns a copy of the APIConfig with Enabled cleared — a test helper so
// the "disabled even with tokens" case reuses the token-bearing fixture.
func (c APIConfig) disable() APIConfig { c.Enabled = false; return c }
