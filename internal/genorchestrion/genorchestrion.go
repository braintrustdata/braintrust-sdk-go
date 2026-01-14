// Package genorchestrion generates combined orchestrion.yml files from individual provider configs.
package genorchestrion

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// OrchestrionConfig represents the structure of an orchestrion.yml file.
type OrchestrionConfig struct {
	Meta    Meta     `yaml:"meta"`
	Aspects []Aspect `yaml:"aspects"`
}

// Meta contains metadata about the orchestrion configuration.
type Meta struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
}

// Aspect represents a single orchestrion aspect.
type Aspect struct {
	ID        string    `yaml:"id"`
	JoinPoint yaml.Node `yaml:"join-point"`
	Advice    yaml.Node `yaml:"advice"`
}

const fileHeader = `# yaml-language-server: $schema=https://datadoghq.dev/orchestrion/schema.json
#
# AUTO-GENERATED FILE - DO NOT EDIT
#
# This file is generated from individual orchestrion.yml files in trace/contrib/.
# To update, modify the source files and run: make generate
#
`

// Generate reads all orchestrion.yml files from provider directories under contribDir,
// combines their aspects, and returns the generated YAML content.
// It excludes the "all" directory to avoid circular inclusion.
func Generate(contribDir string) ([]byte, error) {
	var ymlFiles []string

	// Walk directory tree to find all orchestrion.yml files
	err := filepath.WalkDir(contribDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip "all" and "testdata" directories entirely
		if d.IsDir() {
			name := d.Name()
			if name == "all" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Name() == "orchestrion.yml" {
			ymlFiles = append(ymlFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking contrib directory: %w", err)
	}

	// Sort for deterministic output
	sort.Strings(ymlFiles)

	var allAspects []Aspect
	for _, ymlPath := range ymlFiles {
		aspects, err := readAspects(ymlPath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", ymlPath, err)
		}
		allAspects = append(allAspects, aspects...)
	}

	// Build the combined config
	combined := OrchestrionConfig{
		Meta: Meta{
			Name:        "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all",
			Description: "All Braintrust tracing integrations for LLM providers",
		},
		Aspects: allAspects,
	}

	// Marshal to YAML
	var buf bytes.Buffer
	buf.WriteString(fileHeader)

	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(combined); err != nil {
		return nil, fmt.Errorf("encoding YAML: %w", err)
	}

	return buf.Bytes(), nil
}

func readAspects(path string) ([]Aspect, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config OrchestrionConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}

	return config.Aspects, nil
}

// GenerateFile generates the combined orchestrion.yml and writes it to outputPath.
func GenerateFile(contribDir, outputPath string) error {
	content, err := Generate(contribDir)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputPath, content, 0644); err != nil {
		return fmt.Errorf("writing output file: %w", err)
	}

	return nil
}
