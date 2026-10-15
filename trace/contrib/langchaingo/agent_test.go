package langchaingo

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms/openai"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/tools"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

func TestAgentCallbacksParentContext(t *testing.T) {
	// The executor VCR test covers chain parenting and event payloads. These
	// cases cover the empty-stack fallback and selecting a child above a chain.
	for _, parent := range []string{"ambient", "tool"} {
		t.Run(parent, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})
			ctx, root := tp.Tracer(t.Name()).Start(context.Background(), "test-parent")
			expectedParent := root.SpanContext()
			if parent == "tool" {
				handler.HandleChainStart(ctx, map[string]any{"input": "calculate"})
				handler.HandleToolStart(ctx, "25 * 4")
				stack := handler.spans[ctx]
				expectedParent = stack[len(stack)-1].span.SpanContext()
			}
			stackBefore := append([]spanEntry(nil), handler.spans[ctx]...)
			handler.HandleAgentAction(ctx, schema.AgentAction{Tool: "calculator", ToolInput: "25 * 4", Log: "calculate"})
			handler.HandleAgentFinish(ctx, schema.AgentFinish{ReturnValues: map[string]any{"output": "100"}, Log: "done"})

			// Both events end immediately and leave the active span stack intact.
			spans := exporter.Flush()
			require.Len(t, spans, 2)
			for _, span := range spans {
				assert.Equal(t, expectedParent.SpanID(), span.Stub.Parent.SpanID(), span.Name())
				assert.Equal(t, expectedParent.TraceID(), span.Stub.SpanContext.TraceID(), span.Name())
			}
			spans[0].AssertNameIs("langchain.agent.action")
			spans[1].AssertNameIs("langchain.agent.finish")
			assert.Equal(t, stackBefore, handler.spans[ctx])

			if parent == "tool" {
				handler.HandleToolEnd(ctx, "100")
				handler.HandleChainEnd(ctx, map[string]any{"output": "100"})
			}
			assert.Empty(t, handler.spans)
			root.End()
		})
	}
}

func TestAgentExecutorSpanHierarchy(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp, Model: "gpt-4o-mini", Provider: "openai"})
	apiKey := "dummy-openai-key-for-replay"
	if vcr.GetVCRMode() != vcr.ModeReplay {
		apiKey = os.Getenv("OPENAI_API_KEY")
		require.NotEmpty(t, apiKey, "OPENAI_API_KEY required in record/off mode")
	}
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithModel("gpt-4o-mini"),
		openai.WithHTTPClient(vcr.NewHTTPClient(t)),
		openai.WithCallback(handler),
	)
	require.NoError(t, err)
	agent := agents.NewOneShotAgent(model, []tools.Tool{tools.Calculator{CallbacksHandler: handler}}, agents.WithCallbacksHandler(handler))
	executor := agents.NewExecutor(agent, agents.WithCallbacksHandler(handler), agents.WithMaxIterations(3), agents.WithReturnIntermediateSteps())
	ctx, root := tp.Tracer(t.Name()).Start(context.Background(), "test-parent")
	defer root.End()
	result, err := chains.Call(ctx, executor, map[string]any{
		"input": "Use the calculator tool to compute 25 * 4. After observing the result, give only the number as your final answer.",
	})
	require.NoError(t, err)
	output, ok := result["output"].(string)
	require.True(t, ok)
	assert.Equal(t, "100", strings.TrimSpace(output))
	steps, ok := result["intermediateSteps"].([]schema.AgentStep)
	require.True(t, ok)
	require.Len(t, steps, 1)
	assert.Empty(t, handler.spans)

	spans := exporter.Flush()
	byName := make(map[string][]oteltest.Span)
	for _, span := range spans {
		byName[span.Name()] = append(byName[span.Name()], span)
		assert.Equal(t, root.SpanContext().TraceID(), span.Stub.SpanContext.TraceID(), span.Name())
	}
	require.Len(t, byName["langchain.chain"], 3)
	require.Len(t, byName["langchain.llm.generate_content"], 2)
	require.Len(t, byName["langchain.tool"], 1)
	require.Len(t, byName["langchain.agent.action"], 1)
	require.Len(t, byName["langchain.agent.finish"], 1)

	// The executor chain encloses two planning chains and the agent events/tool.
	var executorSpan oteltest.Span
	for _, span := range byName["langchain.chain"] {
		if span.Stub.Parent.SpanID() == root.SpanContext().SpanID() {
			executorSpan = span
		}
	}
	require.NotEmpty(t, executorSpan.Stub.Name, "executor chain missing")
	for _, name := range []string{"langchain.agent.action", "langchain.agent.finish", "langchain.tool"} {
		byName[name][0].AssertChildOf(&executorSpan)
	}
	for _, llmSpan := range byName["langchain.llm.generate_content"] {
		var planningSpan oteltest.Span
		for _, chainSpan := range byName["langchain.chain"] {
			if chainSpan.Stub.SpanContext.SpanID() == llmSpan.Stub.Parent.SpanID() {
				planningSpan = chainSpan
			}
		}
		require.NotEmpty(t, planningSpan.Stub.Name, "LLM planning chain missing")
		llmSpan.AssertChildOf(&planningSpan)
		planningSpan.AssertChildOf(&executorSpan)
	}
	assert.Equal(t, map[string]any{
		"tool": steps[0].Action.Tool, "tool_input": steps[0].Action.ToolInput, "log": steps[0].Action.Log,
	}, byName["langchain.agent.action"][0].Input())
	finish, ok := byName["langchain.agent.finish"][0].Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"output": output}, finish["return_values"])
	assert.Contains(t, finish["log"], "Final Answer:")
	assert.Equal(t, steps[0].Action.ToolInput, byName["langchain.tool"][0].Input())
	assert.Equal(t, "100", byName["langchain.tool"][0].Output())
}
