package config

import "testing"

func cfgWithTokens(tokens []APIToken, resolved map[string]string) *Config {
	c := &Config{ResolvedSecrets: resolved}
	c.API.Auth.Tokens = tokens
	return c
}

func TestResolveAPITokens_SecretRefResolves(t *testing.T) {
	cfg := cfgWithTokens(
		[]APIToken{{SecretRef: "ductile-api-admin", Scopes: []string{"*"}}},
		map[string]string{"ductile-api-admin": "s3cr3t"},
	)

	got, warnings, err := ResolveAPITokens(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("vault-backed token should not warn, got %v", warnings)
	}
	if len(got) != 1 || got[0].Token != "s3cr3t" {
		t.Fatalf("expected resolved value s3cr3t, got %+v", got)
	}
	if len(got[0].Scopes) != 1 || got[0].Scopes[0] != "*" {
		t.Fatalf("scopes not carried through: %+v", got[0].Scopes)
	}
}

func TestResolveAPITokens_MissingRefFailsClosed(t *testing.T) {
	cfg := cfgWithTokens(
		[]APIToken{{SecretRef: "ductile-api-admin"}},
		map[string]string{}, // vault has no such secret
	)

	if _, _, err := ResolveAPITokens(cfg); err == nil {
		t.Fatal("expected error for unresolvable secret_ref, got nil (would open API with no credential)")
	}
}

// Proves the security boundary: a secret_ref present in the projection but
// holding an empty value must NOT silently become an empty bearer credential.
func TestResolveAPITokens_EmptyResolvedFailsClosed(t *testing.T) {
	cfg := cfgWithTokens(
		[]APIToken{{SecretRef: "ductile-api-admin"}},
		map[string]string{"ductile-api-admin": ""},
	)

	if _, _, err := ResolveAPITokens(cfg); err == nil {
		t.Fatal("expected error for empty resolved secret, got nil")
	}
}

func TestResolveAPITokens_LiteralWarnsButResolves(t *testing.T) {
	cfg := cfgWithTokens(
		[]APIToken{{Token: "legacy-literal", Scopes: []string{"read:jobs"}}},
		nil,
	)

	got, warnings, err := ResolveAPITokens(cfg)
	if err != nil {
		t.Fatalf("literal token should resolve, got error: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("literal token should produce exactly one migration warning, got %v", warnings)
	}
	if len(got) != 1 || got[0].Token != "legacy-literal" {
		t.Fatalf("expected literal value carried through, got %+v", got)
	}
}

func TestResolveAPITokens_BothSourcesIsError(t *testing.T) {
	cfg := cfgWithTokens(
		[]APIToken{{Token: "lit", SecretRef: "ref"}},
		map[string]string{"ref": "v"},
	)

	if _, _, err := ResolveAPITokens(cfg); err == nil {
		t.Fatal("expected error when both token and secret_ref are set")
	}
}

func TestResolveAPITokens_NeitherSourceIsError(t *testing.T) {
	cfg := cfgWithTokens([]APIToken{{Scopes: []string{"*"}}}, nil)

	if _, _, err := ResolveAPITokens(cfg); err == nil {
		t.Fatal("expected error when neither token nor secret_ref is set")
	}
}

func TestResolveAPITokens_NoTokensIsClean(t *testing.T) {
	cfg := cfgWithTokens(nil, nil)

	got, warnings, err := ResolveAPITokens(cfg)
	if err != nil {
		t.Fatalf("empty token list should be clean, got error: %v", err)
	}
	if len(got) != 0 || len(warnings) != 0 {
		t.Fatalf("expected no resolved tokens and no warnings, got %d/%d", len(got), len(warnings))
	}
}
