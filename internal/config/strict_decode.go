package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// dedicatedScopeDomains are top-level config domains that are NOT decoded
// field-by-field into Config — Config carries Pipelines as `yaml:"-"` and loads
// it via a dedicated path. A file that only carries such a domain is not a
// Config document, so a strict Config-decode would mis-report its top key as
// "unknown". Skip those files in the strict-decode warning pass.
var dedicatedScopeDomains = map[string]bool{
	"pipelines": true,
}

// StrictDecodeWarnings re-decodes the already-loaded config's source files
// strictly (yaml KnownFields) and returns a warning for every key the lenient
// load-time decode silently dropped — a typo'd or unsupported key the operator
// likely believes is active (#26). It is warn-only by design: the load itself
// stays lenient, so a config the daemon already accepts keeps booting; these
// warnings only make the dropped keys visible. Type-mismatch noise from
// un-set ${ENV} placeholders is filtered out — only unknown-field lines remain.
//
// It uses the same interpolation the loader applies before decoding, so the
// strict pass sees exactly what the lenient pass saw.
func StrictDecodeWarnings(cfg *Config) []string {
	files := make([]string, 0, len(cfg.SourceFiles))
	for f := range cfg.SourceFiles {
		files = append(files, f)
	}
	sort.Strings(files)

	var warnings []string
	for _, f := range files {
		node := cfg.SourceFiles[f]
		if node == nil {
			continue
		}
		if onlyDedicatedScope(topLevelMapKeys(node)) {
			continue
		}
		data, err := yaml.Marshal(node)
		if err != nil {
			continue // best-effort: never block boot on a serialisation hiccup
		}
		dec := yaml.NewDecoder(strings.NewReader(interpolateEnv(string(data))))
		dec.KnownFields(true)
		var probe Config
		if derr := dec.Decode(&probe); derr != nil {
			for _, line := range unknownFieldLines(derr) {
				warnings = append(warnings, fmt.Sprintf("%s: %s", filepath.Base(f), line))
			}
		}
	}
	return warnings
}

// StrictDecodeError is the hard-gate companion to StrictDecodeWarnings (#26): it
// returns a non-nil error naming every key the lenient load silently dropped, or
// nil when the decode is clean. The daemon promotes dropped keys from a warning
// to an admission failure when service.admission.validate_config_on_boot is set;
// otherwise the keys stay warn-only. It uses the same struct-decode mechanism as
// StrictDecodeWarnings (not the embedded JSON schema, which is the CLI lint).
func StrictDecodeError(cfg *Config) error {
	w := StrictDecodeWarnings(cfg)
	if len(w) == 0 {
		return nil
	}
	return fmt.Errorf("%d ignored config key(s): %s", len(w), strings.Join(w, "; "))
}

// unknownFieldLines pulls the "field X not found in type ..." lines out of a yaml
// decode error, dropping type-mismatch lines so the warnings are only about keys
// the decode dropped, not values it could not parse.
func unknownFieldLines(err error) []string {
	var out []string
	for _, line := range strings.Split(err.Error(), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "not found in type") {
			out = append(out, line)
		}
	}
	return out
}

// onlyDedicatedScope reports whether every top-level key belongs to a dedicated
// scope domain (so the file is not a Config document and must be skipped).
func onlyDedicatedScope(keys []string) bool {
	if len(keys) == 0 {
		return false
	}
	for _, k := range keys {
		if !dedicatedScopeDomains[k] {
			return false
		}
	}
	return true
}

// topLevelMapKeys returns the top-level mapping keys of a loaded YAML document.
func topLevelMapKeys(node *yaml.Node) []string {
	root := node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		keys = append(keys, root.Content[i].Value)
	}
	return keys
}
