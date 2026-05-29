package btx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// LlmSpanSpec represents a parsed YAML spec file for an LLM span test.
type LlmSpanSpec struct {
	Name                    string
	Type                    string
	Provider                string
	Endpoint                string
	Headers                 map[string]string
	Requests                []map[string]any
	ExpectedBrainstoreSpans []map[string]any
	SourcePath              string
	DisplayName             string // "provider/name"
}

// FnMatcher represents a !fn custom YAML tag.
type FnMatcher struct {
	Expr string
}

// StartsWithMatcher represents a !starts_with custom YAML tag.
type StartsWithMatcher struct {
	Prefix string
}

// OrMatcher represents a !or custom YAML tag.
type OrMatcher struct {
	Alternatives []any
}

// loadSpecs walks the spec directory and returns all specs for the given providers.
// specRoot can point to the repository root (containing test/llm_span/) or
// directly to the llm_span directory itself.
func loadSpecs(specRoot string, providers []string) ([]LlmSpanSpec, error) {
	llmSpanDir := filepath.Join(specRoot, "test", "llm_span")
	if _, err := os.Stat(llmSpanDir); err != nil {
		// Try using specRoot directly as the llm_span directory.
		llmSpanDir = specRoot
	}

	providerSet := make(map[string]bool, len(providers))
	for _, p := range providers {
		providerSet[p] = true
	}

	var specs []LlmSpanSpec
	err := filepath.Walk(llmSpanDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}

		// Determine provider from directory structure: llm_span/<provider>/<name>.yaml
		rel, err := filepath.Rel(llmSpanDir, path)
		if err != nil {
			return err
		}
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 2)
		if len(parts) < 2 {
			return nil
		}
		provider := parts[0]
		if !providerSet[provider] {
			return nil
		}

		spec, err := loadSpec(path, provider)
		if err != nil {
			return fmt.Errorf("loading spec %s: %w", path, err)
		}
		specs = append(specs, spec)

		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(specs, func(i, j int) bool {
		return specs[i].DisplayName < specs[j].DisplayName
	})

	return specs, nil
}

// loadSpec parses a single YAML spec file.
func loadSpec(path, provider string) (LlmSpanSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LlmSpanSpec{}, err
	}

	// Parse into a yaml.Node tree to handle custom tags.
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return LlmSpanSpec{}, fmt.Errorf("parsing YAML: %w", err)
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return LlmSpanSpec{}, fmt.Errorf("unexpected YAML structure in %s", path)
	}

	raw := resolveNode(doc.Content[0])
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return LlmSpanSpec{}, fmt.Errorf("expected map at top level of %s", path)
	}

	// Resolve variables and template substitution.
	variables := resolveVariables(rawMap)
	if len(variables) > 0 {
		substituted, ok := substituteTemplates(rawMap, variables).(map[string]any)
		if !ok {
			return LlmSpanSpec{}, fmt.Errorf("template substitution did not produce a map in %s", path)
		}
		rawMap = substituted
	}

	spec := LlmSpanSpec{
		Name:       stringField(rawMap, "name"),
		Type:       stringField(rawMap, "type"),
		Provider:   provider,
		Endpoint:   stringField(rawMap, "endpoint"),
		SourcePath: path,
	}
	spec.DisplayName = spec.Provider + "/" + spec.Name

	// Parse headers.
	if h, ok := rawMap["headers"]; ok {
		if hm, ok := h.(map[string]any); ok {
			spec.Headers = make(map[string]string, len(hm))
			for k, v := range hm {
				spec.Headers[k] = fmt.Sprintf("%v", v)
			}
		}
	}

	// Parse requests.
	if reqs, ok := rawMap["requests"]; ok {
		if reqList, ok := reqs.([]any); ok {
			for _, r := range reqList {
				if rm, ok := r.(map[string]any); ok {
					spec.Requests = append(spec.Requests, rm)
				}
			}
		}
	}

	// Parse expected spans.
	if spans, ok := rawMap["expected_brainstore_spans"]; ok {
		if spanList, ok := spans.([]any); ok {
			for _, s := range spanList {
				if sm, ok := s.(map[string]any); ok {
					spec.ExpectedBrainstoreSpans = append(spec.ExpectedBrainstoreSpans, sm)
				}
			}
		}
	}

	return spec, nil
}

// resolveNode converts a yaml.Node tree into Go values, handling custom tags.
func resolveNode(node *yaml.Node) any {
	switch node.Tag {
	case "!fn":
		return FnMatcher{Expr: node.Value}
	case "!starts_with":
		return StartsWithMatcher{Prefix: node.Value}
	case "!or":
		// !or is applied to a sequence.
		if node.Kind == yaml.SequenceNode {
			items := make([]any, len(node.Content))
			for i, child := range node.Content {
				items[i] = resolveNode(child)
			}
			return OrMatcher{Alternatives: items}
		}
		return OrMatcher{}
	case "!gen":
		return resolveGenerator(node.Value)
	}

	switch node.Kind {
	case yaml.MappingNode:
		m := make(map[string]any, len(node.Content)/2)
		for i := 0; i < len(node.Content)-1; i += 2 {
			key := node.Content[i].Value
			val := resolveNode(node.Content[i+1])
			m[key] = val
		}
		return m

	case yaml.SequenceNode:
		items := make([]any, len(node.Content))
		for i, child := range node.Content {
			items[i] = resolveNode(child)
		}
		return items

	case yaml.ScalarNode:
		return resolveScalar(node)

	case yaml.AliasNode:
		return resolveNode(node.Alias)

	default:
		return node.Value
	}
}

// resolveScalar converts a YAML scalar node into the appropriate Go type.
func resolveScalar(node *yaml.Node) any {
	// Unmarshal using yaml.v3's type inference for scalars.
	var val any
	if err := node.Decode(&val); err != nil {
		return node.Value
	}
	return val
}

// resolveGenerator resolves a !gen tag value.
func resolveGenerator(name string) string {
	switch name {
	case "test_runner_client":
		return "go-btx"
	case "vcr_nonce":
		if vcr.GetVCRMode() == vcr.ModeReplay {
			return "replay-nonce"
		}
		return uuid.New().String()[:8]
	default:
		return name
	}
}

// resolveVariables extracts and resolves the "variables" map from a raw spec.
func resolveVariables(rawMap map[string]any) map[string]string {
	vars, ok := rawMap["variables"]
	if !ok {
		return nil
	}
	varMap, ok := vars.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(varMap))
	for k, v := range varMap {
		result[k] = fmt.Sprintf("%v", v)
	}
	return result
}

// substituteTemplates recursively replaces {{var}} placeholders in strings.
func substituteTemplates(val any, vars map[string]string) any {
	switch v := val.(type) {
	case string:
		result := v
		for name, value := range vars {
			result = strings.ReplaceAll(result, "{{"+name+"}}", value)
		}
		return result
	case map[string]any:
		m := make(map[string]any, len(v))
		for key, value := range v {
			m[key] = substituteTemplates(value, vars)
		}
		return m
	case []any:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = substituteTemplates(item, vars)
		}
		return items
	default:
		return val
	}
}

// stringField extracts a string field from a map, returning "" if missing.
func stringField(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
