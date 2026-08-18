package genai

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
)

// setUpTest is a helper function that sets up a new tracer provider and VCR for each test.
// It returns a genai client configured with VCR and an exporter.
func setUpTest(t *testing.T) (*genai.Client, *oteltest.Exporter) {
	t.Helper()

	tp, exporter := oteltest.Setup(t)

	mode := vcr.GetVCRMode()

	// Get API key or use dummy for replay mode
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if mode != vcr.ModeReplay && apiKey == "" {
		t.Fatal("GOOGLE_API_KEY or GEMINI_API_KEY not set (required in record/off mode)")
	}
	if apiKey == "" {
		apiKey = "dummy-google-key-for-replay"
	}

	// Create HTTP client with VCR
	httpClient := vcr.NewHTTPClient(t)

	// Wrap with tracing
	tracedClient := WrapClient(httpClient, WithTracerProvider(tp))

	// Create client with tracing and VCR
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: tracedClient,
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
	})
	require.NoError(t, err)

	return client, exporter
}

func candidateParts(t *testing.T, output any, candidateIndex int) []any {
	t.Helper()

	outputMap, ok := output.(map[string]any)
	require.True(t, ok)
	candidates, ok := outputMap["candidates"].([]any)
	require.True(t, ok)
	require.Greater(t, len(candidates), candidateIndex)
	candidate, ok := candidates[candidateIndex].(map[string]any)
	require.True(t, ok)
	content, ok := candidate["content"].(map[string]any)
	require.True(t, ok)
	parts, ok := content["parts"].([]any)
	require.True(t, ok)
	return parts
}

func TestBasicGenerateContent(t *testing.T) {
	client, exporter := setUpTest(t)

	assert := assert.New(t)
	require := require.New(t)

	// Make a simple generateContent request
	timer := oteltest.NewTimer()
	resp, err := client.Models.GenerateContent(
		context.Background(),
		"gemini-2.0-flash-exp",
		genai.Text("What is 2+2? Answer with just the number."),
		nil,
	)
	timeRange := timer.Tick()

	require.NoError(err)
	require.NotNil(resp)

	// Check the response contains expected answer
	text := resp.Text()
	assert.Contains(text, "4")

	// Verify span was created
	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("generate_content")
	assert.Equal(codes.Unset, ts.Status().Code)

	// Verify metadata
	metadata := ts.Metadata()
	assert.Equal("google", metadata["provider"])
	assert.Equal("gemini-2.0-flash-exp", metadata["model"])

	// Verify the provider-native input excludes request configuration.
	assert.Equal(map[string]any{
		"model": "gemini-2.0-flash-exp",
		"contents": []any{
			map[string]any{
				"parts": []any{map[string]any{"text": "What is 2+2? Answer with just the number."}},
				"role":  "user",
			},
		},
	}, ts.Input())

	// Verify output
	output := ts.Output()
	require.NotNil(output)

	// Verify metrics. TTFT only applies to streaming calls.
	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
	assert.NotContains(metrics, "time_to_first_token")
}

func TestStreamingGenerateContent(t *testing.T) {
	client, exporter := setUpTest(t)

	assert := assert.New(t)
	require := require.New(t)

	// Make a streaming generateContent request
	timer := oteltest.NewTimer()
	iter := client.Models.GenerateContentStream(
		context.Background(),
		"gemini-2.0-flash-exp",
		genai.Text("Count from 1 to 3. Output only the numbers."),
		nil,
	)

	var fullText string
	for resp, err := range iter {
		require.NoError(err)
		fullText += resp.Text()
	}
	timeRange := timer.Tick()

	assert.Contains(fullText, "1")
	assert.Contains(fullText, "2")
	assert.Contains(fullText, "3")

	// Verify span was created
	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("generate_content")
	assert.Equal(codes.Unset, ts.Status().Code)

	// Verify metadata
	metadata := ts.Metadata()
	assert.Equal("google", metadata["provider"])
	assert.Equal("gemini-2.0-flash-exp", metadata["model"])

	// Verify input
	input := ts.Input()
	require.NotNil(input)

	// Verify output was reconstructed from stream
	output := ts.Output()
	require.NotNil(output)

	// Verify metrics (token counts + time_to_first_token)
	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
	assert.Greater(metrics["time_to_first_token"], float64(0))
}

