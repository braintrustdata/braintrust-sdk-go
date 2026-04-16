package eino

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	einoembed "github.com/cloudwego/eino-ext/components/embedding/openai"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

const testModel = "gpt-4o-mini"

func setUpTest(t *testing.T) (*einoopenai.ChatModel, *Handler, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-openai-key-for-replay"
	}

	httpClient := vcr.NewHTTPClient(t)

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	m, err := einoopenai.NewChatModel(context.Background(), &einoopenai.ChatModelConfig{
		Model:      testModel,
		APIKey:     apiKey,
		HTTPClient: httpClient,
	})
	require.NoError(t, err)

	return m, handler, exporter
}

// ctxWithHandler creates a context with the handler registered for ChatModel callbacks.
func ctxWithHandler(handler *Handler) context.Context {
	return callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{
		Component: components.ComponentOfChatModel,
	}, handler)
}

func TestIntegration_NonStreaming(t *testing.T) {
	m, handler, exporter := setUpTest(t)

	ctx := ctxWithHandler(handler)
	resp, err := m.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "What is 2+2? Answer with just the number."},
	})
	require.NoError(t, err)
	handler.Wait()

	assert.Contains(t, resp.Content, "4")

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	inp := span.Input()
	require.NotNil(t, inp)
	inputJSON, _ := json.Marshal(inp)
	assert.Contains(t, string(inputJSON), "2+2")

	out := span.Output()
	require.NotNil(t, out)
	outMap, ok := out.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", outMap["role"])
	assert.Contains(t, outMap["content"], "4")

	metrics := span.Metrics()
	require.NotNil(t, metrics)
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
	assert.Greater(t, metrics["tokens"], float64(0))

	meta := span.Metadata()
	require.NotNil(t, meta)
	assert.Equal(t, testModel, meta["model"])
}

func TestIntegration_Streaming(t *testing.T) {
	m, handler, exporter := setUpTest(t)

	ctx := ctxWithHandler(handler)
	reader, err := m.Stream(ctx, []*schema.Message{
		{Role: schema.User, Content: "Count from 1 to 3, one number per word."},
	})
	require.NoError(t, err)

	var content string
	for {
		chunk, err := reader.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		content += chunk.Content
	}
	reader.Close()
	handler.Wait()

	assert.NotEmpty(t, content)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	out := span.Output()
	require.NotNil(t, out)
	outMap, ok := out.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", outMap["role"])

	metrics := span.Metrics()
	require.NotNil(t, metrics)
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
}

func TestIntegration_ToolCalling(t *testing.T) {
	m, handler, exporter := setUpTest(t)

	ctx := ctxWithHandler(handler)

	// Define a tool
	type multiplyInput struct {
		A float64 `json:"a" jsonschema_description:"First number"`
		B float64 `json:"b" jsonschema_description:"Second number"`
	}
	multiplyTool, err := utils.InferTool("multiply", "Multiply two numbers together",
		func(_ context.Context, input multiplyInput) (string, error) {
			result := input.A * input.B
			out, _ := json.Marshal(map[string]float64{"result": result})
			return string(out), nil
		})
	require.NoError(t, err)

	toolInfo, err := multiplyTool.Info(ctx)
	require.NoError(t, err)
	require.NoError(t, m.BindTools([]*schema.ToolInfo{toolInfo}))

	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: []tool.BaseTool{multiplyTool},
	})
	require.NoError(t, err)

	// First turn: model decides to call the tool
	firstResp, err := m.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "What is 6 multiplied by 7? You must use the multiply tool."},
	})
	require.NoError(t, err)
	handler.Wait()

	require.NotEmpty(t, firstResp.ToolCalls, "model should have made a tool call")

	// Verify the LLM span has tool_calls in output
	llmSpans := exporter.Flush()
	require.Len(t, llmSpans, 1)
	llmOut := llmSpans[0].Output()
	require.NotNil(t, llmOut)
	llmOutJSON, _ := json.Marshal(llmOut)
	assert.Contains(t, string(llmOutJSON), "tool_calls")
	assert.Contains(t, string(llmOutJSON), "multiply")

	// Second turn: execute tool via ToolsNode
	toolResults, err := toolsNode.Invoke(ctx, firstResp)
	require.NoError(t, err)
	handler.Wait()

	// Verify tool span was created
	toolSpans := exporter.Flush()
	require.Len(t, toolSpans, 1)
	toolSpan := toolSpans[0]

	toolOut := toolSpan.Output()
	require.NotNil(t, toolOut)
	toolOutMap, ok := toolOut.(map[string]any)
	require.True(t, ok, "tool output should be parsed JSON, got %T", toolOut)
	assert.Equal(t, float64(42), toolOutMap["result"])

	// Verify tool span type via raw attribute
	var spanAttrsStr string
	for _, a := range toolSpan.Stub.Attributes {
		if string(a.Key) == "braintrust.span_attributes" {
			spanAttrsStr = a.Value.AsString()
		}
	}
	assert.Contains(t, spanAttrsStr, `"type":"tool"`)

	// Third turn: model incorporates tool results
	messages := []*schema.Message{
		{Role: schema.User, Content: "What is 6 multiplied by 7? You must use the multiply tool."},
		firstResp,
	}
	messages = append(messages, toolResults...)
	finalResp, err := m.Generate(ctx, messages)
	require.NoError(t, err)
	handler.Wait()

	assert.Contains(t, finalResp.Content, "42")

	finalSpans := exporter.Flush()
	require.Len(t, finalSpans, 1)
	finalMetrics := finalSpans[0].Metrics()
	require.NotNil(t, finalMetrics)
	assert.Greater(t, finalMetrics["tokens"], float64(0))
}

// --- Embedding integration tests ---

func setUpEmbedderTest(t *testing.T) (*einoembed.Embedder, *Handler, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-openai-key-for-replay"
	}

	httpClient := vcr.NewHTTPClient(t)

	handler := NewHandlerWithOptions(HandlerOptions{TracerProvider: tp})

	e, err := einoembed.NewEmbedder(context.Background(), &einoembed.EmbeddingConfig{
		Model:      "text-embedding-3-small",
		APIKey:     apiKey,
		HTTPClient: httpClient,
	})
	require.NoError(t, err)

	return e, handler, exporter
}

// ctxWithEmbedderHandler registers the handler for embedding callbacks.
func ctxWithEmbedderHandler(handler *Handler) context.Context {
	return callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{
		Component: components.ComponentOfEmbedding,
	}, handler)
}

func TestIntegration_EmbedStrings(t *testing.T) {
	e, handler, exporter := setUpEmbedderTest(t)

	ctx := ctxWithEmbedderHandler(handler)
	vectors, err := e.EmbedStrings(ctx, []string{
		"The quick brown fox jumps over the lazy dog",
		"braintrust tracing",
	})
	require.NoError(t, err)
	handler.Wait()

	require.Len(t, vectors, 2)
	assert.Greater(t, len(vectors[0]), 0)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	span := spans[0]

	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	inp := span.Input()
	arr, ok := inp.([]interface{})
	require.True(t, ok)
	require.Len(t, arr, 2)
	assert.Equal(t, "The quick brown fox jumps over the lazy dog", arr[0])

	out := span.Output()
	outMap, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(len(vectors[0])), outMap["embedding_length"])
	assert.Equal(t, float64(2), outMap["embeddings_count"])

	metadata := span.Metadata()
	assert.Equal(t, "text-embedding-3-small", metadata["model"])
}
