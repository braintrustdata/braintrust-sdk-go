package anthropic_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/btx"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
)

func TestBTXSpec(t *testing.T) {
	btx.RunProviderSpecs(t, executeAnthropic, "anthropic")
}

// executeAnthropic dispatches to the Anthropic executor.
func executeAnthropic(ctx context.Context, spec btx.LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if vcr.GetVCRMode() == vcr.ModeReplay {
		apiKey = "dummy-anthropic-key"
	}

	//nolint:bodyclose // false positive: middleware factory, not HTTP response
	opts := []anthropicoption.RequestOption{
		anthropicoption.WithAPIKey(apiKey),
		anthropicoption.WithHTTPClient(httpClient),
		anthropicoption.WithMiddleware(traceanthropic.NewMiddleware(traceanthropic.WithTracerProvider(tp))),
	}

	client := anthropic.NewClient(opts...)

	switch spec.Endpoint {
	case "/v1/messages":
		return executeAnthropicMessages(ctx, spec, client)
	default:
		return fmt.Errorf("unsupported Anthropic endpoint: %s", spec.Endpoint)
	}
}

// executeAnthropicMessages handles Anthropic messages (streaming and non-streaming).
func executeAnthropicMessages(ctx context.Context, spec btx.LlmSpanSpec, client anthropic.Client) error {
	var history []anthropic.MessageParam

	for _, req := range spec.Requests {
		messages := buildAnthropicMessages(req)
		allMessages := append(history, messages...)

		params := anthropic.MessageNewParams{
			Model:    anthropic.Model(stringFromMap(req, "model")),
			Messages: allMessages,
		}

		if mt, ok := req["max_tokens"]; ok {
			params.MaxTokens = int64(toFloat64(mt))
		}
		if temp, ok := req["temperature"]; ok {
			params.Temperature = anthropic.Float(toFloat64(temp))
		}

		// Handle system prompt.
		if sys := buildAnthropicSystem(req); len(sys) > 0 {
			params.System = sys
		}

		isStreaming := boolFromMap(req, "stream")

		// Build extra headers from spec.
		var extraOpts []anthropicoption.RequestOption
		for k, v := range spec.Headers {
			extraOpts = append(extraOpts, anthropicoption.WithHeader(k, v))
		}

		if isStreaming {
			stream := client.Messages.NewStreaming(ctx, params, extraOpts...)
			for stream.Next() {
				// Consume stream to trigger instrumentation.
			}
			if err := stream.Err(); err != nil {
				return fmt.Errorf("streaming anthropic messages: %w", err)
			}
		} else {
			resp, err := client.Messages.New(ctx, params, extraOpts...)
			if err != nil {
				return fmt.Errorf("anthropic messages: %w", err)
			}
			// Accumulate for multi-turn.
			assistantContent := make([]anthropic.ContentBlockParamUnion, 0, len(resp.Content))
			for _, block := range resp.Content {
				if block.Type == "text" {
					assistantContent = append(assistantContent, anthropic.NewTextBlock(block.Text))
				}
			}
			if len(assistantContent) > 0 {
				history = append(allMessages, anthropic.NewAssistantMessage(assistantContent...))
			}
		}
	}

	return nil
}

// buildAnthropicMessages converts spec request messages to Anthropic MessageParam.
func buildAnthropicMessages(req map[string]any) []anthropic.MessageParam {
	rawMsgs, ok := req["messages"].([]any)
	if !ok {
		return nil
	}

	var messages []anthropic.MessageParam
	for _, raw := range rawMsgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringFromMap(msg, "role")
		content := msg["content"]

		switch role {
		case "user":
			blocks := buildAnthropicContentBlocks(content)
			if blocks != nil {
				messages = append(messages, anthropic.NewUserMessage(blocks...))
			} else {
				messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(fmt.Sprintf("%v", content))))
			}
		case "assistant":
			messages = append(messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(fmt.Sprintf("%v", content))))
		}
	}
	return messages
}

// buildAnthropicContentBlocks builds content blocks for multipart Anthropic messages.
func buildAnthropicContentBlocks(content any) []anthropic.ContentBlockParamUnion {
	parts, ok := content.([]any)
	if !ok {
		return nil
	}

	var blocks []anthropic.ContentBlockParamUnion
	for _, part := range parts {
		pm, ok := part.(map[string]any)
		if !ok {
			continue
		}
		switch pm["type"] {
		case "text":
			blocks = append(blocks, anthropic.NewTextBlock(stringFromMap(pm, "text")))
		case "image":
			if src, ok := pm["source"].(map[string]any); ok {
				mediaType := stringFromMap(src, "media_type")
				data := stringFromMap(src, "data")
				blocks = append(blocks, anthropic.NewImageBlockBase64(mediaType, data))
			}
		case "document":
			if src, ok := pm["source"].(map[string]any); ok {
				data := stringFromMap(src, "data")
				blocks = append(blocks, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
					Data: data,
				}))
			}
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

// buildAnthropicSystem builds the system prompt from a spec request.
func buildAnthropicSystem(req map[string]any) []anthropic.TextBlockParam {
	sys := req["system"]
	if sys == nil {
		return nil
	}

	switch s := sys.(type) {
	case string:
		return []anthropic.TextBlockParam{{Text: s}}
	case []any:
		// List of system message blocks.
		var blocks []anthropic.TextBlockParam
		for _, item := range s {
			if m, ok := item.(map[string]any); ok {
				block := anthropic.TextBlockParam{
					Text: stringFromMap(m, "text"),
				}
				// Handle cache_control if present.
				if cc, ok := m["cache_control"].(map[string]any); ok {
					_ = stringFromMap(cc, "type") // always "ephemeral"
					ccParam := anthropic.NewCacheControlEphemeralParam()
					if ttl, ok := cc["ttl"].(string); ok {
						ccParam.TTL = anthropic.CacheControlEphemeralTTL(ttl)
					}
					block.CacheControl = ccParam
				}
				blocks = append(blocks, block)
			}
		}
		return blocks
	default:
		return nil
	}
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