func TestStreamingGenerateContentWithThinking(t *testing.T) {
	client, exporter := setUpTest(t)

	thinkingBudget := int32(256)
	iter := client.Models.GenerateContentStream(
		context.Background(),
		"gemini-2.5-flash",
		genai.Text("Explain briefly why 2 plus 2 equals 4."),
		&genai.GenerateContentConfig{
			ThinkingConfig: &genai.ThinkingConfig{
				IncludeThoughts: true,
				ThinkingBudget:  &thinkingBudget,
			},
		},
	)
	for _, err := range iter {
		require.NoError(t, err)
	}

	ts := exporter.FlushOne()
	parts := candidateParts(t, ts.Output(), 0)

	var thoughtText string
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if ok && part["thought"] == true {
			if text, ok := part["text"].(string); ok {
				thoughtText += text
			}
		}
	}
	assert.NotEmpty(t, thoughtText)

	metrics := ts.Metrics()
	assert.Greater(t, metrics["completion_reasoning_tokens"], float64(0))
	assert.Greater(t, metrics["time_to_first_token"], float64(0))
}

func TestStreamingGenerateContentWithImage(t *testing.T) {
	client, exporter := setUpTest(t)

	iter := client.Models.GenerateContentStream(
		context.Background(),
		"gemini-2.5-flash-image",
		genai.Text("Generate a simple blue square icon."),
		&genai.GenerateContentConfig{ResponseModalities: []string{"TEXT", "IMAGE"}},
	)
	var imageBytes int
	for resp, err := range iter {
		require.NoError(t, err)
		for _, candidate := range resp.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil {
					imageBytes += len(part.InlineData.Data)
				}
			}
		}
	}
	require.Greater(t, imageBytes, 64*1024)

	ts := exporter.FlushOne()
	parts := candidateParts(t, ts.Output(), 0)
	var tracedImageBytes int
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		inlineData, ok := part["inlineData"].(map[string]any)
		if !ok {
			continue
		}
		if data, ok := inlineData["data"].(string); ok {
			tracedImageBytes += len(data)
		}
	}
	assert.Greater(t, tracedImageBytes, 64*1024)
	assert.Greater(t, ts.Metrics()["time_to_first_token"], float64(0))
}

func TestGenerateContentWithThinking(t *testing.T) {
	client, exporter := setUpTest(t)

	assert := assert.New(t)
	require := require.New(t)

	thinkingBudget := int32(1024)
	timer := oteltest.NewTimer()
	resp, err := client.Models.GenerateContent(
		context.Background(),
		"gemini-2.5-flash",
		genai.Text("Look at this sequence: 2, 6, 12, 20, 30. What is the pattern and what would be the formula for the nth term?"),
		&genai.GenerateContentConfig{
			MaxOutputTokens: 2048,
			SystemInstruction: genai.NewContentFromText(
				"You are a mathematical reasoning assistant.",
				genai.RoleUser,
			),
			ThinkingConfig: &genai.ThinkingConfig{
				IncludeThoughts: true,
				ThinkingBudget:  &thinkingBudget,
			},
		},
	)
	timeRange := timer.Tick()

	require.NoError(err)
	require.NotNil(resp)
	assert.Contains(resp.Text(), "n")

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("generate_content")
	assert.Equal(codes.Unset, ts.Status().Code)

	metadata := ts.Metadata()
	assert.Equal("google", metadata["provider"])
	assert.Equal("gemini-2.5-flash", metadata["model"])
	require.Contains(metadata, "thinkingConfig")
	assert.Equal(float64(2048), metadata["max_tokens"])
	assert.NotContains(metadata, "maxOutputTokens")
	assert.NotContains(metadata, "systemInstruction")
	assert.Equal(map[string]any{
		"includeThoughts": true,
		"thinkingBudget":  float64(1024),
	}, metadata["thinkingConfig"])

	input, ok := ts.Input().(map[string]any)
	require.True(ok)
	assert.Contains(input, "systemInstruction")

	parts := candidateParts(t, ts.Output(), 0)
	require.Len(parts, 2)
	thought, ok := parts[0].(map[string]any)
	require.True(ok)
	assert.Equal(true, thought["thought"])
	assert.NotEmpty(thought["text"])

	metrics := ts.Metrics()
	assert.Equal(float64(47), metrics["prompt_tokens"])
	assert.Equal(float64(1711), metrics["completion_tokens"])
	assert.Equal(float64(874), metrics["completion_reasoning_tokens"])
	assert.Equal(float64(1758), metrics["tokens"])
	assert.Equal(metrics["tokens"], metrics["prompt_tokens"]+metrics["completion_tokens"])
	assert.NotContains(metrics, "thoughts_token_count")
}

