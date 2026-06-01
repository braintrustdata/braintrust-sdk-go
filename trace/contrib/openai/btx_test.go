package openai_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/btx"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

func TestBTXSpec(t *testing.T) {
	btx.RunProviderSpecs(t, executeOpenAI, "openai")
}

// executeOpenAI dispatches to the correct OpenAI executor based on endpoint.
func executeOpenAI(ctx context.Context, spec btx.LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if vcr.GetVCRMode() == vcr.ModeReplay {
		apiKey = "dummy-openai-key"
	}

	mw := traceopenai.NewMiddleware(traceopenai.WithTracerProvider(tp)) //nolint:bodyclose // false positive: middleware factory
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		option.WithMiddleware(mw),
	)

	switch spec.Endpoint {
	case "/v1/chat/completions":
		return executeChatCompletions(ctx, spec, client)
	case "/v1/responses":
		return executeResponses(ctx, spec, client)
	default:
		return fmt.Errorf("unsupported OpenAI endpoint: %s", spec.Endpoint)
	}
}

// executeChatCompletions handles OpenAI chat completions (streaming and non-streaming).
func executeChatCompletions(ctx context.Context, spec btx.LlmSpanSpec, client openai.Client) error {
	var history []openai.ChatCompletionMessageParamUnion

	for _, req := range spec.Requests {
		messages := buildChatMessages(req)
		// Prepend history from previous turns.
		allMessages := append(history, messages...)

		params := openai.ChatCompletionNewParams{
			Model:    openai.ChatModel(stringFromMap(req, "model")),
			Messages: allMessages,
		}

		if temp, ok := req["temperature"]; ok {
			params.Temperature = openai.Float(toFloat64(temp))
		}
		if mt, ok := req["max_tokens"]; ok {
			params.MaxTokens = openai.Int(int64(toFloat64(mt)))
		}
		if tools, ok := req["tools"].([]any); ok {
			params.Tools = buildChatTools(tools)
		}

		isStreaming := boolFromMap(req, "stream")

		if isStreaming {
			// Set stream_options if present.
			if so, ok := req["stream_options"].(map[string]any); ok {
				if boolFromMap(so, "include_usage") {
					params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
						IncludeUsage: param.Opt[bool]{Value: true},
					}
				}
			}

			stream := client.Chat.Completions.NewStreaming(ctx, params)
			for stream.Next() {
				// Consume stream to trigger instrumentation.
			}
			if err := stream.Err(); err != nil {
				return fmt.Errorf("streaming chat completions: %w", err)
			}
			// For multi-turn streaming, we don't accumulate assistant responses into history.
			// The spec tests don't require it for streaming chat completions.
		} else {
			resp, err := client.Chat.Completions.New(ctx, params)
			if err != nil {
				return fmt.Errorf("chat completions: %w", err)
			}
			// Accumulate assistant response for multi-turn.
			if len(resp.Choices) > 0 {
				history = append(allMessages, openai.AssistantMessage(resp.Choices[0].Message.Content))
			}
		}
	}

	return nil
}

// executeResponses handles the OpenAI Responses API (multi-turn, reasoning).
func executeResponses(ctx context.Context, spec btx.LlmSpanSpec, client openai.Client) error {
	var historyItems []responses.ResponseInputItemUnionParam

	for _, req := range spec.Requests {
		inputItems := buildResponsesInput(req)
		// Prepend history from previous turns.
		allInput := append(historyItems, inputItems...)

		params := responses.ResponseNewParams{
			Model: shared.ResponsesModel(stringFromMap(req, "model")),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: allInput,
			},
		}

		if reasoning, ok := req["reasoning"].(map[string]any); ok {
			var rp shared.ReasoningParam
			if effort, ok := reasoning["effort"].(string); ok {
				rp.Effort = shared.ReasoningEffort(effort)
			}
			if summary, ok := reasoning["summary"].(string); ok {
				rp.Summary = shared.ReasoningSummary(summary)
			}
			params.Reasoning = rp
		}

		resp, err := client.Responses.New(ctx, params)
		if err != nil {
			return fmt.Errorf("responses API: %w", err)
		}

		// Accumulate the response output items for the next turn.
		historyItems = append(allInput, responsesToInputItems(resp)...)
	}

	return nil
}

// --- Message building helpers ---

