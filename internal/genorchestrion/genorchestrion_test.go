package genorchestrion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGenerate(t *testing.T) {
	// Find the contrib directory relative to this test
	contribDir := filepath.Join("..", "..", "trace", "contrib")

	output, err := Generate(contribDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Parse the output to verify structure
	var result OrchestrionConfig
	if err := yaml.Unmarshal(output, &result); err != nil {
		t.Fatalf("Failed to parse generated YAML: %v", err)
	}

	// Verify meta
	if result.Meta.Name != "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all" {
		t.Errorf("unexpected meta.name: %s", result.Meta.Name)
	}

	// Verify we have aspects from all providers
	aspectIDs := make(map[string]bool)
	for _, aspect := range result.Aspects {
		aspectIDs[aspect.ID] = true
	}

	expectedAspects := []string{
		"openai-newclient-middleware",
		"openai-v2-newclient-middleware",
		"anthropic-newclient-middleware",
		"sashabaranov-newclientwithconfig-wrap",
		"genai-newclient-wrap",
		"langchaingo-openai-newllm-callback",
	}

	for _, expected := range expectedAspects {
		if !aspectIDs[expected] {
			t.Errorf("missing expected aspect: %s", expected)
		}
	}

	// Verify the output has the header comments
	outputStr := string(output)
	if !strings.HasPrefix(outputStr, "# yaml-language-server: $schema=") {
		t.Error("output should start with yaml-language-server schema comment")
	}
	if !strings.Contains(outputStr, "AUTO-GENERATED FILE") {
		t.Error("output should contain AUTO-GENERATED FILE comment")
	}
}

func TestGenerateExcludesAllDirectory(t *testing.T) {
	// The generator should NOT include aspects from all/orchestrion.yml
	// to avoid duplication
	contribDir := filepath.Join("..", "..", "trace", "contrib")

	output, err := Generate(contribDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	var result OrchestrionConfig
	if err := yaml.Unmarshal(output, &result); err != nil {
		t.Fatalf("Failed to parse generated YAML: %v", err)
	}

	// Count aspects - should be exactly 13 (one per provider, plus openai-v2, 4 for adk, and 3 for genkit)
	// If it's more, we might be including duplicates from all/
	if len(result.Aspects) != 13 {
		t.Errorf("expected 13 aspects, got %d", len(result.Aspects))
	}
}

func TestGenerateWithTestData(t *testing.T) {
	// Create a temp directory with test yml files
	tmpDir := t.TempDir()

	// Create provider1 directory with orchestrion.yml
	provider1Dir := filepath.Join(tmpDir, "provider1")
	if err := os.MkdirAll(provider1Dir, 0755); err != nil {
		t.Fatal(err)
	}
	provider1YML := `meta:
  name: test/provider1
  description: Provider 1
aspects:
  - id: provider1-aspect
    join-point:
      function-call: example.com/provider1.NewClient
    advice:
      - append-args:
          type: example.com/provider1/option.Option
          values:
            - template: option.WithMiddleware()
`
	if err := os.WriteFile(filepath.Join(provider1Dir, "orchestrion.yml"), []byte(provider1YML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create provider2 directory with orchestrion.yml
	provider2Dir := filepath.Join(tmpDir, "provider2")
	if err := os.MkdirAll(provider2Dir, 0755); err != nil {
		t.Fatal(err)
	}
	provider2YML := `meta:
  name: test/provider2
  description: Provider 2
aspects:
  - id: provider2-aspect
    join-point:
      function-call: example.com/provider2.NewClient
    advice:
      - wrap-expression:
          template: wrapped()
`
	if err := os.WriteFile(filepath.Join(provider2Dir, "orchestrion.yml"), []byte(provider2YML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create "all" directory that should be excluded
	allDir := filepath.Join(tmpDir, "all")
	if err := os.MkdirAll(allDir, 0755); err != nil {
		t.Fatal(err)
	}
	allYML := `meta:
  name: test/all
aspects:
  - id: should-be-excluded
    join-point:
      function-call: example.com/excluded.NewClient
`
	if err := os.WriteFile(filepath.Join(allDir, "orchestrion.yml"), []byte(allYML), 0644); err != nil {
		t.Fatal(err)
	}

	output, err := Generate(tmpDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	var result OrchestrionConfig
	if err := yaml.Unmarshal(output, &result); err != nil {
		t.Fatalf("Failed to parse generated YAML: %v", err)
	}

	// Should have exactly 2 aspects (provider1 + provider2, not "all")
	if len(result.Aspects) != 2 {
		t.Errorf("expected 2 aspects, got %d", len(result.Aspects))
	}

	aspectIDs := make(map[string]bool)
	for _, aspect := range result.Aspects {
		aspectIDs[aspect.ID] = true
	}

	if !aspectIDs["provider1-aspect"] {
		t.Error("missing provider1-aspect")
	}
	if !aspectIDs["provider2-aspect"] {
		t.Error("missing provider2-aspect")
	}
	if aspectIDs["should-be-excluded"] {
		t.Error("should-be-excluded aspect should not be present")
	}
}

func TestCommittedFileIsUpToDate(t *testing.T) {
	// This test ensures the committed all/orchestrion.yml is in sync
	// with the individual provider yml files.
	// If this test fails, run: make generate

	contribDir := filepath.Join("..", "..", "trace", "contrib")
	committedPath := filepath.Join(contribDir, "all", "orchestrion.yml")

	// Generate what the file should contain
	expected, err := Generate(contribDir)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// Read the committed file
	actual, err := os.ReadFile(committedPath)
	if err != nil {
		t.Fatalf("Failed to read committed file: %v", err)
	}

	if string(actual) != string(expected) {
		t.Errorf("trace/contrib/all/orchestrion.yml is out of date.\n\nRun 'make generate' to update it.")
	}
}
