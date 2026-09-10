package evalrunner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

func TestListMode_EmptyRunnerEmitsEmptyObject(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})

	require.NoError(t, Run(context.Background(), r))

	assert.JSONEq(t, `{}`, stdout.String())
}

// bt scans the child's stdout in reverse for the last JSON-parseable line, so
// the manifest must be exactly one line and nothing may follow it.
func TestListMode_ManifestIsASingleLineOfJSON(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "my-eval", nil)

	require.NoError(t, Run(context.Background(), r))

	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 1, "manifest must be one line")
	assert.True(t, json.Valid([]byte(lines[0])))
	assert.True(t, strings.HasSuffix(stdout.String(), "\n"), "manifest must be newline-terminated")
}

func TestListMode_ManifestGolden(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "food-classifier", eval.ParameterSchema{
		"model": {Type: "model", Default: "rule-based", Description: "Classification strategy"},
	})

	require.NoError(t, Run(context.Background(), r))

	assert.JSONEq(t, `{
		"food-classifier": {
			"scores": [{"name": "exact_match"}],
			"parameters": {
				"type": "braintrust.staticParameters",
				"schema": {
					"model": {
						"type": "model",
						"default": "rule-based",
						"description": "Classification strategy"
					}
				},
				"source": null
			}
		}
	}`, stdout.String())
}

// Scalar parameters ride in the union's "data" arm: type "data" plus a nested
// schema object naming the real type.
func TestListMode_ScalarParameterWireShape(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "param-eval", eval.ParameterSchema{
		"threshold": {Type: "number", Default: 0.5, Description: "Cutoff"},
	})

	require.NoError(t, Run(context.Background(), r))

	var manifest map[string]struct {
		Parameters *parametersMeta `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &manifest))

	param := manifest["param-eval"].Parameters.Schema["threshold"]
	assert.Equal(t, "data", param.Type)
	require.NotNil(t, param.Schema)
	assert.Equal(t, "number", param.Schema.Type)
	assert.InDelta(t, 0.5, param.Default, 0.0001)
	assert.Equal(t, "Cutoff", param.Description)
}

// A model parameter uses the union's "model" arm: top-level type "model" and no
// nested schema.
func TestListMode_ModelParameterWireShape(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "model-eval", eval.ParameterSchema{
		"model": {Type: "model", Default: "gpt-4o"},
	})

	require.NoError(t, Run(context.Background(), r))

	var manifest map[string]struct {
		Parameters *parametersMeta `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &manifest))

	param := manifest["model-eval"].Parameters.Schema["model"]
	assert.Equal(t, "model", param.Type)
	assert.Nil(t, param.Schema, "model params must not carry a nested schema")
}

// A prompt parameter uses the union's "prompt" arm, and its Go-declared default
// has to serialize as the prompt data the playground expects.
func TestListMode_PromptParameterWireShape(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "prompt-eval", eval.ParameterSchema{
		"summary_prompt": {
			Type:        eval.ParameterTypePrompt,
			Description: "How to summarize",
			Default: prompt.Definition{
				Model:    "gpt-4o-mini",
				Messages: []prompt.Message{prompt.User("Summarize {{input}}")},
				Params:   map[string]any{"temperature": 0},
			},
		},
	})

	require.NoError(t, Run(context.Background(), r))

	var manifest map[string]struct {
		Parameters *parametersMeta `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &manifest))

	param := manifest["prompt-eval"].Parameters.Schema["summary_prompt"]
	assert.Equal(t, "prompt", param.Type)
	assert.Nil(t, param.Schema, "prompt params must not carry a nested schema")
	assert.Equal(t, "How to summarize", param.Description)

	// The default is prompt data, not the Go Definition shape.
	def, ok := param.Default.(map[string]any)
	require.True(t, ok, "default should be an object, got %T", param.Default)
	block, ok := def["prompt"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "chat", block["type"])
	options, ok := def["options"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "gpt-4o-mini", options["model"])
}

func TestListMode_EvalWithoutParametersOmitsTheKey(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "plain", nil)

	require.NoError(t, Run(context.Background(), r))

	var manifest map[string]map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &manifest))
	assert.NotContains(t, manifest["plain"], "parameters")
}

// List mode needs no API access at all, so it must work with no credentials --
// bt polls it constantly while a playground is open.
func TestListMode_NeedsNoCredentials(t *testing.T) {
	t.Setenv("BRAINTRUST_API_KEY", "")

	r, stdout := newTestRunner(t, map[string]string{"BT_EVAL_DEV_MODE": "list"})
	registerTestEval(r, "my-eval", nil)

	require.NoError(t, Run(context.Background(), r))
	assert.Contains(t, stdout.String(), "my-eval")
}

func TestListMode_HonoursFilters(t *testing.T) {
	r, stdout := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":      "list",
		"BT_EVAL_FILTER_PARSED": `[{"path":["name"],"pattern":"food"}]`,
	})
	registerTestEval(r, "food-classifier", nil)
	registerTestEval(r, "tone-checker", nil)

	require.NoError(t, Run(context.Background(), r))

	var manifest map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &manifest))
	assert.Contains(t, manifest, "food-classifier")
	assert.NotContains(t, manifest, "tone-checker")
}

func registerTestEval(r *Runner, name string, schema eval.ParameterSchema) {
	RegisterEval(r, &eval.Eval[string, string]{
		Name: name,
		Task: eval.T(func(_ context.Context, input string) (string, error) {
			return input, nil
		}),
		ParameterSchema: schema,
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("exact_match", func(_ context.Context, res eval.TaskResult[string, string]) (eval.Scores, error) {
				if res.Output == res.Expected {
					return eval.S(1.0), nil
				}
				return eval.S(0.0), nil
			}),
		},
	})
}
