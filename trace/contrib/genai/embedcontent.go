package genai

// this file parses the embedContent / batchEmbedContents API.

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// embedContentTracer is a tracer for the Gemini :embedContent and
// :batchEmbedContents endpoints (Gemini API + Vertex AI).
type embedContentTracer struct {
	cfg      *config
	metadata map[string]any
	model    string
}

func newEmbedContentTracer(cfg *config, model string) *embedContentTracer {
	return &embedContentTracer{
		cfg:   cfg,
		model: model,
		metadata: map[string]any{
			"provider": "google",
		},
	}
}

func (et *embedContentTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := et.cfg.tracer().Start(
		ctx,
		"embed_content",
		trace.WithTimestamp(t),
	)

	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		return ctx, span, err
	}

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	if et.model != "" {
		et.metadata["model"] = et.model
	}

	if err := internal.SetJSONAttr(span, "braintrust.input_json", canonicalEmbeddingInput(raw)); err != nil {
		return ctx, span, err
	}
	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return ctx, span, err
	}

	return ctx, span, nil
}

func (et *embedContentTracer) TagSpan(span trace.Span, body io.Reader) error {
	var raw map[string]interface{}
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		return err
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", et.metadata); err != nil {
		return err
	}
	if err := internal.SetJSONAttr(span, "braintrust.output_json", embedContentOutputSummary(raw)); err != nil {
		return err
	}

	metrics := make(map[string]any)
	if usage, ok := raw["usageMetadata"].(map[string]any); ok {
		for k, v := range parseEmbeddingUsageTokens(usage) {
			metrics[k] = v
		}
	}
	if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
		return err
	}

	return nil
}

// embedContentOutputSummary intentionally reports only the number of returned
// embeddings. Raw vectors, dimensions, and derived vector data are omitted.
func embedContentOutputSummary(raw map[string]interface{}) map[string]any {
	if _, ok := raw["embedding"].(map[string]interface{}); ok {
		return map[string]any{"count": 1}
	}
	if embeddings, ok := raw["embeddings"].([]interface{}); ok {
		return map[string]any{"count": len(embeddings)}
	}
	return map[string]any{"count": 0}
}

func canonicalEmbeddingInput(raw map[string]any) map[string]any {
	input := map[string]any{"inputs": []any{}}
	inputs := make([]any, 0)

	if requests, ok := raw["requests"].([]any); ok {
		for _, rawRequest := range requests {
			request, ok := rawRequest.(map[string]any)
			if !ok {
				continue
			}
			if content, ok := request["content"].(map[string]any); ok {
				inputs = append(inputs, map[string]any{"content": canonicalEmbeddingContent(content)})
			}
		}
	} else if content, ok := raw["content"].(map[string]any); ok {
		inputs = append(inputs, map[string]any{"content": canonicalEmbeddingContent(content)})
	}
	input["inputs"] = inputs

	if dimensions, ok := raw["outputDimensionality"]; ok {
		input["output_dimensions"] = dimensions
	} else if requests, ok := raw["requests"].([]any); ok && len(requests) > 0 {
		if first, ok := requests[0].(map[string]any); ok {
			if dimensions, ok := first["outputDimensionality"]; ok {
				input["output_dimensions"] = dimensions
			}
		}
	}

	return input
}

func canonicalEmbeddingContent(content map[string]any) any {
	parts, _ := content["parts"].([]any)
	if len(parts) == 1 {
		if part, ok := parts[0].(map[string]any); ok {
			if text, ok := part["text"].(string); ok {
				return text
			}
		}
	}

	// The canonical embedding schema only permits text, image_url, and file
	// parts. Provider-only companion fields and unsupported part types are
	// intentionally omitted rather than copied into arbitrary telemetry.
	normalized := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := part["text"].(string); ok {
			normalized = append(normalized, map[string]any{"type": "text", "text": text})
			continue
		}
		if mediaPart := canonicalEmbeddingMediaPart(part); mediaPart != nil {
			normalized = append(normalized, mediaPart)
		}
	}
	return normalized
}

func canonicalEmbeddingMediaPart(part map[string]any) any {
	inlineData, _ := part["inlineData"].(map[string]any)
	if inlineData == nil {
		inlineData, _ = part["inline_data"].(map[string]any)
	}
	if inlineData != nil {
		mimeType, _ := inlineData["mimeType"].(string)
		if mimeType == "" {
			mimeType, _ = inlineData["mime_type"].(string)
		}
		data, _ := inlineData["data"].(string)
		if mimeType == "" || data == "" {
			return nil
		}
		return canonicalEmbeddingMedia(mimeType, "data:"+mimeType+";base64,"+data, "")
	}

	fileData, _ := part["fileData"].(map[string]any)
	if fileData == nil {
		fileData, _ = part["file_data"].(map[string]any)
	}
	if fileData != nil {
		mimeType, _ := fileData["mimeType"].(string)
		if mimeType == "" {
			mimeType, _ = fileData["mime_type"].(string)
		}
		uri, _ := fileData["fileUri"].(string)
		if uri == "" {
			uri, _ = fileData["file_uri"].(string)
		}
		if uri == "" {
			return nil
		}
		filename, _ := fileData["displayName"].(string)
		if filename == "" {
			filename, _ = fileData["display_name"].(string)
		}
		return canonicalEmbeddingMedia(mimeType, uri, filename)
	}
	return nil
}

func canonicalEmbeddingMedia(mimeType, value, filename string) any {
	if strings.HasPrefix(mimeType, "image/") {
		return map[string]any{
			"type":      "image_url",
			"image_url": map[string]any{"url": value},
		}
	}
	file := map[string]any{"file_data": value}
	if filename != "" {
		file["filename"] = filename
	}
	return map[string]any{
		"type": "file",
		"file": file,
	}
}

func parseEmbeddingUsageTokens(usage map[string]any) map[string]int64 {
	metrics := map[string]int64{}
	if ok, promptTokens := internal.ToInt64(usage["promptTokenCount"]); ok {
		metrics["prompt_tokens"] = promptTokens
	}
	if ok, totalTokens := internal.ToInt64(usage["totalTokenCount"]); ok {
		metrics["tokens"] = totalTokens
	} else if promptTokens, ok := metrics["prompt_tokens"]; ok {
		metrics["tokens"] = promptTokens
	}
	if tokens, ok := sumModalityTokens(usage["promptTokensDetails"], "AUDIO"); ok {
		metrics["prompt_audio_tokens"] = tokens
	}
	return metrics
}

// Ensure our tracer implements the shared interface
var _ internal.MiddlewareTracer = &embedContentTracer{}
