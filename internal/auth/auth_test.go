package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestAuthenticate(t *testing.T) {
	tokens := []TokenConfig{
		{Token: "valid-token-1", Scopes: []string{"jobs:ro"}},
		{Name: "api-admin", Token: "valid-token-2", Scopes: []string{"*"}},
		{Name: "  ", Token: "plugin-token", Scopes: []string{"plugin:rw"}},
	}

	tests := []struct {
		name          string
		presented     string
		tokens        []TokenConfig
		wantPrincipal Principal
		wantBool      bool
	}{
		{
			name:          "unnamed token is named by position",
			presented:     "valid-token-1",
			tokens:        tokens,
			wantPrincipal: Principal{Name: "tokens[0]"},
			wantBool:      true,
		},
		{
			name:          "named token keeps its name",
			presented:     "valid-token-2",
			tokens:        tokens,
			wantPrincipal: Principal{Name: "api-admin"},
			wantBool:      true,
		},
		{
			name:          "blank name falls back to position",
			presented:     "plugin-token",
			tokens:        tokens,
			wantPrincipal: Principal{Name: "tokens[2]"},
			wantBool:      true,
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

func TestPrincipalContext(t *testing.T) {
	ctx := context.Background()
	p := Principal{Name: "test-token"}

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
