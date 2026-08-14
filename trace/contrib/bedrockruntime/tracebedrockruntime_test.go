package bedrockruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// Pinned model + region for recorded cassettes. Both record and replay must
// hit the same regional endpoint because go-vcr's default matcher is
// method+URL, which includes the region host.
const (
	testModelID = "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	testRegion  = "us-east-2"
)

// TestParseUsageTokens verifies token normalization for the typed Converse usage.
func TestParseUsageTokens(t *testing.T) {
	t.Run("nil usage returns empty", func(t *testing.T) {
		m := parseUsageTokens(nil)
		assert.Empty(t, m)
	})

	t.Run("input/output/total from converse", func(t *testing.T) {
		u := &types.TokenUsage{
			InputTokens:  aws.Int32(10),
			OutputTokens: aws.Int32(20),
			TotalTokens:  aws.Int32(30),
		}
		m := parseUsageTokens(u)
		assert.Equal(t, int64(10), m["prompt_tokens"])
		assert.Equal(t, int64(20), m["completion_tokens"])
		assert.Equal(t, int64(30), m["tokens"])
		assert.NotContains(t, m, "prompt_cached_tokens")
		assert.NotContains(t, m, "prompt_cache_creation_tokens")
	})

	t.Run("cache tokens included in prompt total", func(t *testing.T) {
		u := &types.TokenUsage{
			InputTokens:           aws.Int32(5),
			OutputTokens:          aws.Int32(7),
			CacheReadInputTokens:  aws.Int32(100),
			CacheWriteInputTokens: aws.Int32(11),
			TotalTokens:           aws.Int32(123),
		}
		m := parseUsageTokens(u)
		assert.Equal(t, int64(116), m["prompt_tokens"], "prompt_tokens = input + cacheRead + cacheWrite")
		assert.Equal(t, int64(7), m["completion_tokens"])
		assert.Equal(t, int64(100), m["prompt_cached_tokens"])
		assert.Equal(t, int64(11), m["prompt_cache_creation_tokens"])
		assert.Equal(t, int64(123), m["tokens"])
	})

	t.Run("total falls back to prompt+completion when missing", func(t *testing.T) {
		u := &types.TokenUsage{
			InputTokens:  aws.Int32(4),
			OutputTokens: aws.Int32(6),
		}
		m := parseUsageTokens(u)
		assert.Equal(t, int64(10), m["tokens"])
	})

	t.Run("missing usage is omitted rather than fabricated as zero", func(t *testing.T) {
		m := parseUsageTokens(&types.TokenUsage{OutputTokens: aws.Int32(6)})
		assert.NotContains(t, m, "prompt_tokens")
		assert.NotContains(t, m, "tokens")
		assert.Equal(t, int64(6), m["completion_tokens"])
	})

	t.Run("cache write TTL details are normalized", func(t *testing.T) {
		m := parseUsageTokens(&types.TokenUsage{
			InputTokens: aws.Int32(20),
			CacheDetails: []types.CacheDetail{
				{Ttl: types.CacheTTLFiveMinutes, InputTokens: aws.Int32(5)},
				{Ttl: types.CacheTTLOneHour, InputTokens: aws.Int32(15)},
			},
		})
		assert.Equal(t, int64(5), m["prompt_cache_creation_5m_tokens"])
		assert.Equal(t, int64(15), m["prompt_cache_creation_1h_tokens"])
	})

	t.Run("negative provider usage is omitted", func(t *testing.T) {
		m := parseUsageTokens(&types.TokenUsage{
			InputTokens:  aws.Int32(-1),
			OutputTokens: aws.Int32(-2),
			TotalTokens:  aws.Int32(-3),
		})
		assert.Empty(t, m)
	})
}

