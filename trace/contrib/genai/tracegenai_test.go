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
	assert.Equal("gemini", metadata["provider"])
	assert.Equal("gemini-2.0-flash-exp", metadata["model"])

	// Verify input
	input := ts.Input()
	require.NotNil(input)

	// Verify output
	output := ts.Output()
	require.NotNil(output)

	// Verify metrics (token counts + time_to_first_token)
	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
	assert.Greater(metrics["time_to_first_token"], float64(0))
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
	assert.Equal("gemini", metadata["provider"])
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
	assert.Equal("gemini", metadata["provider"])
	assert.Equal("gemini-2.5-flash", metadata["model"])
	require.Contains(metadata, "thinkingConfig")
	assert.Equal(map[string]any{
		"includeThoughts": true,
		"thinkingBudget":  float64(1024),
	}, metadata["thinkingConfig"])

	metrics := ts.Metrics()
	assert.Greater(metrics["completion_reasoning_tokens"], float64(0))
	assert.NotContains(metrics, "thoughts_token_count")
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["tokens"], float64(0))
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

	t.Run("nil_usage", func(t *testing.T) {
		metrics := parseUsageTokens(nil)
		assert.Empty(t, metrics)
	})

	t.Run("unknown_field", func(t *testing.T) {
		usage := map[string]interface{}{
			"promptTokenCount":  float64(10),
			"someNewTokenCount": float64(5),
		}

		metrics := parseUsageTokens(usage)

		assert.Equal(t, int64(10), metrics["prompt_tokens"])
		// Unknown field should be converted to snake_case
		assert.Equal(t, int64(5), metrics["some_new_token_count"])
	})
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

func TestIsBatchEmbedPath(t *testing.T) {
	assert.True(t, isBatchEmbedPath("/v1beta/models/text-embedding-004:batchEmbedContents"))
	assert.False(t, isBatchEmbedPath("/v1beta/models/text-embedding-004:embedContent"))
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
			want: map[string]any{"embedding_length": 3, "embeddings_count": 1},
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
			want: map[string]any{"embedding_length": 2, "embeddings_count": 3},
		},
		{
			name: "empty batch",
			raw:  map[string]interface{}{"embeddings": []interface{}{}},
			want: map[string]any{"embeddings_count": 0},
		},
		{
			name: "empty object",
			raw:  map[string]interface{}{},
			want: map[string]any{},
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

func TestCamelToSnake(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"promptTokenCount", "prompt_token_count"},
		{"cachedContentTokenCount", "cached_content_token_count"},
		{"totalTokenCount", "total_token_count"},
		{"simpleWord", "simple_word"},
		{"ABC", "a_b_c"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := camelToSnake(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}