func TestGenerateContentWithFunctionCall(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Models.GenerateContent(
		context.Background(),
		"gemini-2.5-flash",
		genai.Text("Call get_weather for Paris, France. Do not answer directly."),
		&genai.GenerateContentConfig{
			Tools: []*genai.Tool{{
				FunctionDeclarations: []*genai.FunctionDeclaration{{
					Name:        "get_weather",
					Description: "Get the current weather for a location",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"location": {
								Type:        genai.TypeString,
								Description: "City and country",
							},
						},
						Required: []string{"location"},
					},
				}},
			}},
			ToolConfig: &genai.ToolConfig{
				FunctionCallingConfig: &genai.FunctionCallingConfig{
					Mode:                 genai.FunctionCallingConfigModeAny,
					AllowedFunctionNames: []string{"get_weather"},
				},
			},
		},
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.FunctionCalls())
	assert.Equal(t, "get_weather", resp.FunctionCalls()[0].Name)

	ts := exporter.FlushOne()
	ts.AssertNameIs("generate_content")
	metadata := ts.Metadata()
	assert.Equal(t, []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Get the current weather for a location",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{
							"type":        "string",
							"description": "City and country",
						},
					},
					"required": []any{"location"},
				},
			},
		},
	}, metadata["tools"])
	assert.Equal(t, map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "get_weather"},
	}, metadata["tool_choice"])

	input, ok := ts.Input().(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, input, "tools")
	assert.NotContains(t, input, "toolConfig")

	parts := candidateParts(t, ts.Output(), 0)
	require.NotEmpty(t, parts)
	part, ok := parts[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"name": "get_weather",
		"args": map[string]any{"location": "Paris, France"},
	}, part["functionCall"])
}

