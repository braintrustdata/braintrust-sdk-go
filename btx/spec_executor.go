package btx

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"encoding/base64"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
	"google.golang.org/genai"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	traceanthropic "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/anthropic"
	tracebedrockruntime "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime"
	tracegenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genai"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

// executeSpec runs all requests in a spec under a parent OTel span and returns
// the trace ID (hex string). The httpClient should be VCR-wrapped for replay.
func executeSpec(ctx context.Context, spec LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) (string, error) {
	tracer := tp.Tracer("btx")
	ctx, rootSpan := tracer.Start(ctx, spec.Name)
	defer rootSpan.End()

	traceID := rootSpan.SpanContext().TraceID().String()

	switch spec.Provider {
	case "openai":
		if err := executeOpenAI(ctx, spec, tp, httpClient); err != nil {
			return traceID, err
		}
	case "anthropic":
		if err := executeAnthropic(ctx, spec, tp, httpClient); err != nil {
			return traceID, err
		}
	case "google":
		if err := executeGoogle(ctx, spec, tp, httpClient); err != nil {
			return traceID, err
		}
	case "bedrock":
		if err := executeBedrock(ctx, spec, tp, httpClient); err != nil {
			return traceID, err
		}
	default:
		return traceID, fmt.Errorf("unsupported provider: %s", spec.Provider)
	}

	return traceID, nil
}

// executeOpenAI dispatches to the correct OpenAI executor based on endpoint.
func executeOpenAI(ctx context.Context, spec LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
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
func executeChatCompletions(ctx context.Context, spec LlmSpanSpec, client openai.Client) error {
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
func executeResponses(ctx context.Context, spec LlmSpanSpec, client openai.Client) error {
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

// executeAnthropic dispatches to the Anthropic executor.
func executeAnthropic(ctx context.Context, spec LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
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
func executeAnthropicMessages(ctx context.Context, spec LlmSpanSpec, client anthropic.Client) error {
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

// --- AWS Bedrock executor ---

// bedrockRegion is the AWS region used for Bedrock cassettes. Both record and
// replay must use the same region because go-vcr matches on method+URL, and
// the URL includes the regional hostname.
const bedrockRegion = "us-east-2"

// executeBedrock dispatches to the correct Bedrock executor based on endpoint.
func executeBedrock(ctx context.Context, spec LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = bedrockRegion
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(httpClient),
	}
	if vcr.GetVCRMode() == vcr.ModeReplay {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("AKIAFAKE", "fake", ""),
		))
	}

	// Disable retries — VCR records a single request/response pair, and
	// retries would produce requests that don't match the cassette.
	opts = append(opts, awsconfig.WithRetryMaxAttempts(1))

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	client := bedrockruntime.NewFromConfig(cfg,
		tracebedrockruntime.NewMiddleware(tracebedrockruntime.WithTracerProvider(tp)),
	)

	switch {
	case strings.HasSuffix(spec.Endpoint, "/converse"):
		return executeBedrockConverse(ctx, spec, client)
	case strings.HasSuffix(spec.Endpoint, "/converse-stream"):
		return executeBedrockConverseStream(ctx, spec, client)
	default:
		return fmt.Errorf("unsupported Bedrock endpoint: %s", spec.Endpoint)
	}
}

// executeBedrockConverse handles non-streaming Bedrock Converse calls.
func executeBedrockConverse(ctx context.Context, spec LlmSpanSpec, client *bedrockruntime.Client) error {
	for _, req := range spec.Requests {
		modelID := stringFromMap(req, "modelId")
		messages := buildBedrockMessages(req)

		input := &bedrockruntime.ConverseInput{
			ModelId:  &modelID,
			Messages: messages,
		}

		_, err := client.Converse(ctx, input)
		if err != nil {
			return fmt.Errorf("bedrock converse: %w", err)
		}
	}
	return nil
}

// executeBedrockConverseStream handles streaming Bedrock ConverseStream calls.
func executeBedrockConverseStream(ctx context.Context, spec LlmSpanSpec, client *bedrockruntime.Client) error {
	for _, req := range spec.Requests {
		modelID := stringFromMap(req, "modelId")
		messages := buildBedrockMessages(req)

		input := &bedrockruntime.ConverseStreamInput{
			ModelId:  &modelID,
			Messages: messages,
		}

		out, err := client.ConverseStream(ctx, input)
		if err != nil {
			return fmt.Errorf("bedrock converse stream: %w", err)
		}

		// Consume the stream to trigger span finalization.
		for ev := range out.GetStream().Events() {
			_ = ev // Drain all events.
		}
		if err := out.GetStream().Close(); err != nil {
			return fmt.Errorf("closing bedrock stream: %w", err)
		}
	}
	return nil
}

// buildBedrockMessages converts spec request messages to Bedrock typed messages.
func buildBedrockMessages(req map[string]any) []brtypes.Message {
	rawMsgs, ok := req["messages"].([]any)
	if !ok {
		return nil
	}

	var messages []brtypes.Message
	for _, rm := range rawMsgs {
		mm, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		msg := brtypes.Message{
			Role: brtypes.ConversationRole(stringFromMap(mm, "role")),
		}
		if rawContent, ok := mm["content"].([]any); ok {
			for _, rc := range rawContent {
				block := buildBedrockContentBlock(rc)
				if block != nil {
					msg.Content = append(msg.Content, block)
				}
			}
		}
		messages = append(messages, msg)
	}
	return messages
}

// buildBedrockContentBlock converts a spec content block to a Bedrock typed content block.
func buildBedrockContentBlock(raw any) brtypes.ContentBlock {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	// Text block: {text: "..."}
	if text, ok := m["text"].(string); ok {
		return &brtypes.ContentBlockMemberText{Value: text}
	}

	// Image block: {image: {format: "png", source: {bytes: "<base64>"}}}
	if img, ok := m["image"].(map[string]any); ok {
		format := stringFromMap(img, "format")
		if src, ok := img["source"].(map[string]any); ok {
			if b64, ok := src["bytes"].(string); ok {
				data, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					return nil
				}
				return &brtypes.ContentBlockMemberImage{
					Value: brtypes.ImageBlock{
						Format: brtypes.ImageFormat(format),
						Source: &brtypes.ImageSourceMemberBytes{Value: data},
					},
				}
			}
		}
	}

	return nil
}

// --- Google/Gemini executor ---

// executeGoogle dispatches to the correct Google executor based on endpoint.
func executeGoogle(ctx context.Context, spec LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
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
func executeGenerateContent(ctx context.Context, spec LlmSpanSpec, client *genai.Client) error {
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
