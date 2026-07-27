package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// GetPath retrieves a value from the configuration using a dot-notation path.
func (c *Config) GetPath(path string) (any, error) {
	// 1. Resolve Entity Addressing (type:name)
	if strings.Contains(path, ":") {
		return c.GetEntity(path)
	}

	// 2. Convert to map for generic traversal
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	// 3. Traverse
	return getValue(m, path)
}

// GetEntity retrieves a first-class entity (plugin, pipeline, etc.) by type:name.
func (c *Config) GetEntity(address string) (any, error) {
	parts := strings.SplitN(address, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid entity address format %q (expected type:name)", address)
	}

	entityType, name := parts[0], parts[1]

	switch entityType {
	case "plugin":
		if name == "*" {
			return c.Plugins, nil
		}
		p, ok := c.Plugins[name]
		if !ok {
			return nil, fmt.Errorf("plugin %q not found", name)
		}
		return p, nil

	case "pipeline":
		// Note: Pipelines are not yet in the main Config struct in some versions,
		// but per ROUTING_SPEC they should be. Checking current types.go.
		// For now, support what's in the struct.
		return nil, fmt.Errorf("entity type %q not yet fully implemented in accessor", entityType)

	case "webhook":
		if c.Webhooks == nil {
			return nil, fmt.Errorf("no webhooks configured")
		}
		if name == "*" {
			return c.Webhooks.Endpoints, nil
		}
		for _, ep := range c.Webhooks.Endpoints {
			// Webhooks don't have names in types.go yet, usually identified by path or plugin.
			// Using plugin name as proxy for now if path doesn't match.
			if ep.Path == name || ep.Plugin == name {
				return ep, nil
			}
		}
		return nil, fmt.Errorf("webhook %q not found", name)

	default:
		return nil, fmt.Errorf("unsupported entity type %q", entityType)
	}
}

func getValue(m map[string]any, path string) (any, error) {
	parts := strings.Split(path, ".")
	var current any = m

	for _, part := range parts {
		if part == "" {
			continue
		}

		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q breaks at %q (not a map)", path, part)
		}

		val, exists := m[part]
		if !exists {
			return nil, fmt.Errorf("path %q: key %q not found", path, part)
		}
		current = val
	}

	return current, nil
}

func findNode(node *yaml.Node, path string, create bool) (*yaml.Node, error) {
	parts := strings.Split(path, ".")
	current := node

	for _, part := range parts {
		if current.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("not a mapping node")
		}

		found := false
		for i := 0; i < len(current.Content); i += 2 {
			keyNode := current.Content[i]
			if keyNode.Value == part {
				current = current.Content[i+1]
				found = true
				break
			}
		}

		if !found {
			if create {
				// Add new key-value pair to mapping
				keyNode := &yaml.Node{
					Kind:  yaml.ScalarNode,
					Tag:   "!!str",
					Value: part,
				}
				valueNode := &yaml.Node{
					Kind: yaml.MappingNode, // Default to mapping if we have more parts
					Tag:  "!!map",
				}
				// If this is the last part, it will be overwritten by the value anyway
				current.Content = append(current.Content, keyNode, valueNode)
				current = valueNode
			} else {
				return nil, fmt.Errorf("key %q not found", part)
			}
		}
	}

	return current, nil
}

// SetPath modifies a configuration value at the specified path.
func (c *Config) SetPath(path, value string, persist bool) error {
	// ... (addressing logic)
	// 1. Resolve Entity Addressing (type:name)
	if strings.Contains(path, ":") {
		parts := strings.SplitN(path, ".", 2)
		entityAddr := parts[0]

		eparts := strings.SplitN(entityAddr, ":", 2)
		etype, ename := eparts[0], eparts[1]

		// Map type:name to physical YAML path
		var physicalPath string
		switch etype {
		case "plugin":
			physicalPath = "plugins." + ename
		case "webhook":
			return fmt.Errorf("setting webhook fields via entity address not yet implemented")
		default:
			return fmt.Errorf("unsupported entity type for set: %q", etype)
		}

		if len(parts) > 1 {
			path = physicalPath + "." + parts[1]
		} else {
			return fmt.Errorf("must specify a field to set (e.g., %s.enabled=false)", entityAddr)
		}
	}

	// 2. Identify which file owns the root of this path.
	targetFile := c.resolveTargetFile()
	if targetFile == "" {
		return fmt.Errorf("no valid configuration source found")
	}

	rootNode := c.SourceFiles[targetFile]
	if rootNode == nil || rootNode.Kind != yaml.DocumentNode {
		return fmt.Errorf("no valid configuration source found")
	}

	target, err := findNode(rootNode.Content[0], path, true)
	if err != nil {
		return fmt.Errorf("failed to navigate/create path %q: %w", path, err)
	}

	target.Kind = yaml.ScalarNode
	target.Value = value
	target.Tag = guessTag(value)

	if !persist {
		return nil
	}

	candidate, err := yaml.Marshal(rootNode)
	if err != nil {
		return err
	}

	return c.persistWithValidation(targetFile, candidate)
}

func guessTag(v string) string {
	if v == "true" || v == "false" {
		return "!!bool"
	}
	// Check for integer
	isDigit := true
	for i, c := range v {
		if i == 0 && c == '-' {
			continue
		}
		if c < '0' || c > '9' {
			isDigit = false
			break
		}
	}
	if isDigit && v != "" && v != "-" {
		return "!!int"
	}
	return "!!str"
}

func (c *Config) resolveTargetFile() string {
	for f := range c.SourceFiles {
		if strings.HasSuffix(f, "config.yaml") {
			return f
		}
	}
	for f := range c.SourceFiles {
		return f
	}
	return ""
}

func (c *Config) resolveRootConfigPath(fallback string) string {
	for f := range c.SourceFiles {
		if filepath.Base(f) == "config.yaml" {
			return f
		}
	}
	return fallback
}

func (c *Config) persistWithValidation(targetFile string, candidate []byte) error {
	// #nosec G304 -- targetFile is derived from config sources (operator-controlled).
	original, err := os.ReadFile(targetFile)
	if err != nil {
		return fmt.Errorf("failed to read original config file: %w", err)
	}

	mode := os.FileMode(0600)
	if info, statErr := os.Stat(targetFile); statErr == nil {
		mode = info.Mode().Perm()
	}

	// #177: was a bare os.WriteFile — the fifth .yaml writer in the tree and the
	// only one that neither wrote atomically nor negotiated ownership. It was
	// accidentally safe on the ownership axis because os.WriteFile truncates an
	// existing file in place and so keeps the inode's owner; that stops holding the
	// moment the target does not already exist. The atomicity gap had no such luck:
	// a crash, a full disk, or a kill between truncate and completion left
	// config.yaml partially written — the one file the daemon needs in order to boot
	// at all.
	if err := WriteFileAtomic(targetFile, candidate, mode); err != nil {
		return fmt.Errorf("failed to persist config change: %w", err)
	}

	rootPath := c.resolveRootConfigPath(targetFile)
	if _, err := Load(rootPath); err != nil {
		// The rollback goes through the same atomic path. Restoring with a
		// non-atomic write would mean the recovery from a bad edit could itself
		// leave the file truncated, which is a worse outcome than the edit.
		restoreErr := WriteFileAtomic(targetFile, original, mode)
		if restoreErr != nil {
			return fmt.Errorf("validation failed (%v) and rollback failed (%v)", err, restoreErr)
		}
		return fmt.Errorf("validation failed: %w", err)
	}

	return nil
}
