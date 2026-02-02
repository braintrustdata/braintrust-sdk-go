package adk

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// mockCallbackContext implements the minimal agent.CallbackContext interface needed for testing
type mockCallbackContext struct {
	context.Context
	agentName    string
	appName      string
	invocationID string
	sessionID    string
}

func (m *mockCallbackContext) AgentName() string                    { return m.agentName }
func (m *mockCallbackContext) AppName() string                      { return m.appName }
func (m *mockCallbackContext) InvocationID() string                 { return m.invocationID }
func (m *mockCallbackContext) SessionID() string                    { return m.sessionID }
func (m *mockCallbackContext) Agent() agent.Agent                   { return nil }
func (m *mockCallbackContext) Artifacts() agent.Artifacts           { return nil }
func (m *mockCallbackContext) Memory() agent.Memory                 { return nil }
func (m *mockCallbackContext) Session() session.Session             { return nil }
func (m *mockCallbackContext) Branch() string                       { return "" }
func (m *mockCallbackContext) UserContent() *genai.Content          { return nil }
func (m *mockCallbackContext) UserID() string                       { return "test-user" }
func (m *mockCallbackContext) ReadonlyState() session.ReadonlyState { return nil }
func (m *mockCallbackContext) State() session.State                 { return nil }

// mockToolContext implements the minimal tool.Context interface needed for testing
type mockToolContext struct {
	*mockCallbackContext
	funcCallID string
}

