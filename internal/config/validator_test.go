package config

import (
	"testing"
)

func TestConfigValidator_ValidateRoutes(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "Valid routes",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
					"p2": {Enabled: true},
				},
				Routes: []RouteConfig{
					{From: "p1", To: "p2"},
				},
			},
			wantErr: false,
		},
		{
			name: "Missing from plugin",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p2": {Enabled: true},
				},
				Routes: []RouteConfig{
					{From: "p1", To: "p2"},
				},
			},
			wantErr: true,
			errMsg:  "route[0]: 'from' plugin \"p1\" does not exist",
		},
		{
			name: "Missing to plugin",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Routes: []RouteConfig{
					{From: "p1", To: "p2"},
				},
			},
			wantErr: true,
			errMsg:  "route[0]: 'to' plugin \"p2\" does not exist",
		},
		{
			name: "Empty routes",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Routes: []RouteConfig{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &ConfigValidator{config: tt.config}
			err := v.validateRoutes()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRoutes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("validateRoutes() error = %q, wantErrMsg %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestConfigValidator_ValidateWebhooks(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		tokens  map[string]string
		wantErr bool
		errMsg  string
	}{
		{
			name: "Valid webhook with secret_ref",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SecretRef: "ref0", SignatureHeader: "X-Sig"},
					},
				},
			},
			tokens:  map[string]string{"ref0": "s1"},
			wantErr: false,
		},
		{
			name: "Valid webhook with secret_ref",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SecretRef: "ref1", SignatureHeader: "X-Sig"},
					},
				},
			},
			tokens:  map[string]string{"ref1": "secret1"},
			wantErr: false,
		},
		{
			name: "Missing plugin",
			config: &Config{
				Plugins: map[string]PluginConf{},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SecretRef: "ref1", SignatureHeader: "X-Sig"},
					},
				},
			},
			wantErr: true,
			errMsg:  "webhook[0] (/w1): plugin \"p1\" does not exist",
		},
		{
			name: "Missing secret_ref",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SignatureHeader: "X-Sig"},
					},
				},
			},
			wantErr: true,
			errMsg:  "webhook[0] (/w1): secret_ref is required",
		},
		{
			name: "Missing secret_ref in tokens",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SecretRef: "ref1", SignatureHeader: "X-Sig"},
					},
				},
			},
			tokens:  map[string]string{},
			wantErr: true,
			errMsg:  "webhook[0] (/w1): secret_ref \"ref1\" not found in the vault",
		},
		{
			name: "Missing signature_header",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true},
				},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SecretRef: "ref1"},
					},
				},
			},
			tokens:  map[string]string{"ref1": "s1"},
			wantErr: true,
			errMsg:  "webhook[0] (/w1): signature_header is required",
		},
		{
			name: "Nil webhooks",
			config: &Config{
				Webhooks: nil,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &ConfigValidator{config: tt.config, tokens: tt.tokens}
			err := v.validateWebhooks()
			if (err != nil) != tt.wantErr {
				t.Errorf("validateWebhooks() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("validateWebhooks() error = %q, wantErrMsg %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestConfigValidator_ValidatePluginTokenRefs(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		tokens  map[string]string
		wantErr bool
		errMsg  string
	}{
		{
			name: "Valid _ref",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {
						Enabled: true,
						Config: map[string]any{
							"api_key_ref": "ref1",
						},
					},
				},
			},
			tokens:  map[string]string{"ref1": "secret1"},
			wantErr: false,
		},
		{
			name: "_ref is not a string",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {
						Enabled: true,
						Config: map[string]any{
							"api_key_ref": 123,
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "plugin \"p1\": config field \"api_key_ref\" must be a string",
		},
		{
			name: "_ref token not found",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {
						Enabled: true,
						Config: map[string]any{
							"api_key_ref": "ref1",
						},
					},
				},
			},
			tokens:  map[string]string{},
			wantErr: true,
			errMsg:  "plugin \"p1\" config field \"api_key_ref\": secret_ref \"ref1\" not found in the vault",
		},
		{
			name: "Non-_ref field",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {
						Enabled: true,
						Config: map[string]any{
							"other_field": "some_value",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Nil config",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {
						Enabled: true,
						Config:  nil,
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &ConfigValidator{config: tt.config, tokens: tt.tokens}
			err := v.validatePluginTokenRefs()
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePluginTokenRefs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("validatePluginTokenRefs() error = %q, wantErrMsg %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestConfigValidator_ValidateCrossReferences(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		tokens  map[string]string
		wantErr bool
	}{
		{
			name: "All valid",
			config: &Config{
				Plugins: map[string]PluginConf{
					"p1": {Enabled: true, Config: map[string]any{"key_ref": "ref1"}},
					"p2": {Enabled: true},
				},
				RelayInstances: []RelayInstanceConfig{
					{Name: "lab", SecretRef: "ref1"},
				},
				RemoteIngress: &RemoteIngressConfig{
					TrustedPeers: []RelayPeerConfig{
						{Name: "home-primary", SecretRef: "ref1"},
					},
				},
				Routes: []RouteConfig{
					{From: "p1", To: "p2"},
				},
				Webhooks: &WebhooksConfig{
					Endpoints: []WebhookEndpoint{
						{Path: "/w1", Plugin: "p1", SecretRef: "ref1", SignatureHeader: "X-Sig"},
					},
				},
			},
			tokens:  map[string]string{"ref1": "secret1"},
			wantErr: false,
		},
		{
			name: "Invalid route",
			config: &Config{
				Plugins: map[string]PluginConf{"p1": {Enabled: true}},
				Routes:  []RouteConfig{{From: "p1", To: "invalid"}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &ConfigValidator{config: tt.config, tokens: tt.tokens}
			err := v.ValidateCrossReferences()
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCrossReferences() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRelayEventTypePatternAcceptsUnderscoredSegments(t *testing.T) {
	cases := []string{
		"withings.measurement_recorded",
		"agent_handshake.registered",
		"gmail.new_message",
		"birdnet.firstday_species",
		"backup.ready",
		"withings.measurement-recorded",
	}
	for _, eventType := range cases {
		if !relayEventTypePattern.MatchString(eventType) {
			t.Errorf("relayEventTypePattern rejected %q; expected match", eventType)
		}
	}
	for _, bad := range []string{"", "Foo.bar", "foo.BAR", "foo..bar", "_foo", "foo bar"} {
		if relayEventTypePattern.MatchString(bad) {
			t.Errorf("relayEventTypePattern accepted %q; expected reject", bad)
		}
	}
}

func TestConfigValidator_ValidateRelay(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		tokens  map[string]string
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid outbound and inbound refs",
			config: &Config{
				RelayInstances: []RelayInstanceConfig{{Name: "lab", SecretRef: "relay-lab-v1"}},
				RemoteIngress: &RemoteIngressConfig{
					TrustedPeers: []RelayPeerConfig{{Name: "home-primary", SecretRef: "relay-home-primary-v1"}},
				},
			},
			tokens: map[string]string{
				"relay-lab-v1":          "secret-a",
				"relay-home-primary-v1": "secret-b",
			},
		},
		{
			name: "missing outbound token",
			config: &Config{
				RelayInstances: []RelayInstanceConfig{{Name: "lab", SecretRef: "relay-lab-v1"}},
			},
			tokens:  map[string]string{},
			wantErr: true,
			errMsg:  `instances[0] (lab): secret_ref "relay-lab-v1" not found in the vault`,
		},
		{
			name: "missing inbound token",
			config: &Config{
				RemoteIngress: &RemoteIngressConfig{
					TrustedPeers: []RelayPeerConfig{{Name: "home-primary", SecretRef: "relay-home-primary-v1"}},
				},
			},
			tokens:  map[string]string{},
			wantErr: true,
			errMsg:  `remote_ingress.peers[0] (home-primary): secret_ref "relay-home-primary-v1" not found in the vault`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := &ConfigValidator{config: tt.config, tokens: tt.tokens}
			err := v.validateRelay()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateRelay() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Fatalf("validateRelay() error = %q, want %q", err.Error(), tt.errMsg)
			}
		})
	}
}
