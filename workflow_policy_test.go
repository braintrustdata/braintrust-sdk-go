package braintrust

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type actionsWorkflow struct {
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress *bool  `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Jobs map[string]struct {
		TimeoutMinutes int      `yaml:"timeout-minutes"`
		Needs          []string `yaml:"needs"`
		If             string   `yaml:"if"`
		Steps          []struct {
			Name           string `yaml:"name"`
			TimeoutMinutes int    `yaml:"timeout-minutes"`
			Run            string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func readActionsWorkflow(t *testing.T, path string) actionsWorkflow {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var workflow actionsWorkflow
	require.NoError(t, yaml.Unmarshal(data, &workflow))
	return workflow
}

// Keep new jobs and steps from silently inheriting GitHub's six-hour timeout.
func TestActionsWorkflowsHaveBoundedRuntime(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob(".github/workflows/*.y*ml")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			workflow := readActionsWorkflow(t, path)
			assert.NotEmpty(t, workflow.Concurrency.Group, "serialize duplicate runs")
			require.NotNil(t, workflow.Concurrency.CancelInProgress)
			// Superseded CI should stop; release mutations must finish uninterrupted.
			assert.Equal(t, filepath.Base(path) == "ci.yml", *workflow.Concurrency.CancelInProgress)
			require.NotEmpty(t, workflow.Jobs)
			for name, job := range workflow.Jobs {
				t.Run(name, func(t *testing.T) {
					assert.Positive(t, job.TimeoutMinutes, "set an explicit job timeout")
					assert.LessOrEqual(t, job.TimeoutMinutes, 30, "keep jobs within 30 minutes")
					for _, step := range job.Steps {
						assert.Positive(t, step.TimeoutMinutes, "step %q needs a timeout", step.Name)
						assert.LessOrEqual(t, step.TimeoutMinutes, job.TimeoutMinutes, "step %q exceeds its job timeout", step.Name)
					}
				})
			}
		})
	}
}

func TestActionsCIGateRejectsUnsuccessfulDependencies(t *testing.T) {
	t.Parallel()
	workflow := readActionsWorkflow(t, ".github/workflows/ci.yml")
	gate := workflow.Jobs["ci-passed"]
	assert.Contains(t, gate.If, "!cancelled()", "run the gate when dependencies fail or are skipped")
	require.Len(t, gate.Steps, 1)
	for name := range workflow.Jobs {
		if name != "ci-passed" {
			assert.Contains(t, gate.Needs, name, "the gate must cover every CI job")
		}
	}
	for _, result := range []string{"success", "failure", "cancelled", "skipped"} {
		t.Run(result, func(t *testing.T) {
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", gate.Steps[0].Run)
			cmd.Env = append(os.Environ(), `NEEDS_JSON={"test":{"result":"`+result+`"},"lint":{"result":"success"}}`)
			output, err := cmd.CombinedOutput()
			if result == "success" {
				require.NoError(t, err, string(output))
			} else {
				require.Error(t, err, strings.TrimSpace(string(output)))
			}
		})
	}
}

// Exercise the real Makefile with tiny Go modules: an early failure must not
// be hidden by a later successful module in the shell loop.
func TestActionsMakeTargetsStopOnModuleFailure(t *testing.T) {
	t.Parallel()
	makefile, err := os.ReadFile("Makefile")
	require.NoError(t, err)
	moduleScript, err := os.ReadFile("scripts/list_nested_modules.sh")
	require.NoError(t, err)
	for _, target := range []string{"test", "build"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"Makefile":                       string(makefile),
				"scripts/list_nested_modules.sh": string(moduleScript),
				"scripts/nested_modules.txt":     "broken\npassing\n",
				"go.mod":                         "module example.com/ci\n\ngo 1.25.0\n",
				"main.go":                        "package ci\n",
				"examples/internal/workflow-eval/main.go": "package workflow\n",
				"broken/go.mod":   "module example.com/broken\n\ngo 1.25.0\n",
				"passing/go.mod":  "module example.com/passing\n\ngo 1.25.0\n",
				"passing/main.go": "package passing\n",
				"btx/go.mod":      "module example.com/btx\n\ngo 1.25.0\n",
				"btx/main.go":     "package btx\n",
			}
			if target == "build" {
				files["broken/main.go"] = "package broken\nvar _ = undefined\n"
			} else {
				files["broken/main_test.go"] = "package broken\nimport \"testing\"\nfunc TestFailure(t *testing.T) { t.Fatal(\"intentional CI failure\") }\n"
			}
			for path, contents := range files {
				path := filepath.Join(dir, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
			}
			require.NoError(t, os.Chmod(filepath.Join(dir, "scripts/list_nested_modules.sh"), 0o700))
			cmd := exec.Command("make", target, "GO_TEST_FLAGS=-timeout=30s")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
			output, err := cmd.CombinedOutput()
			require.Contains(t, string(output), "example.com/broken", "must exercise the failing module")
			require.Error(t, err, "make %s should fail on the first broken module:\n%s", target, output)
			assert.NotContains(t, string(output), "example.com/passing", "stop before the next module")
		})
	}
}