func (m *mockToolContext) FunctionCallID() string         { return m.funcCallID }
func (m *mockToolContext) Actions() *session.EventActions { return nil }
func (m *mockToolContext) SearchMemory(context.Context, string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (m *mockToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (m *mockToolContext) RequestConfirmation(string, any) error                { return nil }

// mockTool implements the minimal tool.Tool interface needed for testing
type mockTool struct {
	name        string
	description string
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return m.description }
func (m *mockTool) IsLongRunning() bool { return false }

// TestTracingCallbacks tests the ADK tracing callbacks to verify they create
// the expected spans with correct attributes and parent-child relationships.
func TestTracingCallbacks(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	otel.SetTracerProvider(tp)

	cb := NewCallbacks(WithTracerProvider(tp))
	callbacks, ok := cb.(*callbacksImpl)
	require.True(t, ok, "expected *callbacksImpl")

	// Create mock contexts
	sessionID := "test-session-123"
	agentCtx := &mockCallbackContext{
		Context:      context.Background(),
		agentName:    "test_agent",
		appName:      "test_app",
		invocationID: "agent-inv-1",
		sessionID:    sessionID,
	}
	toolCtx := &mockToolContext{
		mockCallbackContext: &mockCallbackContext{
			Context:      context.Background(),
			agentName:    "test_agent",
			appName:      "test_app",
			invocationID: "tool-inv-1",
			sessionID:    sessionID,
		},
		funcCallID: "func-call-123",
	}

	// Simulate an agent run with model and tool calls

	// 1. BeforeAgent - creates agent span
	_, err := callbacks.BeforeAgent(agentCtx)
	require.NoError(t, err)

	// 2. BeforeModel - creates model span as child of agent span
	modelReq := &model.LLMRequest{
		Model: "gemini-2.0-flash",
		Contents: []*genai.Content{
			{Parts: []*genai.Part{genai.NewPartFromText("Calculate 5 + 3")}},
		},
	}
	_, err = callbacks.BeforeModel(agentCtx, modelReq)
	require.NoError(t, err)

	// 3. BeforeTool - creates tool span as child of model span
	testTool := &mockTool{
		name:        "calculator",
		description: "Performs calculations",
	}
	// Ensure tool package is recognized as used
	var _ tool.Tool = testTool
	var _ tool.Context = toolCtx

	toolArgs := map[string]any{
		"operation": "add",
		"a":         float64(5),
		"b":         float64(3),
	}
	_, err = callbacks.BeforeTool(toolCtx, testTool, toolArgs)
	require.NoError(t, err)

	// 4. AfterTool - completes tool span
	toolResult := map[string]any{
		"result": float64(8),
	}
	_, err = callbacks.AfterTool(toolCtx, testTool, toolArgs, toolResult, nil)
	require.NoError(t, err)

	// 5. AfterModel - completes model span
	modelResp := &model.LLMResponse{
		Content: &genai.Content{
			Parts: []*genai.Part{genai.NewPartFromText("The result is 8")},
		},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:     10,
			CandidatesTokenCount: 5,
			TotalTokenCount:      15,
		},
		FinishReason: "STOP",
	}
	_, err = callbacks.AfterModel(agentCtx, modelResp, nil)
	require.NoError(t, err)

	// 6. AfterAgent - completes agent span and cleans up
	_, err = callbacks.AfterAgent(agentCtx)
	require.NoError(t, err)

	// Verify spans were created correctly
	spans := exporter.Flush()
	require.Len(t, spans, 3, "Expected 3 spans: agent_run, call_llm, and tool")

	// Find each span type
	var agentSpan, modelSpan, toolSpan *oteltest.Span
	for i := range spans {
		s := &spans[i]
		name := s.Name()
		switch name {
		case "agent_run [test_agent]":
			agentSpan = s
		case "call_llm":
			modelSpan = s
		case "tool [calculator]":
			toolSpan = s
		}
	}

	// Verify all spans were found
	require.NotNil(t, agentSpan, "agent_run span not found")
	require.NotNil(t, modelSpan, "call_llm span not found")
	require.NotNil(t, toolSpan, "tool span not found")

	// Verify agent span
	assert.Equal(t, codes.Ok, agentSpan.Status().Code)
	assert.Equal(t, trace.SpanKindInternal, agentSpan.Stub.SpanKind)
	agentSpan.AssertAttrEquals("adk.agent.name", "test_agent")
	agentSpan.AssertAttrEquals("adk.agent.invocation_id", "agent-inv-1")
	agentSpan.AssertAttrEquals("adk.agent.session_id", sessionID)
	agentSpan.AssertAttrEquals("adk.agent.branch", "")

	// Verify model span
	assert.Equal(t, codes.Ok, modelSpan.Status().Code)
	assert.Equal(t, trace.SpanKindClient, modelSpan.Stub.SpanKind)
	modelSpan.AssertAttrEquals("gen_ai.request.model", "gemini-2.0-flash")
	assert.True(t, modelSpan.HasAttr("gen_ai.prompt"))
	assert.True(t, modelSpan.HasAttr("gen_ai.completion"))
	modelSpan.AssertAttrEquals("gen_ai.usage.prompt_tokens", int64(10))
	modelSpan.AssertAttrEquals("gen_ai.usage.completion_tokens", int64(5))
	modelSpan.AssertAttrEquals("gen_ai.usage.total_tokens", int64(15))
	modelSpan.AssertAttrEquals("gen_ai.finish_reason", "STOP")

	// Verify tool span
	assert.Equal(t, codes.Ok, toolSpan.Status().Code)
	assert.Equal(t, trace.SpanKindInternal, toolSpan.Stub.SpanKind)
	toolSpan.AssertAttrEquals("tool.name", "calculator")
	toolSpan.AssertAttrEquals("tool.description", "Performs calculations")
	assert.True(t, toolSpan.HasAttr("tool.input"))
	assert.True(t, toolSpan.HasAttr("tool.output"))

	// Verify braintrust-specific attributes on tool span
	assert.True(t, toolSpan.HasAttr("braintrust.span_attributes"))
	assert.True(t, toolSpan.HasAttr("braintrust.input_json"))
	assert.True(t, toolSpan.HasAttr("braintrust.output_json"))

	// Verify input/output values
	input := toolSpan.Input()
	require.NotNil(t, input)
	inputMap, ok := input.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "add", inputMap["operation"])
	assert.Equal(t, float64(5), inputMap["a"])
	assert.Equal(t, float64(3), inputMap["b"])

	output := toolSpan.Output()
	require.NotNil(t, output)
	outputMap, ok := output.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(8), outputMap["result"])

	// Verify span parent-child relationships
	// Model span should be a child of agent span
	assert.Equal(t, agentSpan.Stub.SpanContext.SpanID(), modelSpan.Stub.Parent.SpanID(),
		"model span should be child of agent span")

	// Tool span should be a child of model span
	assert.Equal(t, modelSpan.Stub.SpanContext.SpanID(), toolSpan.Stub.Parent.SpanID(),
		"tool span should be child of model span")
}
