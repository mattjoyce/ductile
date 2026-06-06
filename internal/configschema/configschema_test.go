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
	// After the #36 consolidation the schema set is keyed by artifact identity:
	// config (all config, lenient root + WholeConfig overlay), pipelines (the DSL),
	// plugin-manifest (the plugin contract), tokens (directory-mode tokens.yaml).
	for _, want := range []string{"config", "pipelines", "plugin-manifest", "tokens"} {
		if !slices.Contains(names, want) {
			t.Errorf("Names() = %v, missing %q", names, want)
		}
	}
	// The drifted per-section duplicates were deleted; they must NOT reappear.
	for _, gone := range []string{"include", "plugins", "webhooks", "routes", "relay-ingress", "relay-instances"} {
		if slices.Contains(names, gone) {
			t.Errorf("Names() = %v, deleted duplicate schema %q is back", names, gone)
		}
	}
}

// TestFoldedNamesDisjointFromLive guards the back-compat shim: a name #36 folded
// into "config" must never also be a live schema, or `config validate --name X`
// would wrongly redirect a real schema to config. If a future change re-adds one
// of these as a real schema, this fails the build instead of silently lying.
func TestFoldedNamesDisjointFromLive(t *testing.T) {
	for name := range foldedSchemaNames {
		if canonical, folded := FoldedInto(name); !folded || canonical != "config" {
			t.Errorf("FoldedInto(%q) = (%q, %v), want (\"config\", true)", name, canonical, folded)
		}
		if slices.Contains(Names(), name) {
			t.Errorf("folded name %q is also a live schema — the alias would shadow it", name)
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
// cross-file $refs. Post-#36, config.schema.json is the one that reaches across
// files (its lenient root $refs pipelines.schema.json#/$defs/PipelineEntry and
// tokens.schema.json for the folded-in include surface); if registration were
// wrong, compile() would fail with an unresolved $ref.
func TestCrossFileRefsResolve(t *testing.T) {
	for _, name := range Names() {
		if _, err := compile(name); err != nil {
			t.Errorf("compile(%q) failed — cross-file $ref did not resolve: %v", name, err)
		}
	}
}

// TestFragmentsValidateAgainstLenientRoot proves the consolidation's core claim:
// a single-section include fragment (just `plugins:` or `webhooks:` or `api:`)
// validates against the lenient `config` root without false "missing
// service/state/plugin_roots" errors — and split sub-objects (webhooks.listen in
// one file, webhooks.endpoints in another) each validate alone.
func TestFragmentsValidateAgainstLenientRoot(t *testing.T) {
	fragments := map[string]string{
		"api":               "api:\n  enabled: true\n  listen: \"127.0.0.1:8081\"\n",
		"plugins":           "plugins:\n  echo:\n    enabled: true\n",
		"webhooks-listen":   "webhooks:\n  listen: \"127.0.0.1:8091\"\n",
		"webhooks-endpoint": "webhooks:\n  endpoints:\n    - path: /w/x\n      plugin: sys_exec\n",
	}
	for name, frag := range fragments {
		if err := ValidateYAML("config", []byte(frag)); err != nil {
			t.Errorf("fragment %q rejected by lenient config root: %v", name, err)
		}
	}
}

// TestWholeConfigRequiresTopLevel proves the strict overlay still enforces
// completeness: a fragment passes the lenient root but FAILS WholeConfig.
func TestWholeConfigRequiresTopLevel(t *testing.T) {
	frag := "plugins:\n  echo:\n    enabled: true\n"
	if err := ValidateYAML("config", []byte(frag)); err != nil {
		t.Fatalf("fragment should pass lenient root: %v", err)
	}
	if err := ValidateYAMLWhole([]byte(frag)); err == nil {
		t.Fatal("fragment passed WholeConfig — strict overlay not enforcing service/state/plugin_roots")
	}
	if err := ValidateYAMLWhole([]byte(minimalValidConfig)); err != nil {
		t.Fatalf("complete config rejected by WholeConfig: %v", err)
	}
}

// TestPluginTimeoutShorthandRejected locks in D1: the flat timeout/max_attempts
// keys (silently dropped by the struct) must be rejected so they can't masquerade
// as live config. The nested timeouts:/retry: form is the supported shape.
func TestPluginTimeoutShorthandRejected(t *testing.T) {
	flat := "plugins:\n  fabric:\n    enabled: true\n    timeout: 120s\n    max_attempts: 2\n"
	if err := ValidateYAML("config", []byte(flat)); err == nil {
		t.Fatal("flat timeout/max_attempts passed — they are not PluginConf fields and must be rejected")
	}
	nested := "plugins:\n  fabric:\n    enabled: true\n    timeouts:\n      poll: 120s\n      handle: 120s\n    retry:\n      max_attempts: 2\n"
	if err := ValidateYAML("config", []byte(nested)); err != nil {
		t.Fatalf("nested timeouts/retry form rejected: %v", err)
	}
}

// TestServiceAdmissionAndRetentionAccepted proves the schema no longer
// false-rejects real ServiceConfig fields (the admission block + the four
// retention durations) that additionalProperties:false used to drop.
func TestServiceAdmissionAndRetentionAccepted(t *testing.T) {
	cfg := `
service:
  name: ductile
  tick_interval: 60s
  job_queue_retention: 168h
  job_transitions_retention: 168h
  job_attempts_retention: 168h
  breaker_transitions_retention: 168h
  admission:
    verify_integrity_on_boot: true
    fail_on_drift: true
    validate_config_on_boot: true
    require_api_auth: true
state:
  path: /tmp/ductile.db
plugin_roots:
  - ./plugins
`
	if err := ValidateYAML("config", []byte(cfg)); err != nil {
		t.Fatalf("admission/retention fields rejected: %v", err)
	}
}

// TestEmptyPluginConfigAccepted proves `config:` left empty (YAML null) is
// accepted — it maps to a nil map in the struct, so the schema must allow null.
func TestEmptyPluginConfigAccepted(t *testing.T) {
	cfg := "plugins:\n  echo:\n    enabled: true\n    config:\n"
	if err := ValidateYAML("config", []byte(cfg)); err != nil {
		t.Fatalf("empty (null) plugin config rejected: %v", err)
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
