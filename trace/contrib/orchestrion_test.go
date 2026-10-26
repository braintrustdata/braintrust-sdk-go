package contrib

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// TestOrchestrionInjection verifies that orchestrion auto-injects our middleware
// into SDK client calls. It runs the same consumer fixture in two modes:
//  1. Importing the trace/contrib/all meta-module
//  2. Importing individual integration modules directly
//
// The inner tests create SDK clients WITHOUT manual middleware, make recorded API
// calls, and verify spans were created.
func TestOrchestrionInjection(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping orchestrion test in short mode")
	}

	// Check orchestrion is available.
	if _, err := exec.LookPath("orchestrion"); err != nil {
		t.Fatal("orchestrion not found in PATH (install: go install github.com/DataDog/orchestrion@v1.14.0)")
	}
	output, err := exec.Command("orchestrion", "version").CombinedOutput()
	if err != nil {
		t.Fatalf("get orchestrion version: %v\n%s", err, output)
	}
	orchestrionVersion, ok := strings.CutPrefix(strings.TrimSpace(string(output)), "orchestrion ")
	if !ok || orchestrionVersion == "" {
		t.Fatalf("unexpected orchestrion version output: %q", output)
	}
	t.Logf("using orchestrion %s", orchestrionVersion)

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	testCases := []struct {
		name    string
		imports []string
	}{
		{
			name: "all",
			imports: []string{
				"github.com/DataDog/orchestrion",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all",
			},
		},
		{
			name: "individual",
			imports: []string{
				"github.com/DataDog/orchestrion",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/a2a",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo",
				"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if vcr.GetVCRMode() != vcr.ModeRecord {
				t.Parallel()
			}
			fixtureDir := prepareOrchestrionFixture(t, repoRoot, orchestrionVersion, tc.imports)

			// Run the inner tests with orchestrion - this compiles with middleware injection.
			//
			// Important: force standalone module mode with GOWORK=off.
			//
			// This fixture is meant to simulate an external consumer module, and
			// orchestrion's pinning/setup flow edits that module with -mod=mod
			// semantics. After this repo introduced a top-level go.work, running the
			// fixture in workspace mode started failing with:
			//
			//   go: -mod may only be set to readonly or vendor when in workspace mode,
			//   but it is set to "mod"
			//
			// So we explicitly disable workspace mode here to keep the fixture isolated
			// and to match how a real downstream user would run orchestrion in their own
			// module.
			cmd := exec.Command("orchestrion", "go", "test", "-count=1", "-v", "./...")
			cmd.Dir = fixtureDir
			cmd.Env = append(os.Environ(), "GOWORK=off")
			// Record once, then verify individual imports against the same responses.
			if vcr.GetVCRMode() == vcr.ModeRecord && tc.name == "individual" {
				cmd.Env = append(cmd.Env, "VCR_MODE=replay")
			}
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			if err := cmd.Run(); err != nil {
				t.Fatalf("orchestrion go test failed: %v", err)
			}
			if vcr.GetVCRMode() == vcr.ModeRecord && tc.name == "all" {
				copyDir(t, filepath.Join(fixtureDir, "testdata", "cassettes"),
					filepath.Join("testdata", "orchestrion", "testdata", "cassettes"))
			}
		})
	}
}

func prepareOrchestrionFixture(t *testing.T, repoRoot, orchestrionVersion string, imports []string) string {
	t.Helper()

	srcDir := filepath.Join("testdata", "orchestrion")
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		t.Fatalf("testdata directory not found: %s", srcDir)
	}

	dstDir := filepath.Join(t.TempDir(), "orchestrion")
	copyDir(t, srcDir, dstDir)
	writeOrchestrionToolFile(t, filepath.Join(dstDir, "orchestrion.tool.go"), imports)
	configureFixtureModule(t, dstDir, repoRoot, orchestrionVersion)
	tidyFixtureModule(t, dstDir)

	return dstDir
}

func copyDir(t *testing.T, srcDir, dstDir string) {
	t.Helper()

	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dstDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		return os.WriteFile(targetPath, data, info.Mode())
	})
	if err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
}

func writeOrchestrionToolFile(t *testing.T, path string, imports []string) {
	t.Helper()

	content := "//go:build tools\n\n" +
		"// This file controls which orchestrion integrations are enabled.\n" +
		"// We only enable Braintrust integrations, NOT Datadog's dd-trace-go.\n\n" +
		"package main\n\n" +
		"import (\n"

	for _, imp := range imports {
		content += fmt.Sprintf("\t_ %q\n", imp)
	}
	content += ")\n"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write orchestrion tool file: %v", err)
	}
}

func configureFixtureModule(t *testing.T, fixtureDir, repoRoot, orchestrionVersion string) {
	t.Helper()

	replacements := map[string]string{
		"github.com/braintrustdata/braintrust-sdk-go":                                                 repoRoot,
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/a2a":                               filepath.Join(repoRoot, "trace", "contrib", "a2a"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/adk":                               filepath.Join(repoRoot, "trace", "contrib", "adk"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/all":                               filepath.Join(repoRoot, "trace", "contrib", "all"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic":                         filepath.Join(repoRoot, "trace", "contrib", "anthropic"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime":                    filepath.Join(repoRoot, "trace", "contrib", "bedrockruntime"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino":                    filepath.Join(repoRoot, "trace", "contrib", "cloudwego", "eino"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai":                             filepath.Join(repoRoot, "trace", "contrib", "genai"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit":                            filepath.Join(repoRoot, "trace", "contrib", "genkit"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/github.com/sashabaranov/go-openai": filepath.Join(repoRoot, "trace", "contrib", "github.com", "sashabaranov", "go-openai"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/langchaingo":                       filepath.Join(repoRoot, "trace", "contrib", "langchaingo"),
		"github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai":                            filepath.Join(repoRoot, "trace", "contrib", "openai"),
	}

	// Keep the committed fixture compatible with Go 1.25, then pin its tool
	// dependency to the installed binary. Orchestrion requires these versions
	// to match; newer releases also raise the fixture's Go minimum during tidy.
	// Apply local replacements in the same call to avoid subprocess round-trips.
	args := []string{"mod", "edit", "-require=github.com/DataDog/orchestrion@" + orchestrionVersion}
	for modulePath, replacementPath := range replacements {
		args = append(args, "-replace="+modulePath+"="+replacementPath)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = fixtureDir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure fixture module: %v\n%s", err, output)
	}
}

func tidyFixtureModule(t *testing.T, fixtureDir string) {
	t.Helper()

	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = fixtureDir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tidy fixture module: %v\n%s", err, output)
	}
}
