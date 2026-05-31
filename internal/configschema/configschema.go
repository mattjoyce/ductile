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

// Names returns the available schema names (e.g. "config", "plugins",
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

// ValidateYAML validates a YAML document against the named schema. YAML is
// normalised through JSON so types line up with JSON Schema (e.g. integers,
// numbers) before validation. A nil return means valid.
func ValidateYAML(name string, yamlData []byte) error {
	var raw any
	if err := yaml.Unmarshal(yamlData, &raw); err != nil {
		return fmt.Errorf("parse yaml: %w", err)
	}
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("normalise to json: %w", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return fmt.Errorf("decode instance: %w", err)
	}
	sch, err := compile(name)
	if err != nil {
		return err
	}
	return sch.Validate(instance)
}

// compile builds the schema for a name, registering every embedded schema as a
// resource (under its $id) so any cross-references resolve regardless.
func compile(name string) (*jsonschema.Schema, error) {
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
