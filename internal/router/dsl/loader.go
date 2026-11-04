package dsl

import (
	"fmt"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// LoadAndCompileFiles parses pipeline definitions from the provided YAML files
// and compiles them into validated DAGs.
func LoadAndCompileFiles(paths []string) (*Set, error) {
	var specs []PipelineSpec
	for _, filePath := range paths {
		if strings.TrimSpace(filePath) == "" {
			continue
		}
		fileSpec, err := LoadFile(filePath)
		if err != nil {
			return nil, err
		}
		specs = append(specs, fileSpec.Pipelines...)
	}

	if len(specs) == 0 {
		return &Set{Pipelines: map[string]*Pipeline{}}, nil
	}

	return CompileSpecs(specs)
}

// LoadFile parses one pipeline YAML file.
func LoadFile(path string) (*FileSpec, error) {
	// #nosec G304 -- pipeline files are operator-controlled local inputs.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pipeline file %q: %w", path, err)
	}

	var fileSpec FileSpec
	if err := yaml.Unmarshal(data, &fileSpec); err != nil {
		return nil, fmt.Errorf("parse pipeline file %q: %w", path, err)
	}

	for i, pipeline := range fileSpec.Pipelines {
		fileSpec.Pipelines[i].Name = strings.TrimSpace(pipeline.Name)
		fileSpec.Pipelines[i].On = strings.TrimSpace(pipeline.On)
	}
	return &fileSpec, nil
}