// TestExtractUsageForModel verifies InvokeModel's Claude-only token branch.
func TestExtractUsageForModel(t *testing.T) {
	claudeBody := map[string]any{
		"usage": map[string]any{
			"input_tokens":                float64(7),
			"output_tokens":               float64(3),
			"cache_read_input_tokens":     float64(2),
			"cache_creation_input_tokens": float64(1),
		},
	}

	t.Run("claude normalizes tokens", func(t *testing.T) {
		m := extractUsageForModel("anthropic.claude-3-haiku-20240307-v1:0", claudeBody)
		require.NotNil(t, m)
		assert.Equal(t, int64(10), m["prompt_tokens"], "input + cache_read + cache_create")
		assert.Equal(t, int64(3), m["completion_tokens"])
		assert.Equal(t, int64(13), m["tokens"])
		assert.Equal(t, int64(2), m["prompt_cached_tokens"])
		assert.Equal(t, int64(1), m["prompt_cache_creation_tokens"])
	})

	t.Run("non-claude model returns nil", func(t *testing.T) {
		m := extractUsageForModel("amazon.titan-text-express-v1", claudeBody)
		assert.Nil(t, m)
	})

	t.Run("nil body returns nil", func(t *testing.T) {
		m := extractUsageForModel("anthropic.claude-3-haiku-20240307-v1:0", nil)
		assert.Nil(t, m)
	})
}

// setUpTest builds a Bedrock client wired to the VCR cassette for the current
// test. In replay mode (the default), dummy creds are injected so no live AWS
// access is required. In record or off mode, real AWS credentials from the
// environment are used.
func setUpTest(t *testing.T) (*bedrockruntime.Client, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()
	httpClient := vcr.NewHTTPClient(t)

	opts := []func(*config.LoadOptions) error{
		config.WithRegion(testRegion),
		config.WithHTTPClient(httpClient),
	}
	switch mode {
	case vcr.ModeReplay:
		// Dummy creds: the cassette supplies the canned response regardless of
		// what SigV4 header AWS SDK builds, because go-vcr's default matcher is
		// method+URL only.
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("AKIAFAKE", "fake", ""),
		))
	default:
		if os.Getenv("AWS_ACCESS_KEY_ID") == "" {
			t.Fatal("AWS_ACCESS_KEY_ID not set (required in record / off mode)")
		}
	}

	cfg, err := config.LoadDefaultConfig(context.Background(), opts...)
	require.NoError(t, err)

	client := bedrockruntime.NewFromConfig(cfg, NewMiddleware(WithTracerProvider(tp)))
	return client, exporter
}

// assertSpanValid checks the common properties of a Bedrock span.
func assertSpanValid(t *testing.T, span oteltest.Span, timeRange oteltest.TimeRange, wantName string, streaming bool) {
	t.Helper()
	a := assert.New(t)

	span.AssertInTimeRange(timeRange)
	span.AssertNameIs(wantName)
	a.Equal(codes.Unset, span.Stub.Status.Code)

	metadata := span.Metadata()
	a.Equal("bedrock", metadata["provider"])

	metrics := span.Metrics()
	required := []string{"prompt_tokens", "completion_tokens", "tokens"}
	for _, m := range required {
		a.Contains(metrics, m, "missing metric %s", m)
		a.Greater(metrics[m], float64(0), "metric %s should be > 0", m)
	}
	if streaming {
		a.Greater(metrics["time_to_first_token"], float64(0))
	} else {
		a.NotContains(metrics, "time_to_first_token")
	}
}

func TestConverse(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	out, err := client.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "What is the capital of France? Reply in one word."},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:     aws.Int32(64),
			Temperature:   aws.Float32(0.2),
			StopSequences: []string{"DONE"},
		},
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotNil(t, out)
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	require.True(t, ok)
	require.NotEmpty(t, msg.Value.Content)

	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange, "bedrock.converse", false)

	metadata := span.Metadata()
	assert.Equal(t, "converse", metadata["endpoint"])
	assert.Equal(t, testModelID, metadata["model"])
	assert.Equal(t, float64(64), metadata["max_tokens"])
	assert.Equal(t, 0.2, metadata["temperature"])
	assert.Equal(t, []any{"DONE"}, metadata["stop"])
	assert.NotContains(t, metadata, "stop_sequences")
	assert.Equal(t, "end_turn", metadata["stop_reason"])

	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, "capital of France")
	assert.Contains(t, input, `"role":"user"`)

	output := span.Attr("braintrust.output_json").String()
	assert.Contains(t, output, "Paris")
	assert.Contains(t, output, `"role":"assistant"`)
}

