package plugin

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// CommandType is a coarse permission hint for a command.
// It is used by authorization middleware to separate read-only commands from
// commands that may mutate state or cause side effects.
type CommandType string

const (
	CommandTypeRead  CommandType = "read"
	CommandTypeWrite CommandType = "write"

	SupportedManifestSpec    = "ductile.plugin"
	SupportedManifestVersion = 1
)

func (t CommandType) valid() bool {
	return t == CommandTypeRead || t == CommandTypeWrite
}

// Command declares a supported plugin command and its type.
type Command struct {
	Name         string      `yaml:"name"`
	Type         CommandType `yaml:"type"`
	Description  string      `yaml:"description,omitempty"`
	InputSchema  any         `yaml:"input_schema,omitempty"`
	OutputSchema any         `yaml:"output_schema,omitempty"`
	Values       *Values     `yaml:"values,omitempty"`
	Idempotent   *bool       `yaml:"idempotent,omitempty"`
	RetrySafe    *bool       `yaml:"retry_safe,omitempty"`
}

// Values declares names a command consumes and emits.
//
// It is intentionally names-only. Input/output schemas remain the legacy typed
// surfaces for now; values is the author-facing contract for payload names.
// Pipeline authors still decide which values become durable baggage.
type Values struct {
	Consume []string        `yaml:"consume,omitempty"`
	Emit    []EmittedValues `yaml:"emit,omitempty"`
}

// EmittedValues declares names in one event payload emitted by a command.
type EmittedValues struct {
	Event  string   `yaml:"event"`
	Values []string `yaml:"values,omitempty"`
}

// FactOutputWhen declares when a fact output rule applies.
type FactOutputWhen struct {
	Command string `yaml:"command"`
}

// FactOutputRule declares how a plugin response becomes a durable fact.
type FactOutputRule struct {
	When              FactOutputWhen `yaml:"when"`
	From              string         `yaml:"from"`
	FactType          string         `yaml:"fact_type"`
	CompatibilityView string         `yaml:"compatibility_view,omitempty"`
}

// GetFullInputSchema returns the expanded JSON Schema for the input.
func (c Command) GetFullInputSchema() any {
	return expandSchema(c.InputSchema)
}

// GetFullOutputSchema returns the expanded JSON Schema for the output.
func (c Command) GetFullOutputSchema() any {
	return expandSchema(c.OutputSchema)
}

func expandSchema(schema any) any {
	if schema == nil {
		return nil
	}

	m, ok := schema.(map[string]any)
	if !ok {
		return schema
	}

	// If it already looks like a JSON schema (has "type"), return as-is
	if _, hasType := m["type"]; hasType {
		return schema
	}

	// Otherwise, treat as a compact map of property:type
	properties := make(map[string]any)
	for k, v := range m {
		propType, isString := v.(string)
		if isString {
			properties[k] = map[string]string{"type": propType}
		} else {
			properties[k] = v
		}
	}

	return map[string]any{
		"type":       "object",
		"properties": properties,
	}
}

// Commands is a list of supported commands.
//
// Commands must be expressed as objects: commands: [{name: poll, type: write}]
type Commands []Command

func (c *Commands) UnmarshalYAML(n *yaml.Node) error {
	if n == nil {
		*c = nil
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("commands must be a sequence")
	}

	out := make([]Command, 0, len(n.Content))
	for _, item := range n.Content {
		if item.Kind != yaml.MappingNode {
			return fmt.Errorf("invalid command entry (must be object with name/type)")
		}
		var tmp Command
		if err := item.Decode(&tmp); err != nil {
			return fmt.Errorf("invalid command object: %w", err)
		}
		tmp.Name = strings.TrimSpace(tmp.Name)
		if tmp.Name == "" {
			return fmt.Errorf("command name is required")
		}
		if tmp.Type == "" {
			return fmt.Errorf("command %q is missing required type", tmp.Name)
		}
		if !tmp.Type.valid() {
			return fmt.Errorf("command %q has invalid type %q", tmp.Name, tmp.Type)
		}
		out = append(out, tmp)
	}

	*c = out
	return nil
}

