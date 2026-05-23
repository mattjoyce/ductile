package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestAuthenticate(t *testing.T) {
	tokens := []TokenConfig{
		{Token: "valid-token-1", Scopes: []string{"read", "write"}},
		{Token: "valid-token-2", Scopes: []string{"admin"}},
		{Token: "plugin-token", Scopes: []string{"plugin:rw"}},
	}

	tests := []struct {
		name          string
		presented     string
		tokens        []TokenConfig
		wantPrincipal Principal
		wantBool      bool
	}{
		{
			name:      "valid token 1",
			presented: "valid-token-1",
			tokens:    tokens,
			wantPrincipal: Principal{
				Token: "valid-token-1",
				Scopes: map[string]struct{}{
					"read":  {},
					"write": {},
				},
			},
			wantBool: true,
		},
		{
			name:      "valid token 2",
			presented: "valid-token-2",
			tokens:    tokens,
			wantPrincipal: Principal{
				Token: "valid-token-2",
				Scopes: map[string]struct{}{
					"admin": {},
				},
			},
			wantBool: true,
		},
		{
			name:          "invalid token",
			presented:     "invalid-token",
			tokens:        tokens,
			wantPrincipal: Principal{},
			wantBool:      false,
		},
		{
			name:          "empty presented token",
			presented:     "",
			tokens:        tokens,
			wantPrincipal: Principal{},
			wantBool:      false,
		},
		{
			name:          "empty configured tokens",
			presented:     "valid-token-1",
			tokens:        []TokenConfig{},
			wantPrincipal: Principal{},
			wantBool:      false,
		},
		{
			name:      "plugin rw scope expands to ro plus narrower P2-07 splits",
			presented: "plugin-token",
			tokens:    tokens,
			wantPrincipal: Principal{
				Token: "plugin-token",
				Scopes: map[string]struct{}{
					"plugin:rw":         {},
					"plugin:ro":         {},
					"plugin:catalog:ro": {},
					"plugin:invoke:ro":  {},
				},
			},
			wantBool: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPrincipal, gotBool := Authenticate(tt.presented, tt.tokens)
			if gotBool != tt.wantBool {
				t.Errorf("Authenticate() gotBool = %v, want %v", gotBool, tt.wantBool)
			}
			if !reflect.DeepEqual(gotPrincipal, tt.wantPrincipal) {
				t.Errorf("Authenticate() gotPrincipal = %v, want %v", gotPrincipal, tt.wantPrincipal)
			}
		})
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name            string
		authValue       string
		queryToken      string
		allowQueryToken bool
		want            string
		wantErr         bool
	}{
		{
			name:      "valid bearer token",
			authValue: "Bearer my-secret-token",
			want:      "my-secret-token",
		},
		{
			name:    "missing header",
			wantErr: true,
		},
		{
			name:      "invalid format",
			authValue: "Basic dXNlcm5hbWU6cGFzc3dvcmQ=",
			wantErr:   true,
		},
		{
			name:      "empty token after prefix",
			authValue: "Bearer ",
			wantErr:   true,
		},
		{
			name:      "whitespace token",
			authValue: "Bearer    ",
			wantErr:   true,
		},
		{
			name:            "query token allowed",
			queryToken:      "sse-token",
			allowQueryToken: true,
			want:            "sse-token",
		},
		{
			name:            "query token not allowed — rejected",
			queryToken:      "sse-token",
			allowQueryToken: false,
			wantErr:         true,
		},
		{
			name:            "header takes precedence over query token",
			authValue:       "Bearer header-token",
			queryToken:      "query-token",
			allowQueryToken: true,
			want:            "header-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/"
			if tt.queryToken != "" {
				url = "/?token=" + tt.queryToken
			}
			req, _ := http.NewRequest("GET", url, nil)
			if tt.authValue != "" {
				req.Header.Set("Authorization", tt.authValue)
			}
			got, err := ExtractBearerToken(req, tt.allowQueryToken)
			if (err != nil) != tt.wantErr {
				t.Errorf("ExtractBearerToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ExtractBearerToken() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeScopes(t *testing.T) {
	tests := []struct {
		name   string
		scopes []string
		want   map[string]struct{}
	}{
		{
			name:   "empty",
			scopes: []string{},
			want:   map[string]struct{}{},
		},
		{
			name:   "basic scopes",
			scopes: []string{"read", "write"},
			want: map[string]struct{}{
				"read":  {},
				"write": {},
			},
		},
		{
			name:   "with whitespace",
			scopes: []string{"  read  ", "write ", ""},
			want: map[string]struct{}{
				"read":  {},
				"write": {},
			},
		},
		{
			name:   "plugin rw expansion (full plugin tree)",
			scopes: []string{"plugin:rw"},
			want: map[string]struct{}{
				"plugin:rw":         {},
				"plugin:ro":         {},
				"plugin:catalog:ro": {},
				"plugin:invoke:ro":  {},
			},
		},
		{
			name:   "plugin ro is catalog-only (P2-07 split)",
			scopes: []string{"plugin:ro"},
			want: map[string]struct{}{
				"plugin:ro":         {},
				"plugin:catalog:ro": {},
			},
		},
		{
			name:   "plugin invoke ro is invoke-only",
			scopes: []string{"plugin:invoke:ro"},
			want: map[string]struct{}{
				"plugin:invoke:ro": {},
			},
		},
		{
			name:   "jobs rw expansion (full jobs tree)",
			scopes: []string{"jobs:rw"},
			want: map[string]struct{}{
				"jobs:rw":        {},
				"jobs:ro":        {},
				"jobs:status:ro": {},
				"jobs:result:ro": {},
				"jobs:logs:ro":   {},
				"jobs:tree:ro":   {},
			},
		},
		{
			name:   "jobs ro is super-scope (D1 back-compat)",
			scopes: []string{"jobs:ro"},
			want: map[string]struct{}{
				"jobs:ro":        {},
				"jobs:status:ro": {},
				"jobs:result:ro": {},
				"jobs:logs:ro":   {},
				"jobs:tree:ro":   {},
			},
		},
		{
			name:   "jobs status ro alone does not imply others (D1 narrowing)",
			scopes: []string{"jobs:status:ro"},
			want: map[string]struct{}{
				"jobs:status:ro": {},
			},
		},
		{
			name:   "events rw expansion (full events tree)",
			scopes: []string{"events:rw"},
			want: map[string]struct{}{
				"events:rw":         {},
				"events:ro":         {},
				"events:meta:ro":    {},
				"events:payload:ro": {},
			},
		},
		{
			name:   "events ro is super-scope (D1)",
			scopes: []string{"events:ro"},
			want: map[string]struct{}{
				"events:ro":         {},
				"events:meta:ro":    {},
				"events:payload:ro": {},
			},
		},
		{
			name:   "duplicate scopes",
			scopes: []string{"read", "read"},
			want: map[string]struct{}{
				"read": {},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeScopes(tt.scopes); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("normalizeScopes() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasAnyScope(t *testing.T) {
	tests := []struct {
		name     string
		scopes   map[string]struct{}
		required []string
		want     bool
	}{
		{
			name:     "no required scopes",
			scopes:   map[string]struct{}{"read": {}},
			required: []string{},
			want:     true,
		},
		{
			name:     "has required scope",
			scopes:   map[string]struct{}{"read": {}, "write": {}},
			required: []string{"write"},
			want:     true,
		},
		{
			name:     "missing required scope",
			scopes:   map[string]struct{}{"read": {}},
			required: []string{"write"},
			want:     false,
		},
		{
			name:     "wildcard scope",
			scopes:   map[string]struct{}{"*": {}},
			required: []string{"admin"},
			want:     true,
		},
		{
			name:     "has one of required scopes",
			scopes:   map[string]struct{}{"read": {}},
			required: []string{"write", "read"},
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Principal{Scopes: tt.scopes}
			if got := HasAnyScope(p, tt.required...); got != tt.want {
				t.Errorf("HasAnyScope() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	p := Principal{
		Token:  "test-token",
		Scopes: map[string]struct{}{"test": {}},
	}

	// Test missing
	_, ok := PrincipalFromContext(ctx)
	if ok {
		t.Error("PrincipalFromContext() returned true for empty context")
	}

	// Test with principal
	ctxWithP := WithPrincipal(ctx, p)
	got, ok := PrincipalFromContext(ctxWithP)
	if !ok {
		t.Error("PrincipalFromContext() returned false for context with principal")
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("PrincipalFromContext() = %v, want %v", got, p)
	}
}

func TestConstantTimeEqual(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			name: "equal",
			a:    "secret",
			b:    "secret",
			want: true,
		},
		{
			name: "different length",
			a:    "secret",
			b:    "secrett",
			want: false,
		},
		{
			name: "different content",
			a:    "secret",
			b:    "secred",
			want: false,
		},
		{
			name: "empty a",
			a:    "",
			b:    "secret",
			want: false,
		},
		{
			name: "empty b",
			a:    "secret",
			b:    "",
			want: false,
		},
		{
			name: "both empty",
			a:    "",
			b:    "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := constantTimeEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("constantTimeEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}