func TestGenerateContentWithCodeExecution(t *testing.T) {
	client, exporter := setUpTest(t)

	resp, err := client.Models.GenerateContent(
		context.Background(),
		"gemini-2.5-flash",
		genai.Text("Calculate the sum of the first five prime numbers using Python, then explain the result."),
		&genai.GenerateContentConfig{
			Tools: []*genai.Tool{{CodeExecution: &genai.ToolCodeExecution{}}},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Contains(t, resp.Text(), "28")

	ts := exporter.FlushOne()
	ts.AssertNameIs("generate_content")
	assert.Equal(t, codes.Unset, ts.Status().Code)

	metadata := ts.Metadata()
	assert.Equal(t, "google", metadata["provider"])
	assert.Equal(t, []any{map[string]any{"codeExecution": map[string]any{}}}, metadata["tools"])

	metrics := ts.Metrics()
	assert.Equal(t, float64(265), metrics["prompt_tokens"])
	assert.Equal(t, float64(280), metrics["completion_tokens"])
	assert.Equal(t, float64(159), metrics["completion_reasoning_tokens"])
	assert.Equal(t, float64(545), metrics["tokens"])
	assert.Equal(t, metrics["tokens"], metrics["prompt_tokens"]+metrics["completion_tokens"])
	assert.NotContains(t, metrics, "tool_use_prompt_token_count")
}

func TestStreamingGenerateContentWithCodeExecution(t *testing.T) {
	client, exporter := setUpTest(t)

	iter := client.Models.GenerateContentStream(
		context.Background(),
		"gemini-2.5-flash",
		genai.Text("Use Python to calculate 123 times 456, then explain the result."),
		&genai.GenerateContentConfig{
			Tools: []*genai.Tool{{CodeExecution: &genai.ToolCodeExecution{}}},
		},
	)

	var fullText string
	for resp, err := range iter {
		require.NoError(t, err)
		fullText += resp.Text()
	}
	assert.Contains(t, fullText, "56,088")

	ts := exporter.FlushOne()
	ts.AssertNameIs("generate_content")
	assert.Equal(t, codes.Unset, ts.Status().Code)

	parts := candidateParts(t, ts.Output(), 0)
	require.Len(t, parts, 3)
	assert.Contains(t, parts[0], "executableCode")
	assert.Contains(t, parts[1], "codeExecutionResult")
	assert.Equal(t, map[string]any{
		"text": "The product of 123 multiplied by 456 is 56,088. This means that if you have 123 groups of 456 items, or vice versa, the total number of items is 56,088.",
	}, parts[2])

	metadata := ts.Metadata()
	assert.Equal(t, map[string]any{
		"tool_use_prompt_tokens_details": []any{
			map[string]any{"modality": "TEXT", "tokenCount": float64(85)},
		},
	}, metadata["usage_by_modality"])

	metrics := ts.Metrics()
	assert.Equal(t, float64(105), metrics["prompt_tokens"])
	assert.Equal(t, float64(114), metrics["completion_tokens"])
	assert.Equal(t, float64(37), metrics["completion_reasoning_tokens"])
	assert.Equal(t, float64(219), metrics["tokens"])
	assert.Equal(t, metrics["tokens"], metrics["prompt_tokens"]+metrics["completion_tokens"])
	assert.NotContains(t, metrics, "tool_use_prompt_token_count")
}

func TestParseUsageTokens(t *testing.T) {
	t.Run("basic_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokenCount":     float64(12),
			"candidatesTokenCount": float64(9),
			"totalTokenCount":      float64(21),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(12), metrics["prompt_tokens"])
		assert.Equal(t, int64(9), metrics["completion_tokens"])
		assert.Equal(t, int64(21), metrics["tokens"])
	})

	t.Run("with_cached_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokenCount":        float64(100),
			"candidatesTokenCount":    float64(50),
			"totalTokenCount":         float64(150),
			"cachedContentTokenCount": float64(80),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(100), metrics["prompt_tokens"])
		assert.Equal(t, int64(50), metrics["completion_tokens"])
		assert.Equal(t, int64(150), metrics["tokens"])
		assert.Equal(t, int64(80), metrics["prompt_cached_tokens"])
	})

	t.Run("aggregates_reasoning_and_tool_use_tokens", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokenCount":        float64(12),
			"toolUsePromptTokenCount": float64(3),
			"candidatesTokenCount":    float64(9),
			"thoughtsTokenCount":      float64(6),
			"totalTokenCount":         float64(30),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(15), metrics["prompt_tokens"])
		assert.Equal(t, int64(15), metrics["completion_tokens"])
		assert.Equal(t, int64(6), metrics["completion_reasoning_tokens"])
		assert.Equal(t, int64(30), metrics["tokens"])
		assert.NotContains(t, metrics, "tool_use_prompt_token_count")
	})

	t.Run("preserves_reported_zero_values", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokenCount":        float64(0),
			"toolUsePromptTokenCount": float64(0),
			"candidatesTokenCount":    float64(0),
			"thoughtsTokenCount":      float64(0),
			"totalTokenCount":         float64(0),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, map[string]int64{
			"prompt_tokens":               0,
			"completion_tokens":           0,
			"completion_reasoning_tokens": 0,
			"tokens":                      0,
		}, metrics)
	})

	t.Run("omits_unavailable_values", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokenCount":  float64(10),
			"someNewTokenCount": float64(5),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, map[string]int64{"prompt_tokens": 10}, metrics)
		assert.NotContains(t, metrics, "some_new_token_count")
	})

	t.Run("captures_supported_modality_details", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokensDetails": []any{
				map[string]any{"modality": "AUDIO", "tokenCount": float64(3)},
				map[string]any{"modality": "AUDIO", "tokenCount": float64(4)},
				map[string]any{"modality": "IMAGE", "tokenCount": float64(8)},
			},
			"candidatesTokensDetails": []any{
				map[string]any{"modality": "AUDIO", "tokenCount": float64(5)},
				map[string]any{"modality": "IMAGE", "tokenCount": float64(6)},
			},
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(7), metrics["prompt_audio_tokens"])
		assert.Equal(t, int64(5), metrics["completion_audio_tokens"])
		assert.Equal(t, int64(6), metrics["completion_image_tokens"])
		assert.NotContains(t, metrics, "prompt_image_tokens")
	})

	t.Run("nil_usage", func(t *testing.T) {
		metrics := parseUsageTokens(nil)
		assert.Empty(t, metrics)
	})
}