// Manifest defines the structure of a plugin's manifest.yaml file.
type Manifest struct {
	ManifestSpec    string             `yaml:"manifest_spec"`
	ManifestVersion int                `yaml:"manifest_version"`
	Name            string             `yaml:"name"`
	Version         string             `yaml:"version"`
	Protocol        int                `yaml:"protocol"`
	Entrypoint      string             `yaml:"entrypoint"`
	Description     string             `yaml:"description,omitempty"`
	ConcurrencySafe *bool              `yaml:"concurrency_safe,omitempty"`
	Commands        Commands           `yaml:"commands"`
	FactOutputs     []FactOutputRule   `yaml:"fact_outputs,omitempty"`
	ConfigKeys      *ConfigKeys        `yaml:"config_keys,omitempty"`
	Stopwatch       *StopwatchManifest `yaml:"stopwatch,omitempty"`
}

// ConfigKeys defines required and optional configuration keys for a plugin.
type ConfigKeys struct {
	Required []string `yaml:"required,omitempty"`
	Optional []string `yaml:"optional,omitempty"`
}

// StopwatchManifest declares per-plugin overrides for stopwatch behavior.
// Currently only MaxSubs is supported. A nil pointer means "use defaults"
// (see internal/stopwatch.MaxSubsPerRecord).
type StopwatchManifest struct {
	// MaxSubs overrides stopwatch.MaxSubsPerRecord for this plugin. Must
	// be in [1, stopwatch.MaxSubsHardUpper]; values outside that range
	// are rejected at manifest load. Zero / absent means "use default".
	MaxSubs int `yaml:"max_subs,omitempty"`
}

// Plugin represents a discovered and validated plugin.
type Plugin struct {
	ManifestSpec    string   // Manifest spec identifier.
	ManifestVersion int      // Manifest schema version.
	Name            string   // Plugin name from manifest.
	Path            string   // Absolute path to plugin directory.
	Entrypoint      string   // Absolute path to entrypoint executable.
	Protocol        int      // Protocol version.
	Version         string   // Plugin version.
	Description     string   // Human-readable description.
	ConcurrencySafe bool     // Manifest concurrency safety hint (default true when unspecified).
	Commands        Commands // Supported commands (poll, handle, health, init).
	FactOutputs     []FactOutputRule
	ConfigKeys      *ConfigKeys
	Stopwatch       *StopwatchManifest // Optional per-plugin stopwatch overrides.
}

// SupportsCommand checks if the plugin supports a given command.
func (p *Plugin) SupportsCommand(cmd string) bool {
	for _, c := range p.Commands {
		if c.Name == cmd {
			return true
		}
	}
	return false
}

// CommandTypeFor returns the declared type for a command.
func (p *Plugin) CommandTypeFor(cmd string) (CommandType, bool) {
	for _, c := range p.Commands {
		if c.Name == cmd {
			return c.Type, true
		}
	}
	return "", false
}

// GetReadCommands returns the command names marked type=read, preserving manifest order.
func (p *Plugin) GetReadCommands() []string {
	var out []string
	for _, c := range p.Commands {
		if c.Type == CommandTypeRead {
			out = append(out, c.Name)
		}
	}
	return out
}

// GetWriteCommands returns the command names marked type=write, preserving manifest order.
func (p *Plugin) GetWriteCommands() []string {
	var out []string
	for _, c := range p.Commands {
		if c.Type == CommandTypeWrite {
			out = append(out, c.Name)
		}
	}
	return out
}

// FactOutputRulesForCommand returns declared fact output rules for a command.
func (p *Plugin) FactOutputRulesForCommand(cmd string) []FactOutputRule {
	if p == nil {
		return nil
	}

	var out []FactOutputRule
	for _, rule := range p.FactOutputs {
		if rule.When.Command == cmd {
			out = append(out, rule)
		}
	}
	return out
}
