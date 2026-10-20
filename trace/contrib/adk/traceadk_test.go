package adk

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

func TestUsageMetrics(t *testing.T) {
	t.Run("aggregates_reasoning_and_tool_use_tokens", func(t *testing.T) {
		usage := &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        12,
			ToolUsePromptTokenCount: 3,
			CandidatesTokenCount:    9,
			ThoughtsTokenCount:      6,
			TotalTokenCount:         30,
			CachedContentTokenCount: 4,
		}

		assert.Equal(t, map[string]int64{
			"prompt_tokens":               15,
			"completion_tokens":           15,
			"completion_reasoning_tokens": 6,
			"tokens":                      30,
			"prompt_cached_tokens":        4,
		}, usageMetrics(usage))
	})

	t.Run("preserves_core_zero_values_and_omits_unavailable_details", func(t *testing.T) {
		assert.Equal(t, map[string]int64{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"tokens":            0,
		}, usageMetrics(&genai.GenerateContentResponseUsageMetadata{}))
	})

	t.Run("nil_usage", func(t *testing.T) {
		assert.Empty(t, usageMetrics(nil))
	})
}

func TestCleanupJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    interface{}
		expected interface{}
	}{
		{
			name: "map with empty values",
			input: map[string]interface{}{
				"name":        "test",
				"empty_bool":  false,
				"empty_int":   float64(0),
				"empty_str":   "",
				"empty_map":   map[string]interface{}{},
				"empty_slice": []interface{}{},
				"nil_value":   nil,
			},
			expected: map[string]interface{}{
				"name":       "test",
				"empty_bool": false,
				"empty_int":  float64(0),
			},
		},
		{
			name: "nested map with empty values",
			input: map[string]interface{}{
				"user": map[string]interface{}{
					"name":  "Alice",
					"email": "",
					"metadata": map[string]interface{}{
						"tags": []interface{}{},
					},
				},
				"count": float64(5),
			},
			expected: map[string]interface{}{
				"user": map[string]interface{}{
					"name": "Alice",
				},
				"count": float64(5),
			},
		},
		{
			name: "all empty values result in nil",
			input: map[string]interface{}{
				"empty1": "",
				"empty2": nil,
				"empty3": map[string]interface{}{},
				"empty4": []interface{}{},
			},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanupJSON(logger.Discard(), tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// calculatorArgs defines the input arguments for the calculator tool
type calculatorArgs struct {
	Operation string  `json:"operation" jsonschema:"The operation to perform: add, subtract, multiply, or divide"`
	A         float64 `json:"a" jsonschema:"The first number"`
	B         float64 `json:"b" jsonschema:"The second number"`
}

// calculator is a simple calculator function for testing
func calculator(ctx tool.Context, args calculatorArgs) (float64, error) {
	switch args.Operation {
	case "add":
		return args.A + args.B, nil
	case "subtract":
		return args.A - args.B, nil
	case "multiply":
		return args.A * args.B, nil
	case "divide":
		if args.B == 0 {
			return 0, assert.AnError
		}
		return args.A / args.B, nil
	default:
		return 0, assert.AnError
	}
}

// TestAgentIntegration tests the ADK tracing with a real agent using VCR
func TestAgentIntegration(t *testing.T) {
	ctx, tp, exporter := setupGoldenTest(t)

	// Create calculator tool
	calcTool, err := functiontool.New(
		functiontool.Config{
			Name:        "calculator",
			Description: "Performs basic arithmetic operations (add, subtract, multiply, divide)",
		},
		calculator,
	)
	require.NoError(t, err)

	// Create LLM agent with tool
	cfg := llmagent.Config{
		Name:        "math_agent",
		Model:       newTestModel(ctx, t, goldenModelID, tracegenai.WithTracerProvider(tp)),
		Description: "A helpful math assistant",
		Instruction: "You are a helpful math assistant. Use the calculator tool to perform calculations.",
		Tools:       []tool.Tool{calcTool},
		GenerateContentConfig: &genai.GenerateContentConfig{
			MaxOutputTokens: 500,
		},
	}
	sessionID := "test-session-integration"
	r := newGoldenRunner(ctx, t, tp, sessionID, cfg)
	responses := runAndCollectFinal(ctx, t, r, sessionID,
		genai.NewContentFromText("What is 127 multiplied by 49?", genai.RoleUser), agent.RunConfig{})
	assertFinalResponse(t, responses)

	spans := exporter.Flush()
	assertModelSpanHierarchy(t, spans)

	// Find agent, model, and tool spans
	// Note: There may be multiple call_llm spans, we want the one that's a child of the agent
	var agentSpan, modelSpan, toolSpan *oteltest.Span
	for i := range spans {
		s := &spans[i]
		name := s.Name()
		switch name {
		case "agent_run [math_agent]":
			agentSpan = s
		case "call_llm":
			// capture only the first one, which makes the tool call
			if modelSpan == nil {
				modelSpan = s
			}
		case "tool [calculator]":
			toolSpan = s
		}
	}

	// Verify agent span exists
	require.NotNil(t, agentSpan, "agent_run span not found")
	assert.True(t, agentSpan.Status().Code == codes.Ok || agentSpan.Status().Code == codes.Unset)
	agentSpan.AssertAttrEquals("adk.agent.name", "math_agent")

	// Verify model span exists
	require.NotNil(t, modelSpan, "call_llm span not found")
	assert.True(t, modelSpan.Status().Code == codes.Ok || modelSpan.Status().Code == codes.Unset)
	modelSpan.AssertAttrEquals("gen_ai.request.model", goldenModelID)

	// Verify tool span exists
	require.NotNil(t, toolSpan, "tool span not found")
	assert.True(t, toolSpan.Status().Code == codes.Ok || toolSpan.Status().Code == codes.Unset)
	toolSpan.AssertAttrEquals("tool.name", "calculator")
	assert.True(t, toolSpan.HasAttr("tool.input"))
	assert.True(t, toolSpan.HasAttr("tool.output"))

	// Verify the tool was called with correct operation
	input := toolSpan.Input()
	require.NotNil(t, input)
	inputMap, ok := input.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "multiply", inputMap["operation"])
	assert.Equal(t, float64(127), inputMap["a"])
	assert.Equal(t, float64(49), inputMap["b"])

	// Verify the tool output - it could be either the raw value or wrapped in a map
	output := toolSpan.Output()
	require.NotNil(t, output)
	// The output could be float64 directly or wrapped in a map
	switch v := output.(type) {
	case float64:
		assert.Equal(t, float64(6223), v)
	case map[string]any:
		assert.Equal(t, float64(6223), v["result"])
	default:
		t.Fatalf("unexpected output type: %T", output)
	}

	modelSpan.AssertChildOf(agentSpan)
	toolSpan.AssertChildOf(modelSpan)
	assert.False(t, modelSpan.Stub.EndTime.After(toolSpan.Stub.StartTime), "model duration must exclude tool execution")
}
