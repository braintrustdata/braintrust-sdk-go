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

func TestEmbedContent(t *testing.T) {
	outputDimensions := int32(256)
	for _, tc := range []struct {
		name      string
		contents  []*genai.Content
		config    *genai.EmbedContentConfig
		wantInput map[string]any
		wantCount int
	}{
		{
			name:     "single",
			contents: genai.Text("The quick brown fox jumps over the lazy dog"),
			wantInput: map[string]any{"inputs": []any{
				map[string]any{"content": "The quick brown fox jumps over the lazy dog"},
			}},
			wantCount: 1,
		},
		{
			name: "batch",
			contents: []*genai.Content{
				{Parts: []*genai.Part{{Text: "hello world"}}},
				{Parts: []*genai.Part{{Text: "goodbye world"}}},
				{Parts: []*genai.Part{{Text: "braintrust tracing"}}},
			},
			wantInput: map[string]any{"inputs": []any{
				map[string]any{"content": "hello world"},
				map[string]any{"content": "goodbye world"},
				map[string]any{"content": "braintrust tracing"},
			}},
			wantCount: 3,
		},
		{
			name:     "task_type_and_dimensions",
			contents: genai.Text("What is the capital of France?"),
			config: &genai.EmbedContentConfig{
				TaskType:             "RETRIEVAL_QUERY",
				OutputDimensionality: &outputDimensions,
			},
			wantInput: map[string]any{
				"inputs":            []any{map[string]any{"content": "What is the capital of France?"}},
				"output_dimensions": float64(256),
			},
			wantCount: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, exporter := setUpTest(t)
			timer := oteltest.NewTimer()
			resp, err := client.Models.EmbedContent(context.Background(), testEmbeddingModel, tc.contents, tc.config)
			timeRange := timer.Tick()
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Len(t, resp.Embeddings, tc.wantCount)

			span := exporter.FlushOne()
			span.AssertInTimeRange(timeRange)
			span.AssertNameIs("embed_content")
			assert.Equal(t, codes.Unset, span.Status().Code)
			span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "llm"})
			assert.Equal(t, tc.wantInput, span.Input())
			assert.Equal(t, map[string]any{"count": float64(tc.wantCount)}, span.Output())
			assert.Equal(t, map[string]any{
				"provider": "google",
				"model":    testEmbeddingModel,
			}, span.Metadata())
		})
	}
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
