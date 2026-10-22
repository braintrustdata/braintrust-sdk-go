package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var validSpec = []byte("{\n  \"openapi\": \"3.0.3\", \"paths\": {}, \"components\": {\"schemas\": {}}\n}\n")

func TestFetchPreservesDestinationOnFailure(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		digest string
	}{
		{name: "wrong digest", input: validSpec, digest: strings.Repeat("0", 64)},
		{name: "missing source"},
		{name: "invalid JSON", input: []byte("not JSON")},
		{name: "trailing JSON", input: append(append([]byte(nil), validSpec...), []byte(" {}")...)},
		{name: "null document", input: []byte("null")},
		{name: "array document", input: []byte("[]")},
		{name: "wrong OpenAPI version", input: []byte(`{"openapi":"3.1.0","paths":{},"components":{"schemas":{}}}`)},
		{name: "missing paths", input: []byte(`{"openapi":"3.0.3","components":{"schemas":{}}}`)},
		{name: "null paths", input: []byte(`{"openapi":"3.0.3","paths":null,"components":{"schemas":{}}}`)},
		{name: "missing schemas", input: []byte(`{"openapi":"3.0.3","paths":{},"components":{}}`)},
		{name: "array schemas", input: []byte(`{"openapi":"3.0.3","paths":{},"components":{"schemas":[]}}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, source, config := fetchFixture(t, tt.input)
			if tt.digest != "" {
				config.Spec.SHA256 = tt.digest
				writeConfig(t, root, config)
			}
			old := []byte("existing snapshot\n")
			destination := filepath.Join(root, "openapi", "spec.json")
			writeFixtureFile(t, destination, old)
			t.Setenv("BRAINTRUST_OPENAPI_ROOT", source)
			err := run(context.Background(), root, []string{"fetch"})
			if err == nil {
				t.Fatal("fetch succeeded for invalid input")
			}
			if !strings.Contains(err.Error(), filepath.Join(source, "openapi", "spec.json")) {
				t.Errorf("error does not identify source: %v", err)
			}
			assertFileBytes(t, destination, old)
		})
	}
}

func TestFetchPreservesOriginalBytes(t *testing.T) {
	root, source, config := fetchFixture(t, validSpec)
	t.Setenv("BRAINTRUST_OPENAPI_ROOT", source)
	if err := run(context.Background(), root, []string{"fetch"}); err != nil {
		t.Fatal(err)
	}
	content := assertFileBytes(t, filepath.Join(root, "openapi", "spec.json"), validSpec)
	if got := fmt.Sprintf("%x", sha256.Sum256(content)); got != config.Spec.SHA256 {
		t.Fatalf("snapshot digest = %s, want %s", got, config.Spec.SHA256)
	}
	var document map[string]any
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.0.3" {
		t.Fatalf("unexpected document: %v", document)
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		change func(*config)
	}{
		{"empty repository", func(c *config) { c.Spec.Repository = "" }},
		{"empty path", func(c *config) { c.Spec.Path = "" }},
		{"absolute path", func(c *config) { c.Spec.Path = "/tmp/spec.json" }},
		{"parent path", func(c *config) { c.Spec.Path = "../spec.json" }},
		{"embedded parent path", func(c *config) { c.Spec.Path = "openapi/../spec.json" }},
		{"backslash traversal", func(c *config) { c.Spec.Path = `..\spec.json` }},
		{"short commit", func(c *config) { c.Spec.Commit = "abc123" }},
		{"moving commit", func(c *config) { c.Spec.Commit = "main" }},
		{"nonhex commit", func(c *config) { c.Spec.Commit = strings.Repeat("g", 40) }},
		{"short digest", func(c *config) { c.Spec.SHA256 = "1234" }},
		{"nonhex digest", func(c *config) { c.Spec.SHA256 = strings.Repeat("z", 64) }},
		{"latest generator", func(c *config) { c.Generator.Version = "latest" }},
		{"unversioned generator", func(c *config) { c.Generator.Version = "" }},
		{"prerelease generator", func(c *config) { c.Generator.Version = "v2.8.0-rc.1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, source, config := fetchFixture(t, validSpec)
			tt.change(&config)
			writeConfig(t, root, config)
			t.Setenv("BRAINTRUST_OPENAPI_ROOT", source)
			if err := run(context.Background(), root, []string{"fetch"}); err == nil {
				t.Fatal("fetch accepted invalid configuration")
			}
			if _, err := os.Stat(filepath.Join(root, "openapi", "spec.json")); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration created destination: %v", err)
			}
		})
	}
}

func TestRunRejectsInvalidCommandAndMissingConfig(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"fetch", "extra"}, {"fetch"}, {"generate"}, {"check"}} {
		if err := run(context.Background(), t.TempDir(), args); err == nil {
			t.Errorf("run(%q) succeeded without a valid command/configuration", args)
		}
	}
}

func TestGenerateAndCheckRejectUnverifiedSnapshot(t *testing.T) {
	for _, command := range []string{"generate", "check"} {
		t.Run(command, func(t *testing.T) {
			root, source, _ := fetchFixture(t, validSpec)
			t.Setenv("BRAINTRUST_OPENAPI_ROOT", source)
			writeFixtureFile(t, filepath.Join(root, "openapi", "spec.json"), []byte("changed snapshot"))
			output := filepath.Join(root, "openapi", "api.gen.go")
			old := []byte("existing generated source\n")
			writeFixtureFile(t, output, old)
			err := run(context.Background(), root, []string{command})
			if err == nil || !strings.Contains(err.Error(), "SHA-256") {
				t.Fatalf("expected checksum rejection without fetching, got %v", err)
			}
			assertFileBytes(t, output, old)
		})
	}
}

func TestCheckGeneratedDoesNotWrite(t *testing.T) {
	for _, state := range []string{"matching", "different", "missing", "stale"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			output := filepath.Join(root, "openapi", "client.gen.go")
			stale := filepath.Join(root, "openapi", "api.gen.go")
			generated := []byte("generated source\n")
			old := generated
			if state == "different" {
				old = []byte("old generated source\n")
			}
			if state != "missing" {
				writeFixtureFile(t, output, old)
			}
			if state == "stale" {
				writeFixtureFile(t, stale, old)
			}
			err := installGenerated(root, map[string][]byte{"client.gen.go": generated}, true)
			if state == "matching" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("expected drift error for %s", state)
			}
			if state == "missing" {
				if _, err := os.Stat(output); !os.IsNotExist(err) {
					t.Fatalf("check created missing output: %v", err)
				}
			} else {
				assertFileBytes(t, output, old)
			}
			if state == "stale" {
				assertFileBytes(t, stale, old)
			}
		})
	}
}

func TestInstallGeneratedRemovesStaleFiles(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "openapi", "api.gen.go")
	handwritten := filepath.Join(root, "openapi", "api_test.go")
	writeFixtureFile(t, stale, []byte("old single file\n"))
	writeFixtureFile(t, handwritten, []byte("package openapi_test\n"))
	if err := installGenerated(root, map[string][]byte{"client.gen.go": []byte("generated source\n")}, false); err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, filepath.Join(root, "openapi", "client.gen.go"), []byte("generated source\n"))
	assertFileBytes(t, handwritten, []byte("package openapi_test\n"))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale generated file remains: %v", err)
	}
}

func TestCanceledGenerationPreservesDestination(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "openapi", "api.gen.go")
	old := []byte("existing generated source\n")
	writeFixtureFile(t, output, old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := generate(ctx, root, "v2.8.0", validSpec, false); err == nil {
		t.Fatal("canceled generation succeeded")
	}
	assertFileBytes(t, output, old)
}

func fetchFixture(t *testing.T, input []byte) (string, string, config) {
	t.Helper()
	root, source := t.TempDir(), t.TempDir()
	var c config
	c.Spec.Repository = "braintrustdata/braintrust-openapi"
	c.Spec.Path = "openapi/spec.json"
	c.Spec.Commit = strings.Repeat("a", 40)
	c.Spec.SHA256 = fmt.Sprintf("%x", sha256.Sum256(input))
	c.Generator.Version = "v2.8.0"
	writeConfig(t, root, c)
	if input != nil {
		writeFixtureFile(t, filepath.Join(source, "openapi", "spec.json"), input)
	}
	return root, source, c
}

func writeConfig(t *testing.T, root string, c config) {
	t.Helper()
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(root, "openapi", "config.json"), data)
}

func writeFixtureFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) []byte {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
	return got
}