// buildChatMessages converts a spec request's messages to OpenAI ChatCompletionMessageParamUnion.
func buildChatMessages(req map[string]any) []openai.ChatCompletionMessageParamUnion {
	rawMsgs, ok := req["messages"].([]any)
	if !ok {
		return nil
	}

	var messages []openai.ChatCompletionMessageParamUnion
	for _, raw := range rawMsgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringFromMap(msg, "role")
		content := msg["content"]

		switch role {
		case "system":
			messages = append(messages, openai.SystemMessage(fmt.Sprintf("%v", content)))
		case "user":
			parts := buildChatContentParts(content)
			if parts != nil {
				messages = append(messages, openai.UserMessage(parts))
			} else {
				messages = append(messages, openai.UserMessage(fmt.Sprintf("%v", content)))
			}
		case "assistant":
			messages = append(messages, openai.AssistantMessage(fmt.Sprintf("%v", content)))
		}
	}
	return messages
}

// buildChatContentParts builds multipart content (text + images) for chat completions.
func buildChatContentParts(content any) []openai.ChatCompletionContentPartUnionParam {
	parts, ok := content.([]any)
	if !ok {
		return nil
	}

	var result []openai.ChatCompletionContentPartUnionParam
	for _, part := range parts {
		pm, ok := part.(map[string]any)
		if !ok {
			continue
		}
		switch pm["type"] {
		case "text":
			result = append(result, openai.TextContentPart(stringFromMap(pm, "text")))
		case "image_url":
			if iu, ok := pm["image_url"].(map[string]any); ok {
				result = append(result, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
					URL: stringFromMap(iu, "url"),
				}))
			}
		case "file":
			if f, ok := pm["file"].(map[string]any); ok {
				fp := openai.ChatCompletionContentPartFileFileParam{}
				if fd := stringFromMap(f, "file_data"); fd != "" {
					fp.FileData = param.Opt[string]{Value: fd}
				}
				if fn := stringFromMap(f, "filename"); fn != "" {
					fp.Filename = param.Opt[string]{Value: fn}
				}
				result = append(result, openai.FileContentPart(fp))
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// buildChatTools converts spec tool definitions to OpenAI tool params.
func buildChatTools(tools []any) []openai.ChatCompletionToolParam {
	var result []openai.ChatCompletionToolParam
	for _, tool := range tools {
		tm, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tm["function"].(map[string]any)
		if !ok {
			continue
		}

		param := openai.ChatCompletionToolParam{
			Type: "function",
			Function: openai.FunctionDefinitionParam{
				Name: stringFromMap(fn, "name"),
			},
		}
		if desc, ok := fn["description"].(string); ok {
			param.Function.Description = openai.String(desc)
		}
		if params, ok := fn["parameters"].(map[string]any); ok {
			param.Function.Parameters = openai.FunctionParameters(params)
		}
		result = append(result, param)
	}
	return result
}

// buildResponsesInput converts spec request input items to OpenAI Responses API params.
func buildResponsesInput(req map[string]any) []responses.ResponseInputItemUnionParam {
	rawInput, ok := req["input"].([]any)
	if !ok {
		return nil
	}

	var items []responses.ResponseInputItemUnionParam
	for _, raw := range rawInput {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringFromMap(msg, "role")
		content := stringFromMap(msg, "content")
		if role != "" && content != "" {
			items = append(items, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRole(role)))
		}
	}
	return items
}

// responsesToInputItems converts a Responses API response into input items
// for accumulating multi-turn history. The output items must be fed back
// as properly typed input items (not ID references) so the middleware logs
// full context.
func responsesToInputItems(resp *responses.Response) []responses.ResponseInputItemUnionParam {
	var items []responses.ResponseInputItemUnionParam
	for _, output := range resp.Output {
		switch output.Type {
		case "message":
			var contentParams []responses.ResponseOutputMessageContentUnionParam
			for _, c := range output.Content {
				if c.Type == "output_text" {
					contentParams = append(contentParams, responses.ResponseOutputMessageContentUnionParam{
						OfOutputText: &responses.ResponseOutputTextParam{
							Text:        c.Text,
							Annotations: []responses.ResponseOutputTextAnnotationUnionParam{},
						},
					})
				}
			}
			items = append(items, responses.ResponseInputItemParamOfOutputMessage(
				contentParams,
				output.ID,
				responses.ResponseOutputMessageStatus(output.Status),
			))
		case "reasoning":
			var summaries []responses.ResponseReasoningItemSummaryParam
			for _, s := range output.Summary {
				summaries = append(summaries, responses.ResponseReasoningItemSummaryParam{
					Text: s.Text,
				})
			}
			items = append(items, responses.ResponseInputItemParamOfReasoning(output.ID, summaries))
		}
	}
	return items
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
