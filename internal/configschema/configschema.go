// Package configschema exposes the JSON Schemas embedded in the binary and
// validates configuration against them. It is the authoritative, tamper-proof
// source of "what valid config looks like" — read from the embedded bytes, not
// the on-disk schemas/ files (ADR §11).
//
// Validation here is deliberately STATIC: it parses YAML structure and checks it
// against the schema. It performs no decryption and needs no age key, so it runs
// with the caller's privilege — an AI operator can validate config as an
// unprivileged utility even when the gateway daemon runs as root.
package configschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	ductile "github.com/mattjoyce/ductile"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	schemaDir    = "schemas"
	schemaSuffix = ".schema.json"
)

// foldedSchemaNames are the per-section schema names that #36 folded into the
// single lenient "config" schema (their definitions live in config.schema.json's
// $defs). They MUST stay disjoint from Names() — a name that is both folded and
// live would let the back-compat alias shadow a real schema. The
// TestFoldedNamesDisjointFromLive guard enforces that.
var foldedSchemaNames = map[string]bool{
	"include": true, "plugins": true, "webhooks": true,
	"routes": true, "relay-ingress": true, "relay-instances": true,
}

// FoldedInto reports whether name is a schema that #36 folded into "config".
// When folded it returns ("config", true) so callers can validate against the
// canonical schema (and tell the operator their old --name still works); when
// not, it returns the name unchanged and false.
func FoldedInto(name string) (canonical string, folded bool) {
	if foldedSchemaNames[name] {
		return "config", true
	}
	return name, false
}

// Names returns the available schema names (e.g. "config", "pipelines",
// "tokens"), sorted. The name is the file basename minus the .schema.json
// suffix.
func Names() []string {
	files, err := fs.Glob(ductile.SchemaFS, path.Join(schemaDir, "*"+schemaSuffix))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, nameOf(f))
	}
	sort.Strings(names)
	return names
}

// Bytes returns the embedded schema document for a name.
func Bytes(name string) ([]byte, error) {
	data, err := ductile.SchemaFS.ReadFile(fileFor(name))
	if err != nil {
		return nil, fmt.Errorf("unknown schema %q (try one of: %s)", name, strings.Join(Names(), ", "))
	}
	return data, nil
}

// yamlToInstance normalises a YAML document to a jsonschema-ready instance
// (YAML → JSON → UnmarshalJSON) so types line up with JSON Schema (e.g.
// integers, numbers) before validation.
func yamlToInstance(yamlData []byte) (any, error) {
	var raw any
	if err := yaml.Unmarshal(yamlData, &raw); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("normalise to json: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, fmt.Errorf("decode instance: %w", err)
	}
	return instance, nil
}

// ValidateYAML validates a YAML document against the named schema. A nil return
// means valid.
func ValidateYAML(name string, yamlData []byte) error {
	instance, err := yamlToInstance(yamlData)
	if err != nil {
		return err
	}
	sch, err := compile(name)
	if err != nil {
		return err
	}
	return sch.Validate(instance)
}

// ValidateYAMLWhole validates a YAML document against the strict whole-config
// contract (config.schema.json#/$defs/WholeConfig), which adds the top-level
// completeness requirements (service, state, plugin_roots) that the lenient
// `config` root deliberately omits so that single include fragments can validate
// on their own. Use this for "is this a complete, single-file config?" — the
// CLI `config validate --whole` and, in future, the validate_config_on_boot gate.
func ValidateYAMLWhole(yamlData []byte) error {
	instance, err := yamlToInstance(yamlData)
	if err != nil {
		return err
	}
	sch, err := compileTarget("config", "#/$defs/WholeConfig")
	if err != nil {
		return err
	}
	return sch.Validate(instance)
}

// compile builds the schema for a name, registering every embedded schema as a
// resource (under its $id) so any cross-references resolve regardless.
func compile(name string) (*jsonschema.Schema, error) {
	return compileTarget(name, "")
}

// compileTarget is compile with an optional JSON-pointer anchor (e.g.
// "#/$defs/WholeConfig") so a caller can target a sub-schema of a named schema
// rather than its root. An empty anchor compiles the root.
func compileTarget(name, anchor string) (*jsonschema.Schema, error) {
	files, err := fs.Glob(ductile.SchemaFS, path.Join(schemaDir, "*"+schemaSuffix))
	if err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}
	c := jsonschema.NewCompiler()
	var targetID string
	for _, f := range files {
		data, err := ductile.SchemaFS.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read schema %s: %w", f, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parse schema %s: %w", f, err)
		}
		id, err := idOf(doc, f)
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("register schema %s: %w", f, err)
		}
		if nameOf(f) == name {
			targetID = id
		}
	}
	if targetID == "" {
		return nil, fmt.Errorf("unknown schema %q (try one of: %s)", name, strings.Join(Names(), ", "))
	}
	if anchor != "" {
		// $id already ends with the document URI; append the JSON-pointer fragment.
		targetID = strings.TrimSuffix(targetID, "#") + anchor
	}
	return c.Compile(targetID)
}

// idOf returns a schema's $id. Every embedded schema must declare one: it is the
// base URI against which sibling schemas resolve their cross-file $refs (e.g.
// plugins.schema.json -> config.schema.json#/$defs/PluginConf). A schema without
// an $id would silently break that resolution, so a missing $id is a hard error.
func idOf(doc any, file string) (string, error) {
	if m, ok := doc.(map[string]any); ok {
		if id, ok := m["$id"].(string); ok && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("embedded schema %s has no $id (required for cross-file $ref resolution)", file)
}

func nameOf(file string) string {
	return strings.TrimSuffix(path.Base(file), schemaSuffix)
}

func fileFor(name string) string {
	return path.Join(schemaDir, name+schemaSuffix)
}
