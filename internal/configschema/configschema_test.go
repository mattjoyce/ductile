package configschema

import (
	"slices"
	"strings"
	"testing"
)

const minimalValidConfig = `
service:
  name: ductile
  tick_interval: 60s
state:
  path: /tmp/ductile.db
plugin_roots:
  - ./plugins
`

func TestNamesIncludesCoreSchemas(t *testing.T) {
	names := Names()
	for _, want := range []string{"config", "plugins", "tokens"} {
		if !slices.Contains(names, want) {
			t.Errorf("Names() = %v, missing %q", names, want)
		}
	}
}

func TestBytesReturnsEmbeddedSchema(t *testing.T) {
	data, err := Bytes("config")
	if err != nil {
		t.Fatalf("Bytes(config): %v", err)
	}
	if !strings.Contains(string(data), "\"$id\"") {
		t.Fatal("config schema bytes missing $id")
	}
	if _, err := Bytes("does-not-exist"); err == nil {
		t.Fatal("Bytes of unknown schema returned no error")
	}
}

// TestCrossFileRefsResolve proves register-all-by-$id actually resolves the
// cross-file $refs (e.g. plugins.schema.json -> config.schema.json#/$defs/...).
// If registration were wrong, compile() would fail with an unresolved $ref.
func TestCrossFileRefsResolve(t *testing.T) {
	for _, name := range []string{"plugins", "include", "webhooks", "routes", "relay-ingress", "relay-instances"} {
		if _, err := compile(name); err != nil {
			t.Errorf("compile(%q) failed — cross-file $ref did not resolve: %v", name, err)
		}
	}
}

func TestValidateYAMLValidConfigPasses(t *testing.T) {
	if err := ValidateYAML("config", []byte(minimalValidConfig)); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// Proves the schema edits from batch 1 (secrets.age_key_file,
// service.plugin_env_passthrough) match the embedded schema.
func TestValidateYAMLAcceptsNewSecretsFields(t *testing.T) {
	cfg := `
service:
  name: ductile
  tick_interval: 60s
  plugin_env_passthrough:
    - HTTPS_PROXY
state:
  path: /tmp/ductile.db
plugin_roots:
  - ./plugins
secrets:
  age_key_file: age.key
`
	if err := ValidateYAML("config", []byte(cfg)); err != nil {
		t.Fatalf("config with secrets/passthrough rejected: %v", err)
	}
}

func TestValidateYAMLUnknownKeyFails(t *testing.T) {
	cfg := minimalValidConfig + "bogus_top_level: true\n"
	err := ValidateYAML("config", []byte(cfg))
	if err == nil {
		t.Fatal("unknown top-level key passed validation (additionalProperties:false should bite)")
	}
	if !strings.Contains(err.Error(), "bogus_top_level") && !strings.Contains(err.Error(), "additional") {
		t.Logf("note: error did not name the offending key clearly: %v", err)
	}
}

func TestValidateYAMLWrongTypeFails(t *testing.T) {
	cfg := `
service:
  name: ductile
  tick_interval: 60s
  max_workers: "lots"
state:
  path: /tmp/x.db
plugin_roots:
  - ./plugins
`
	if err := ValidateYAML("config", []byte(cfg)); err == nil {
		t.Fatal("string max_workers passed integer validation")
	}
}

func TestValidateYAMLErrorNamesPath(t *testing.T) {
	cfg := `
service:
  name: ductile
  tick_interval: 60s
  max_workers: "lots"
state:
  path: /tmp/x.db
plugin_roots:
  - ./plugins
`
	err := ValidateYAML("config", []byte(cfg))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "max_workers") {
		t.Errorf("validation error should name the offending path; got: %v", err)
	}
}
