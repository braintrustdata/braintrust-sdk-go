package eino

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

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
	return setUpTestWithModel(t, testModel)
}

func setUpTestWithModel(t *testing.T, modelName string) (*einoopenai.ChatModel, *Handler, *oteltest.Exporter) {
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
		Model:      modelName,
		APIKey:     apiKey,
		HTTPClient: httpClient,
	})
	require.NoError(t, err)

	return m, handler, exporter
}

// ctxWithHandler creates a context with the handler registered for ChatModel callbacks.
func ctxWithHandler(handler *Handler) context.Context {
	return callbacks.InitCallbacks(context.Background(), &callbacks.RunInfo{
		Type:      "OpenAI",
		Component: components.ComponentOfChatModel,
	}, handler)
}

func TestIntegration_NonStreaming(t *testing.T) {
	m, handler, exporter := setUpTest(t)

	graph := compose.NewGraph[[]*schema.Message, *schema.Message]()
	require.NoError(t, graph.AddChatModelNode("model", m))
	require.NoError(t, graph.AddEdge(compose.START, "model"))
	require.NoError(t, graph.AddEdge("model", compose.END))
	runner, err := graph.Compile(context.Background())
	require.NoError(t, err)

	resp, err := runner.Invoke(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "What is 2+2? Answer with just the number."},
	}, compose.WithCallbacks(handler))
	require.NoError(t, err)
	handler.Wait()

	assert.Contains(t, resp.Content, "4")

	spans := exporter.Flush()
	require.Len(t, spans, 2)
	llmSpan, taskSpan := spans[0], spans[1]
	llmSpan.AssertChildOf(&taskSpan)
	taskSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"name": "Graph",
		"type": "task",
	})

	inp := llmSpan.Input()
	require.NotNil(t, inp)
	inputJSON, _ := json.Marshal(inp)
	assert.Contains(t, string(inputJSON), "2+2")

	out := llmSpan.Output()
	choices, ok := out.([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), choice["index"])
	assert.Equal(t, "stop", choice["finish_reason"])
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", message["role"])
	assert.Contains(t, message["content"], "4")

	metrics := llmSpan.Metrics()
	require.NotNil(t, metrics)
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
	assert.Greater(t, metrics["tokens"], float64(0))

	meta := llmSpan.Metadata()
	require.NotNil(t, meta)
	assert.Equal(t, testModel, meta["model"])
	assert.Equal(t, "openai", meta["provider"])
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
	choices, ok := out.([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok)
	message, ok := choice["message"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "assistant", message["role"])

	metrics := span.Metrics()
	require.NotNil(t, metrics)
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
}

func TestIntegration_StreamingEarlyClose(t *testing.T) {
	m, handler, exporter := setUpTest(t)

	reader, err := m.Stream(ctxWithHandler(handler), []*schema.Message{{
		Role:    schema.User,
		Content: "Write twenty words about observability.",
	}})
	require.NoError(t, err)

	chunk, err := reader.Recv()
	require.NoError(t, err)
	assert.NotEmpty(t, chunk.Content)
	reader.Close()
	handler.Wait()

	span := exporter.FlushOne()
	assert.NotNil(t, span.Output())
	assert.Greater(t, span.Metrics()["time_to_first_token"], float64(0))
}

func TestIntegration_Multimodal(t *testing.T) {
	m, handler, exporter := setUpTest(t)
	imageData := redPNGBase64(t)

	resp, err := m.Generate(ctxWithHandler(handler), []*schema.Message{{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "What is the dominant color? Answer with one word."},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{
					Base64Data: &imageData,
					MIMEType:   "image/png",
				}},
			},
		},
	}})
	require.NoError(t, err)
	handler.Wait()
	assert.NotEmpty(t, resp.Content)

	span := exporter.FlushOne()
	messages, ok := span.Input().([]any)
	require.True(t, ok)
	content, ok := messages[0].(map[string]any)["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 2)
	imagePart := content[1].(map[string]any)
	imageURL := imagePart["image_url"].(map[string]any)
	assert.Equal(t, "data:image/png;base64,"+imageData, imageURL["url"])
	assert.NotNil(t, span.Output())
}

