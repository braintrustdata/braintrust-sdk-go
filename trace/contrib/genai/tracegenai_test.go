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
)

// setUpTest is a helper function that sets up a new tracer provider for each test.
// It returns a genai client and an exporter.
func setUpTest(t *testing.T) (*genai.Client, *oteltest.Exporter) {
	t.Helper()

	_, exporter := oteltest.Setup(t)

	// Get API key from environment
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		t.Skip("GOOGLE_API_KEY not set")
	}

	// Create client with tracing
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		HTTPClient: Client(),
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
	ts.AssertNameIs("genai.models.generateContent")
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

	// Verify metrics (token counts)
	metrics := ts.Metrics()
	assert.Greater(metrics["prompt_tokens"], float64(0))
	assert.Greater(metrics["completion_tokens"], float64(0))
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