func TestCaptureGenerationConfig(t *testing.T) {
	metadata := map[string]any{}
	captureGenerationConfig(metadata, map[string]any{
		"temperature":                float64(0.5),
		"topP":                       float64(0.9),
		"maxOutputTokens":            float64(128),
		"stopSequences":              []any{"done"},
		"presencePenalty":            float64(0.2),
		"frequencyPenalty":           float64(0.3),
		"topK":                       float64(40),
		"candidateCount":             float64(2),
		"responseLogprobs":           true,
		"logprobs":                   float64(5),
		"seed":                       float64(42),
		"responseMimeType":           "application/json",
		"responseSchema":             map[string]any{"type": "OBJECT"},
		"responseJsonSchema":         map[string]any{"type": "object"},
		"routingConfig":              map[string]any{"autoMode": map[string]any{}},
		"modelSelectionConfig":       map[string]any{"featureSelectionPreference": "BALANCED"},
		"responseModalities":         []any{"TEXT", "AUDIO"},
		"mediaResolution":            "MEDIA_RESOLUTION_HIGH",
		"speechConfig":               map[string]any{"languageCode": "en-US"},
		"audioTimestamp":             true,
		"thinkingConfig":             map[string]any{"includeThoughts": true},
		"imageConfig":                map[string]any{"aspectRatio": "1:1"},
		"enableEnhancedCivicAnswers": true,
		"unknownSetting":             "omit me",
	})

	assert.Equal(t, map[string]any{
		"temperature":                float64(0.5),
		"top_p":                      float64(0.9),
		"max_tokens":                 float64(128),
		"stop":                       []any{"done"},
		"presence_penalty":           float64(0.2),
		"frequency_penalty":          float64(0.3),
		"topK":                       float64(40),
		"candidateCount":             float64(2),
		"responseLogprobs":           true,
		"logprobs":                   float64(5),
		"seed":                       float64(42),
		"responseMimeType":           "application/json",
		"responseSchema":             map[string]any{"type": "OBJECT"},
		"responseJsonSchema":         map[string]any{"type": "object"},
		"routingConfig":              map[string]any{"autoMode": map[string]any{}},
		"modelSelectionConfig":       map[string]any{"featureSelectionPreference": "BALANCED"},
		"responseModalities":         []any{"TEXT", "AUDIO"},
		"mediaResolution":            "MEDIA_RESOLUTION_HIGH",
		"speechConfig":               map[string]any{"languageCode": "en-US"},
		"audioTimestamp":             true,
		"thinkingConfig":             map[string]any{"includeThoughts": true},
		"imageConfig":                map[string]any{"aspectRatio": "1:1"},
		"enableEnhancedCivicAnswers": true,
	}, metadata)
}

func TestNormalizeTools(t *testing.T) {
	raw := []any{
		map[string]any{
			"functionDeclarations": []any{
				map[string]any{
					"name":        "get_weather",
					"description": "Get the weather",
					"parameters": map[string]any{
						"type": "OBJECT",
					},
				},
			},
		},
		map[string]any{"googleSearch": map[string]any{}},
	}

	assert.Equal(t, []any{
		map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Get the weather",
				"parameters":  map[string]any{"type": "object"},
			},
		},
		map[string]any{"googleSearch": map[string]any{}},
	}, normalizeTools(raw))
}

func TestCaptureToolConfig(t *testing.T) {
	metadata := map[string]any{}
	captureToolConfig(metadata, map[string]any{
		"functionCallingConfig": map[string]any{
			"mode":                 "ANY",
			"allowedFunctionNames": []any{"get_weather", "get_forecast"},
		},
	})

	assert.Equal(t, map[string]any{
		"tool_choice":            "required",
		"allowed_function_names": []string{"get_weather", "get_forecast"},
	}, metadata)

	metadata = map[string]any{}
	captureToolConfig(metadata, map[string]any{
		"functionCallingConfig": map[string]any{"mode": "VALIDATED"},
	})
	assert.Equal(t, map[string]any{
		"tool_choice":           "auto",
		"function_calling_mode": "VALIDATED",
	}, metadata)
}

func TestNormalizeToolChoice(t *testing.T) {
	assert.Equal(t, "auto", normalizeToolChoice(map[string]any{
		"functionCallingConfig": map[string]any{"mode": "AUTO"},
	}))
	assert.Equal(t, "none", normalizeToolChoice(map[string]any{
		"functionCallingConfig": map[string]any{"mode": "NONE"},
	}))
	assert.Equal(t, "required", normalizeToolChoice(map[string]any{
		"functionCallingConfig": map[string]any{"mode": "ANY"},
	}))
	assert.Equal(t, "auto", normalizeToolChoice(map[string]any{
		"functionCallingConfig": map[string]any{"mode": "VALIDATED"},
	}))
	assert.Equal(t, map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "get_weather"},
	}, normalizeToolChoice(map[string]any{
		"functionCallingConfig": map[string]any{
			"mode":                 "ANY",
			"allowedFunctionNames": []any{"get_weather"},
		},
	}))
}