func TestIntegration_APIError(t *testing.T) {
	m, handler, exporter := setUpTestWithModel(t, "braintrust-invalid-model")

	_, err := m.Generate(ctxWithHandler(handler), []*schema.Message{{
		Role:    schema.User,
		Content: "This request should fail.",
	}})
	require.Error(t, err)
	handler.Wait()

	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.NotEmpty(t, span.Events())
}

func redPNGBase64(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestIntegration_ToolCalling(t *testing.T) {
	m, handler, exporter := setUpTest(t)
	ctx := context.Background()

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

	agentTurn := compose.InvokableLambda(func(ctx context.Context, messages []*schema.Message) (*schema.Message, error) {
		firstResp, err := m.Generate(ctx, messages)
		if err != nil {
			return nil, err
		}
		if len(firstResp.ToolCalls) == 0 {
			return firstResp, nil
		}

		toolResults, err := toolsNode.Invoke(ctx, firstResp)
		if err != nil {
			return nil, err
		}
		followUp := append(append([]*schema.Message{}, messages...), firstResp)
		followUp = append(followUp, toolResults...)
		return m.Generate(ctx, followUp)
	})

	graph := compose.NewGraph[[]*schema.Message, *schema.Message]()
	require.NoError(t, graph.AddLambdaNode("agent_turn", agentTurn))
	require.NoError(t, graph.AddEdge(compose.START, "agent_turn"))
	require.NoError(t, graph.AddEdge("agent_turn", compose.END))
	runner, err := graph.Compile(ctx)
	require.NoError(t, err)

	finalResp, err := runner.Invoke(ctx, []*schema.Message{
		{Role: schema.User, Content: "What is 6 multiplied by 7? You must use the multiply tool."},
	}, compose.WithCallbacks(handler))
	require.NoError(t, err)
	handler.Wait()
	assert.Contains(t, finalResp.Content, "42")

	spans := exporter.Flush()
	require.Len(t, spans, 4)
	firstLLM, toolSpan, finalLLM, taskSpan := spans[0], spans[1], spans[2], spans[3]
	firstLLM.AssertChildOf(&taskSpan)
	toolSpan.AssertChildOf(&taskSpan)
	finalLLM.AssertChildOf(&taskSpan)
	taskSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"name": "Graph",
		"type": "task",
	})

	llmOutJSON, err := json.Marshal(firstLLM.Output())
	require.NoError(t, err)
	assert.Contains(t, string(llmOutJSON), `"finish_reason":"tool_calls"`)
	assert.Contains(t, string(llmOutJSON), "multiply")

	llmMetadata := firstLLM.Metadata()
	tools, ok := llmMetadata["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	toolJSON, err := json.Marshal(tools[0])
	require.NoError(t, err)
	assert.Contains(t, string(toolJSON), `"name":"multiply"`)
	assert.Contains(t, string(toolJSON), `"parameters":{"`)

	toolSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"name": "multiply",
		"type": "tool",
	})
	toolOutMap, ok := toolSpan.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(42), toolOutMap["result"])

	finalMetrics := finalLLM.Metrics()
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
		Type:      "OpenAI",
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
	inputMap, ok := inp.(map[string]any)
	require.True(t, ok)
	inputs, ok := inputMap["inputs"].([]any)
	require.True(t, ok)
	require.Len(t, inputs, 2)
	firstInput, ok := inputs[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "The quick brown fox jumps over the lazy dog", firstInput["content"])

	out := span.Output()
	outMap, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, float64(2), outMap["count"])
	assert.NotContains(t, outMap, "embedding_length")

	metadata := span.Metadata()
	assert.Equal(t, "text-embedding-3-small", metadata["model"])
	assert.Equal(t, "openai", metadata["provider"])
	assert.NotContains(t, metadata, "encoding_format")
}
