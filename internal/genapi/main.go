// Command genapi fetches the pinned OpenAPI specification and generates REST bindings.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type config struct {
	Spec struct {
		Repository string `json:"repository"`
		Path       string `json:"path"`
		Commit     string `json:"commit"`
		SHA256     string `json:"sha256"`
	} `json:"spec"`
	Generator struct {
		Version string `json:"version"`
	} `json:"generator"`
}

var (
	commitPattern  = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	digestPattern  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

func main() {
	if err := run(context.Background(), ".", os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "genapi: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, root string, args []string) error {
	if len(args) != 1 || (args[0] != "fetch" && args[0] != "generate" && args[0] != "check") {
		return fmt.Errorf("usage: go run ./internal/genapi {fetch|generate|check}")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	c, err := readConfig(root)
	if err != nil {
		return err
	}
	if args[0] == "fetch" {
		return fetch(ctx, root, c)
	}

	source := filepath.Join(root, "openapi", "spec.json")
	input, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read %s (run make fetch-openapi-spec first): %w", source, err)
	}
	if err := validateSpec(input, c.Spec.SHA256); err != nil {
		return fmt.Errorf("validate %s: %w", source, err)
	}
	normalized, err := normalizeSpec(input)
	if err != nil {
		return fmt.Errorf("normalize %s: %w", source, err)
	}
	return generate(ctx, root, c.Generator.Version, normalized, args[0] == "check")
}

func readConfig(root string) (config, error) {
	var c config
	path := filepath.Join(root, "openapi", "config.json")
	input, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read %s (run from the repository root): %w", path, err)
	}
	if err := json.Unmarshal(input, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return c, fmt.Errorf("validate %s: %w", path, err)
	}
	return c, nil
}

func (c config) validate() error {
	if strings.TrimSpace(c.Spec.Repository) == "" {
		return fmt.Errorf("spec.repository must not be empty")
	}
	if strings.TrimSpace(c.Spec.Path) == "" || !filepath.IsLocal(c.Spec.Path) || strings.ContainsAny(c.Spec.Path, `\:`) {
		return fmt.Errorf("spec.path must be a nonempty relative path without parent traversal")
	}
	for _, part := range strings.Split(c.Spec.Path, "/") {
		if part == ".." {
			return fmt.Errorf("spec.path must not contain parent traversal")
		}
	}
	if !commitPattern.MatchString(c.Spec.Commit) {
		return fmt.Errorf("spec.commit must be a full 40-hex commit")
	}
	if !digestPattern.MatchString(c.Spec.SHA256) {
		return fmt.Errorf("spec.sha256 must be a 64-hex SHA-256 digest")
	}
	if !versionPattern.MatchString(c.Generator.Version) {
		return fmt.Errorf("generator.version must be an explicit vMAJOR.MINOR.PATCH release")
	}
	return nil
}

func fetch(ctx context.Context, root string, c config) error {
	var input []byte
	var source string
	var err error
	if localRoot := os.Getenv("BRAINTRUST_OPENAPI_ROOT"); localRoot != "" {
		source = filepath.Join(localRoot, filepath.FromSlash(c.Spec.Path))
		input, err = os.ReadFile(source)
	} else {
		source = "https://raw.githubusercontent.com/" + c.Spec.Repository + "/" + c.Spec.Commit + "/" + c.Spec.Path
		input, err = download(ctx, source)
	}
	if err != nil {
		return fmt.Errorf("fetch %s: %w", source, err)
	}
	if err := validateSpec(input, c.Spec.SHA256); err != nil {
		return fmt.Errorf("validate %s: %w", source, err)
	}
	return atomicWrite(filepath.Join(root, "openapi", "spec.json"), input)
}

func download(ctx context.Context, source string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	return io.ReadAll(response.Body)
}

func validateSpec(input []byte, expectedDigest string) error {
	actualDigest := fmt.Sprintf("%x", sha256.Sum256(input))
	if !strings.EqualFold(actualDigest, expectedDigest) {
		return fmt.Errorf("SHA-256 mismatch: got %s, want %s", actualDigest, expectedDigest)
	}
	// Raw messages validate the document shape without decoding or changing any
	// schema numbers. The upstream bytes are always installed unchanged.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(input, &document); err != nil {
		return fmt.Errorf("parse OpenAPI JSON object: %w", err)
	}
	if document == nil {
		return fmt.Errorf("OpenAPI document must be a JSON object")
	}
	var version string
	if err := json.Unmarshal(document["openapi"], &version); err != nil || version != "3.0.3" {
		return fmt.Errorf("OpenAPI document must declare openapi: \"3.0.3\"")
	}
	var paths map[string]json.RawMessage
	if err := json.Unmarshal(document["paths"], &paths); err != nil || paths == nil {
		return fmt.Errorf("OpenAPI paths must be a JSON object")
	}
	var components map[string]json.RawMessage
	if err := json.Unmarshal(document["components"], &components); err != nil || components == nil {
		return fmt.Errorf("OpenAPI components must be a JSON object")
	}
	var schemas map[string]json.RawMessage
	if err := json.Unmarshal(components["schemas"], &schemas); err != nil || schemas == nil {
		return fmt.Errorf("OpenAPI components.schemas must be a JSON object")
	}
	return nil
}