func TestConverseWithTools(t *testing.T) {
	client, exporter := setUpTest(t)

	toolSchema := document.NewLazyDocument(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city": map[string]any{"type": "string"},
		},
		"required": []string{"city"},
	})

	timer := oteltest.NewTimer()
	out, err := client.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "What's the weather in Paris? Call the tool."},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(256)},
		ToolConfig: &types.ToolConfiguration{
			Tools: []types.Tool{
				&types.ToolMemberToolSpec{Value: types.ToolSpecification{
					Name:        aws.String("get_weather"),
					Description: aws.String("Get weather for a city"),
					InputSchema: &types.ToolInputSchemaMemberJson{Value: toolSchema},
				}},
			},
			ToolChoice: &types.ToolChoiceMemberAuto{},
		},
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, types.StopReasonToolUse, out.StopReason)

	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange, "bedrock.converse", false)

	metadata := span.Metadata()
	assert.Equal(t, "tool_use", metadata["stop_reason"])
	tools, ok := metadata["tools"].([]any)
	require.True(t, ok, "tools should be a list in metadata")
	require.Len(t, tools, 1)
	assert.Equal(t, map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "get_weather",
			"description": "Get weather for a city",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": []any{"city"},
			},
		},
	}, tools[0])
	assert.Equal(t, "auto", metadata["tool_choice"])

	output := span.Attr("braintrust.output_json").String()
	assert.Contains(t, output, "tool_use")
	assert.Contains(t, output, "get_weather")
	assert.Contains(t, output, "Paris")
}

func TestConverseStream(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	out, err := client.ConverseStream(context.Background(), &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "What is the capital of France? Reply in one word."},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(64),
			Temperature: aws.Float32(0.2),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	var gotText strings.Builder
	for ev := range out.GetStream().Events() {
		if d, ok := ev.(*types.ConverseStreamOutputMemberContentBlockDelta); ok {
			if txt, ok := d.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				gotText.WriteString(txt.Value)
			}
		}
	}
	require.NoError(t, out.GetStream().Close())
	timeRange := timer.Tick()

	assert.Contains(t, gotText.String(), "Paris")

	span := exporter.FlushOne()
	assertSpanValid(t, span, timeRange, "bedrock.converse-stream", true)

	metadata := span.Metadata()
	assert.Equal(t, "converse-stream", metadata["endpoint"])
	assert.Equal(t, true, metadata["stream"])
	assert.Equal(t, "end_turn", metadata["stop_reason"])

	output := span.Attr("braintrust.output_json").String()
	assert.Contains(t, output, "Paris")
	assert.Contains(t, output, `"role":"assistant"`)
}

func TestConverseWithImage(t *testing.T) {
	// Bedrock's image validation rejects sub-pixel-scale PNGs. A small solid
	// square is enough to pass validation and exercise the image code path.
	imgBytes := makeRedPNG(t, 64, 64)

	client, exporter := setUpTest(t)

	_, err := client.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "What color is this image? Reply in one word."},
				&types.ContentBlockMemberImage{Value: types.ImageBlock{
					Format: types.ImageFormatPng,
					Source: &types.ImageSourceMemberBytes{Value: imgBytes},
				}},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(64)},
	})
	require.NoError(t, err)

	span := exporter.FlushOne()
	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, `"type":"image"`)
	assert.Contains(t, input, `"image":{"format":"png"`)
	assert.Contains(t, input, `"source":{"bytes":"iVBORw0KGgo`)
	assert.NotContains(t, input, `"type":"base64"`)
}

func TestConverseWithDocumentCitations(t *testing.T) {
	client, exporter := setUpTest(t)

	_, err := client.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberDocument{Value: types.DocumentBlock{
					Name:   aws.String("france-facts"),
					Format: types.DocumentFormatTxt,
					Source: &types.DocumentSourceMemberText{
						Value: "France's capital is Paris.",
					},
					Citations: &types.CitationsConfig{Enabled: aws.Bool(true)},
				}},
				&types.ContentBlockMemberText{
					Value: "Using only the provided document, what is France's capital?",
				},
			},
		}},
		InferenceConfig: &types.InferenceConfiguration{MaxTokens: aws.Int32(128)},
	})
	require.NoError(t, err)

	span := exporter.FlushOne()
	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, `"type":"document"`)
	assert.Contains(t, input, `"format":"txt"`)
	assert.Contains(t, input, `"name":"france-facts"`)
	assert.Contains(t, input, `"citations_enabled":true`)
	assert.Contains(t, input, "France's capital is Paris.")
}

