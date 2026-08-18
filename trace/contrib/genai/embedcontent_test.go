package genai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

const testEmbeddingModel = "gemini-embedding-001"

func TestEmbedContentSingle(t *testing.T) {
	client, exporter := setUpTest(t)

	timer := oteltest.NewTimer()
	resp, err := client.Models.EmbedContent(
		context.Background(),
		testEmbeddingModel,
		genai.Text("The quick brown fox jumps over the lazy dog"),
		nil,
	)
	timeRange := timer.Tick()
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotEmpty(t, resp.Embeddings)

	ts := exporter.FlushOne()
	ts.AssertInTimeRange(timeRange)
	ts.AssertNameIs("embed_content")
	assert.Equal(t, codes.Unset, ts.Stub.Status.Code)

	ts.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})

	assert.Equal(t, map[string]any{
		"inputs": []any{
			map[string]any{"content": "The quick brown fox jumps over the lazy dog"},
		},
	}, ts.Input())
	assert.Equal(t, map[string]any{"count": float64(1)}, ts.Output())

	metadata := ts.Metadata()
	assert.Equal(t, "google", metadata["provider"])
	assert.Equal(t, testEmbeddingModel, metadata["model"])
}

func TestEmbedContentBatch(t *testing.T) {
	client, exporter := setUpTest(t)

	contents := []*genai.Content{
		{Parts: []*genai.Part{{Text: "hello world"}}},
		{Parts: []*genai.Part{{Text: "goodbye world"}}},
		{Parts: []*genai.Part{{Text: "braintrust tracing"}}},
	}

	resp, err := client.Models.EmbedContent(
		context.Background(),
		testEmbeddingModel,
		contents,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Embeddings, 3)

	ts := exporter.FlushOne()
	ts.AssertNameIs("embed_content")

	assert.Equal(t, map[string]any{
		"inputs": []any{
			map[string]any{"content": "hello world"},
			map[string]any{"content": "goodbye world"},
			map[string]any{"content": "braintrust tracing"},
		},
	}, ts.Input())
	assert.Equal(t, map[string]any{"count": float64(3)}, ts.Output())
}

func TestEmbedContentWithTaskType(t *testing.T) {
	client, exporter := setUpTest(t)

	outputDimensions := int32(256)
	resp, err := client.Models.EmbedContent(
		context.Background(),
		testEmbeddingModel,
		genai.Text("What is the capital of France?"),
		&genai.EmbedContentConfig{
			TaskType:             "RETRIEVAL_QUERY",
			OutputDimensionality: &outputDimensions,
		},
	)
	require.NoError(t, err)
	require.NotNil(t, resp)

	ts := exporter.FlushOne()
	assert.Equal(t, map[string]any{
		"inputs": []any{
			map[string]any{"content": "What is the capital of France?"},
		},
		"output_dimensions": float64(256),
	}, ts.Input())

	metadata := ts.Metadata()
	assert.Equal(t, map[string]any{
		"provider": "google",
		"model":    testEmbeddingModel,
	}, metadata)
}

func TestCanonicalEmbeddingInput(t *testing.T) {
	raw := map[string]any{
		"requests": []any{
			map[string]any{
				"content": map[string]any{
					"parts": []any{
						map[string]any{"text": "Describe this image"},
						map[string]any{"inlineData": map[string]any{
							"mimeType": "image/png",
							"data":     "aGVsbG8=",
						}},
						map[string]any{
							"fileData": map[string]any{
								"mimeType":    "video/mp4",
								"fileUri":     "gs://bucket/demo.mp4",
								"displayName": "demo.mp4",
							},
							"videoMetadata": map[string]any{"startOffset": "1s"},
						},
					},
				},
				"outputDimensionality": float64(256),
			},
		},
	}

	assert.Equal(t, map[string]any{
		"inputs": []any{
			map[string]any{
				"content": []any{
					map[string]any{"type": "text", "text": "Describe this image"},
					map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:image/png;base64,aGVsbG8="},
					},
					map[string]any{
						"type": "file",
						"file": map[string]any{
							"file_data": "gs://bucket/demo.mp4",
							"filename":  "demo.mp4",
						},
					},
				},
			},
		},
		"output_dimensions": float64(256),
	}, canonicalEmbeddingInput(raw))
}

func TestParseEmbeddingUsageTokens(t *testing.T) {
	metrics := parseEmbeddingUsageTokens(map[string]any{
		"promptTokenCount":     float64(12),
		"candidatesTokenCount": float64(99),
		"totalTokenCount":      float64(12),
		"promptTokensDetails": []any{
			map[string]any{"modality": "AUDIO", "tokenCount": float64(3)},
		},
	})

	assert.Equal(t, map[string]int64{
		"prompt_tokens":       12,
		"tokens":              12,
		"prompt_audio_tokens": 3,
	}, metrics)
}
