package contrib

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOrchestrionInjection verifies that orchestrion auto-injects our middleware
// into SDK client calls. It runs tests in testdata/orchestrion that:
// 1. Create SDK clients WITHOUT manual middleware
// 2. Make API calls (VCR mocked)
// 3. Verify spans were created (proves middleware was injected)
//
// Currently tests:
// - OpenAI (openai-go official SDK)
// - Anthropic (anthropic-sdk-go)
func TestOrchestrionInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping orchestrion test in short mode")
	}

	// Check orchestrion is available
	if _, err := exec.LookPath("orchestrion"); err != nil {
		t.Fatal("orchestrion not found in PATH (install: go install github.com/DataDog/orchestrion@v1.6.1)")
	}

	// Get the testdata directory
	testdataDir := filepath.Join("testdata", "orchestrion")
	if _, err := os.Stat(testdataDir); os.IsNotExist(err) {
		t.Fatalf("testdata directory not found: %s", testdataDir)
	}

	// Run the inner tests with orchestrion - this compiles with middleware injection.
	//
	// Important: force standalone module mode with GOWORK=off.
	//
	// This test fixture is meant to simulate an external consumer module under
	// trace/contrib/testdata/orchestrion, and orchestrion's pinning/setup flow
	// edits that module (for example via go list/go get/go mod tidy behavior that
	// assumes standalone module semantics). After this repo introduced a top-level
	// go.work, running this fixture in workspace mode started failing with:
	//
	//   go: -mod may only be set to readonly or vendor when in workspace mode,
	//   but it is set to "mod"
	//
	// So we explicitly disable workspace mode here to keep the fixture isolated
	// and to match how a real downstream user would run orchestrion in their own
	// module.
	cmd := exec.Command("orchestrion", "go", "test", "-v", "./...")
	cmd.Dir = testdataDir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("orchestrion go test failed: %v", err)
	}
}