func TestContainsGenerateContent(t *testing.T) {
	tests := []struct {
		path    string
		matches bool
	}{
		// Non-streaming
		{"/v1beta/models/gemini-2.0-flash/generateContent", true},
		{"/v1beta/models/gemini-2.0-flash:generateContent", true},
		{"/v1/projects/p/locations/l/publishers/google/models/gemini-2.0-flash:generateContent", true},
		// Streaming
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", true},
		{"/v1beta/models/gemini-2.0-flash/streamGenerateContent", true},
		{"/v1/projects/p/locations/l/publishers/google/models/gemini-2.0-flash:streamGenerateContent", true},
		// Non-matching for generateContent, but now routed by containsEmbedContent
		{"/v1beta/models/gemini-2.0-flash/embedContent", false},
		{"/v1beta/models", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.matches, containsGenerateContent(tt.path))
		})
	}
}

func TestContainsEmbedContent(t *testing.T) {
	tests := []struct {
		path    string
		matches bool
	}{
		// Single embed — Gemini API (colon + slash variants)
		{"/v1beta/models/text-embedding-004:embedContent", true},
		{"/v1beta/models/text-embedding-004/embedContent", true},
		// Batch embed — Gemini API
		{"/v1beta/models/text-embedding-004:batchEmbedContents", true},
		{"/v1beta/models/text-embedding-004/batchEmbedContents", true},
		// Vertex AI
		{"/v1/projects/p/locations/l/publishers/google/models/text-embedding-004:embedContent", true},
		{"/v1/projects/p/locations/l/publishers/google/models/text-embedding-004:batchEmbedContents", true},
		// Non-matching
		{"/v1beta/models/gemini-2.0-flash:generateContent", false},
		{"/v1beta/models", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.matches, containsEmbedContent(tt.path))
		})
	}
}

func TestExtractModelFromEmbedPath(t *testing.T) {
	assert.Equal(t, "text-embedding-004", extractModelFromPath("/v1beta/models/text-embedding-004:embedContent"))
	assert.Equal(t, "text-embedding-004", extractModelFromPath("/v1beta/models/text-embedding-004:batchEmbedContents"))
	assert.Equal(t, "text-embedding-004", extractModelFromPath("/v1beta/models/text-embedding-004/embedContent"))
}

func TestEmbedContentOutputSummary(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]interface{}
		want map[string]any
	}{
		{
			name: "single",
			raw: map[string]interface{}{
				"embedding": map[string]interface{}{
					"values": []interface{}{0.1, 0.2, 0.3},
				},
			},
			want: map[string]any{"count": 1},
		},
		{
			name: "batch",
			raw: map[string]interface{}{
				"embeddings": []interface{}{
					map[string]interface{}{"values": []interface{}{0.1, 0.2}},
					map[string]interface{}{"values": []interface{}{0.3, 0.4}},
					map[string]interface{}{"values": []interface{}{0.5, 0.6}},
				},
			},
			want: map[string]any{"count": 3},
		},
		{
			name: "empty batch",
			raw:  map[string]interface{}{"embeddings": []interface{}{}},
			want: map[string]any{"count": 0},
		},
		{
			name: "empty object",
			raw:  map[string]interface{}{},
			want: map[string]any{"count": 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := embedContentOutputSummary(tc.raw)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractModelFromPath(t *testing.T) {
	tests := []struct {
		path  string
		model string
	}{
		{"/v1beta/models/gemini-2.0-flash:generateContent", "gemini-2.0-flash"},
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", "gemini-2.0-flash"},
		{"/v1beta/models/gemini-2.0-flash/generateContent", "gemini-2.0-flash"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.model, extractModelFromPath(tt.path))
		})
	}
}

func TestIsStreamingPath(t *testing.T) {
	tests := []struct {
		path      string
		streaming bool
	}{
		{"/v1beta/models/gemini-2.0-flash:generateContent", false},
		{"/v1beta/models/gemini-2.0-flash:streamGenerateContent", true},
		{"/v1beta/models/gemini-2.0-flash/streamGenerateContent", true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.streaming, isStreamingPath(tt.path))
		})
	}
}