// makeRedPNG builds a solid-red PNG for the vision test.
func makeRedPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	red := color.RGBA{R: 220, G: 20, B: 60, A: 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, red)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestConverseReasoningOutput(t *testing.T) {
	client, exporter := setUpTest(t)

	_, err := client.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "What is 27 * 453?"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(4000),
			Temperature: aws.Float32(1.0),
		},
		AdditionalModelRequestFields: document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{"type": "enabled", "budget_tokens": 2000},
		}),
	})
	require.NoError(t, err)

	span := exporter.FlushOne()
	assert.NotContains(t, span.Metadata(), "additional_model_request_fields")
	output := span.Attr("braintrust.output_json").String()
	// Claude with thinking enabled returns a reasoningContent block and a text
	// answer. The model may format 12231 with or without a thousands separator,
	// so check for the distinctive "231" tail.
	assert.Contains(t, output, `"type":"reasoning"`)
	assert.Contains(t, output, `"type":"text"`)
	assert.Regexp(t, `12[,]?231`, output)
}

func TestConverseStreamReasoning(t *testing.T) {
	client, exporter := setUpTest(t)

	out, err := client.ConverseStream(context.Background(), &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String(testModelID),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "What is 27 * 453?"}},
		}},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens:   aws.Int32(4000),
			Temperature: aws.Float32(1.0),
		},
		AdditionalModelRequestFields: document.NewLazyDocument(map[string]any{
			"thinking": map[string]any{"type": "enabled", "budget_tokens": 2000},
		}),
	})
	require.NoError(t, err)

	for range out.GetStream().Events() { //nolint:revive
		// drain
	}
	require.NoError(t, out.GetStream().Close())

	span := exporter.FlushOne()
	output := span.Attr("braintrust.output_json").String()
	assert.Contains(t, output, `"type":"reasoning"`)
	assert.Contains(t, output, `"type":"text"`)
	assert.Regexp(t, `12[,]?231`, output)
}

func TestInvokeModelClaude(t *testing.T) {
	client, exporter := setUpTest(t)

	reqBody, err := json.Marshal(map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        50,
		"messages": []any{
			map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "What is the capital of France? Reply in one word."}},
			},
		},
	})
	require.NoError(t, err)

	timer := oteltest.NewTimer()
	out, err := client.InvokeModel(context.Background(), &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(testModelID),
		Body:        reqBody,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	timeRange := timer.Tick()

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Contains(t, string(out.Body), "Paris")

	span := exporter.FlushOne()
	span.AssertInTimeRange(timeRange)
	span.AssertNameIs("bedrock.invoke_model")

	metadata := span.Metadata()
	assert.Equal(t, "bedrock", metadata["provider"])
	assert.Equal(t, "invoke_model", metadata["endpoint"])
	assert.Equal(t, "claude-haiku-4-5-20251001", metadata["model"])
	assert.Equal(t, float64(50), metadata["max_tokens"])

	input := span.Attr("braintrust.input_json").String()
	assert.Contains(t, input, "capital of France")
	assert.NotContains(t, input, "anthropic_version")
	assert.NotContains(t, input, "max_tokens")

	output := span.Attr("braintrust.output_json").String()
	assert.Contains(t, output, "Paris")
	assert.Contains(t, output, `"role":"assistant"`)
	assert.NotContains(t, output, `"usage"`)
	assert.NotContains(t, output, `"id"`)

	// Claude token branch fires on any model id starting with anthropic.claude.
	metrics := span.Metrics()
	assert.Greater(t, metrics["prompt_tokens"], float64(0))
	assert.Greater(t, metrics["completion_tokens"], float64(0))
	assert.Greater(t, metrics["tokens"], float64(0))
	assert.Equal(t, float64(0), metrics["prompt_cache_creation_5m_tokens"])
	assert.Equal(t, float64(0), metrics["prompt_cache_creation_1h_tokens"])
	assert.NotContains(t, metrics, "time_to_first_token")
}

func TestConverseError(t *testing.T) {
	client, exporter := setUpTest(t)

	_, err := client.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String("bogus.nonexistent-model-v1:0"),
		Messages: []types.Message{
			{Role: types.ConversationRoleUser, Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "hi"}}},
		},
	})
	require.Error(t, err)

	span := exporter.FlushOne()
	span.AssertNameIs("bedrock.converse")
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
}