func generate(ctx context.Context, root, version string, normalized []byte, check bool) error {
	temp, err := os.MkdirTemp("", "braintrust-openapi-*")
	if err != nil {
		return fmt.Errorf("create generation directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := os.WriteFile(filepath.Join(temp, "spec.json"), normalized, 0600); err != nil {
		return fmt.Errorf("write normalized specification: %w", err)
	}
	command := exec.CommandContext(ctx, "go", "run",
		"github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@"+version,
		"-config", filepath.Join(root, "openapi", "oapi-codegen.yaml"),
		"-o", filepath.Join(temp, "api.gen.go"), filepath.Join(temp, "spec.json"))
	command.Dir = root
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run oapi-codegen %s: %w", version, err)
	}
	generated, err := os.ReadFile(filepath.Join(temp, "api.gen.go"))
	if err != nil {
		return fmt.Errorf("read generated client: %w", err)
	}
	tags, err := operationTags(normalized)
	if err != nil {
		return err
	}
	files, err := splitGenerated(generated, tags)
	if err != nil {
		return fmt.Errorf("split generated client: %w", err)
	}
	return installGenerated(root, files, check)
}

// installGenerated writes the generated files into openapi/ and removes stale
// generated files. In check mode it only reports drift, including added or
// removed files.
func installGenerated(root string, files map[string][]byte, check bool) error {
	dir := filepath.Join(root, "openapi")
	existing, err := filepath.Glob(filepath.Join(dir, "*.gen.go"))
	if err != nil {
		return err
	}
	if check {
		for name, content := range files {
			path := filepath.Join(dir, name)
			committed, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("check %s: %w; run make generate-api-client", path, err)
			}
			if !bytes.Equal(committed, content) {
				return fmt.Errorf("generated client differs from %s; run make generate-api-client", path)
			}
		}
		for _, path := range existing {
			if _, ok := files[filepath.Base(path)]; !ok {
				return fmt.Errorf("stale generated file %s; run make generate-api-client", path)
			}
		}
		return nil
	}
	for name, content := range files {
		if err := atomicWrite(filepath.Join(dir, name), content); err != nil {
			return err
		}
	}
	for _, path := range existing {
		if _, ok := files[filepath.Base(path)]; !ok {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove stale %s: %w", path, err)
			}
		}
	}
	return nil
}

func atomicWrite(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".genapi-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary file for %s: %w", path, err)
	}
	if err := temp.Chmod(0644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set permissions for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %s: %w", path, err)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
