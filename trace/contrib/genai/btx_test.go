package genai_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/internal/btx"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
)

func TestBTXSpec(t *testing.T) {
	btx.RunProviderSpecs(t, executeGoogle, "google")
}

// --- Google/Gemini executor ---

// executeGoogle dispatches to the correct Google executor based on endpoint.
func executeGoogle(ctx context.Context, spec btx.LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if vcr.GetVCRMode() == vcr.ModeReplay {
		apiKey = "dummy-google-key"
	}

	// Wrap the HTTP client with Gemini tracing.
	tracedClient := tracegenai.WrapClient(httpClient, tracegenai.WithTracerProvider(tp))

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		HTTPClient: tracedClient,
		APIKey:     apiKey,
		Backend:    genai.BackendGeminiAPI,
	})
	if err != nil {
		return fmt.Errorf("creating genai client: %w", err)
	}

	// The endpoint contains the model name and operation, e.g.
	// "/v1/models/gemini-3.1-flash-lite-preview:generateContent"
	if strings.Contains(spec.Endpoint, ":generateContent") {
		return executeGenerateContent(ctx, spec, client)
	}

	return fmt.Errorf("unsupported Google endpoint: %s", spec.Endpoint)
}

// extractModelFromEndpoint extracts the model name from a Gemini endpoint path.
// e.g. "/v1/models/gemini-3.1-flash-lite-preview:generateContent" → "gemini-3.1-flash-lite-preview"
func extractModelFromEndpoint(endpoint string) string {
	// Find the model segment between "/models/" and ":"
	const prefix = "/models/"
	idx := strings.Index(endpoint, prefix)
	if idx < 0 {
		return endpoint
	}
	rest := endpoint[idx+len(prefix):]
	if colonIdx := strings.Index(rest, ":"); colonIdx >= 0 {
		return rest[:colonIdx]
	}
	return rest
}

// executeGenerateContent handles Google Gemini generateContent calls.
func executeGenerateContent(ctx context.Context, spec btx.LlmSpanSpec, client *genai.Client) error {
	model := extractModelFromEndpoint(spec.Endpoint)

	for _, req := range spec.Requests {
		contents := buildGeminiContents(req)

		var config *genai.GenerateContentConfig
		if gc, ok := req["generationConfig"].(map[string]any); ok {
			config = buildGeminiConfig(gc)
		}

		_, err := client.Models.GenerateContent(ctx, model, contents, config)
		if err != nil {
			return fmt.Errorf("generateContent: %w", err)
		}
	}

	return nil
}

// buildGeminiContents converts spec request contents to genai.Content structs.
func buildGeminiContents(req map[string]any) []*genai.Content {
	rawContents, ok := req["contents"].([]any)
	if !ok {
		return nil
	}

	var contents []*genai.Content
	for _, rc := range rawContents {
		cm, ok := rc.(map[string]any)
		if !ok {
			continue
		}
		content := &genai.Content{
			Role: stringFromMap(cm, "role"),
		}
		if rawParts, ok := cm["parts"].([]any); ok {
			for _, rp := range rawParts {
				pm, ok := rp.(map[string]any)
				if !ok {
					continue
				}
				part := buildGeminiPart(pm)
				if part != nil {
					content.Parts = append(content.Parts, part)
				}
			}
		}
		contents = append(contents, content)
	}
	return contents
}

// buildGeminiPart converts a spec part map to a genai.Part.
func buildGeminiPart(pm map[string]any) *genai.Part {
	// Text part.
	if text, ok := pm["text"].(string); ok {
		return &genai.Part{Text: text}
	}

	// Inline data (base64 image/binary).
	if id, ok := pm["inline_data"].(map[string]any); ok {
		mimeType := stringFromMap(id, "mime_type")
		dataStr := stringFromMap(id, "data")
		data, err := base64.StdEncoding.DecodeString(dataStr)
		if err != nil {
			return nil
		}
		return &genai.Part{
			InlineData: &genai.Blob{
				MIMEType: mimeType,
				Data:     data,
			},
		}
	}

	return nil
}

// buildGeminiConfig converts a spec generationConfig map to genai.GenerateContentConfig.
func buildGeminiConfig(gc map[string]any) *genai.GenerateContentConfig {
	config := &genai.GenerateContentConfig{}
	if temp, ok := gc["temperature"]; ok {
		config.Temperature = genai.Ptr(float32(toFloat64(temp)))
	}
	if topP, ok := gc["topP"]; ok {
		config.TopP = genai.Ptr(float32(toFloat64(topP)))
	}
	if topK, ok := gc["topK"]; ok {
		config.TopK = genai.Ptr(float32(toFloat64(topK)))
	}
	return config
}

// --- Map helpers ---

func stringFromMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolFromMap(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case float32:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}
