// Package genkit provides OpenTelemetry tracing middleware for Firebase Genkit.
//
// With orchestrion, tracing is injected automatically into all Genkit Generate calls.
// For manual instrumentation, pass NewMiddleware to ai.WithMiddleware:
//
//	resp, err := genkit.Generate(ctx, g,
//		ai.WithPrompt("Hello!"),
//		ai.WithMiddleware(tracegenkit.NewMiddleware()),
//	)
package genkit

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/google/uuid"
	openaigo "github.com/openai/openai-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// Option configures the Genkit tracing middleware.
type Option func(*middlewareConfig)

// WithTracerProvider sets a custom TracerProvider for the middleware.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(cfg *middlewareConfig) { cfg.tracer = tp.Tracer("braintrust") }
}

// WithLogger sets a custom logger for the middleware.
func WithLogger(log logger.Logger) Option {
	return func(cfg *middlewareConfig) {
		if log != nil {
			cfg.logger = log
		}
	}
}

// WithModel supplies the model identifier when the Genkit provider does not
// expose it on ModelRequest or ModelResponse.
func WithModel(model string) Option {
	return func(cfg *middlewareConfig) { cfg.model = model }
}

// WithProvider supplies the underlying model provider when Genkit cannot infer
// it. Use the provider whose pricing applies, not "genkit".
func WithProvider(provider string) Option {
	return func(cfg *middlewareConfig) { cfg.provider = provider }
}

type middlewareConfig struct {
	logger   logger.Logger
	tracer   trace.Tracer
	model    string
	provider string

	agentMu sync.Mutex
	agents  map[string]*agentState
}

type agentState struct {
	mu        sync.Mutex
	span      trace.Span
	traceID   string
	metrics   map[string]float64
	pending   map[string]struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// NewMiddleware creates Genkit model middleware that emits Braintrust spans.
func NewMiddleware(opts ...Option) ai.ModelMiddleware {
	cfg := &middlewareConfig{
		logger: logger.Discard(),
		tracer: otel.GetTracerProvider().Tracer("braintrust"),
		agents: make(map[string]*agentState),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next ai.ModelFunc) ai.ModelFunc {
		return func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return traceGenerate(ctx, cfg, next, req, cb)
		}
	}
}

func traceGenerate(ctx context.Context, cfg *middlewareConfig, next ai.ModelFunc, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	agentKey, agent := prepareAgent(ctx, cfg, req)
	spanContext := ctx
	if agent != nil {
		spanContext = trace.ContextWithSpan(ctx, agent.span)
	}

	ctx, span := cfg.tracer.Start(spanContext, "genkit.generate", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	setJSONAttr(cfg, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	if input := cleanupForInput(req); input != nil {
		setJSONAttr(cfg, span, "braintrust.input_json", input)
	}
	metadata := requestMetadata(cfg, req)

	var ttft time.Duration
	wrappedCB := cb
	if cb != nil {
		enableStreamUsage(req)
		var once sync.Once
		startTime := time.Now()
		wrappedCB = func(ctx context.Context, chunk *ai.ModelResponseChunk) error {
			once.Do(func() { ttft = time.Since(startTime) })
			return cb(ctx, chunk)
		}
	}

	resp, err := next(ctx, req, wrappedCB)
	if err != nil {
		ensureToolCallRefs(resp)
		if output := cleanupForOutput(resp); output != nil {
			setJSONAttr(cfg, span, "braintrust.output_json", output)
		}
		metrics := extractMetrics(resp, ttft)
		if len(metrics) > 0 {
			setJSONAttr(cfg, span, "braintrust.metrics", metrics)
		}
		for key, value := range responseMetadata(cfg, resp) {
			metadata[key] = value
		}
		setJSONAttr(cfg, span, "braintrust.metadata", metadata)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		finishAgentError(cfg, agentKey, agent, err, metrics)
		return resp, err
	}

	ensureToolCallRefs(resp)
	if output := cleanupForOutput(resp); output != nil {
		setJSONAttr(cfg, span, "braintrust.output_json", output)
	}
	metrics := extractMetrics(resp, ttft)
	if len(metrics) > 0 {
		setJSONAttr(cfg, span, "braintrust.metrics", metrics)
	}
	for key, value := range responseMetadata(cfg, resp) {
		metadata[key] = value
	}
	setJSONAttr(cfg, span, "braintrust.metadata", metadata)

	if agent != nil {
		addAgentMetrics(agent, metrics)
		toolRequests := resp.ToolRequests()
		if len(toolRequests) == 0 {
			finishAgent(cfg, agentKey, agent, resp)
		} else {
			updateAgentPending(agent, toolRequests)
		}
	}
	return resp, nil
}

func setJSONAttr(cfg *middlewareConfig, span trace.Span, key string, value any) {
	if err := internal.SetJSONAttr(span, key, value); err != nil {
		cfg.logger.Debug("Failed to set JSON span attribute", "key", key, "error", err)
	}
}

// prepareAgent promotes Genkit's outer generate span to a Braintrust task when
// tools are available. Continuations are matched by tool-call reference rather
// than trace ID so concurrent agent runs in one trace remain independent.
func prepareAgent(ctx context.Context, cfg *middlewareConfig, req *ai.ModelRequest) (string, *agentState) {
	if req == nil || len(req.Tools) == 0 {
		return "", nil
	}
	parent := trace.SpanFromContext(ctx)
	spanContext := parent.SpanContext()
	if !spanContext.IsValid() {
		return "", nil
	}
	traceID := spanContext.TraceID().String()

	cfg.agentMu.Lock()
	if refs := latestToolResponseRefs(req); len(refs) > 0 {
		for key, state := range cfg.agents {
			if state.traceID == traceID && state.matchesAny(refs) {
				cfg.agentMu.Unlock()
				return key, state
			}
		}
	}

	key := spanContext.SpanID().String()
	if state := cfg.agents[key]; state != nil {
		cfg.agentMu.Unlock()
		return key, state
	}
	state := &agentState{
		span:    parent,
		traceID: traceID,
		metrics: make(map[string]float64),
		pending: make(map[string]struct{}),
		done:    make(chan struct{}),
	}
	cfg.agents[key] = state
	cfg.agentMu.Unlock()

	go watchAgentLifecycle(cfg, key, state)
	setJSONAttr(cfg, parent, "braintrust.span_attributes", map[string]string{
		"name": "genkit.generate",
		"type": "task",
	})
	if input := cleanupForInput(req); input != nil {
		setJSONAttr(cfg, parent, "braintrust.input_json", input)
	}
	return key, state
}

func latestToolResponseRefs(req *ai.ModelRequest) []string {
	responses := latestToolResponses(req)
	refs := make([]string, 0, len(responses))
	for _, response := range responses {
		if response != nil && response.Ref != "" {
			refs = append(refs, response.Ref)
		}
	}
	return refs
}

func (agent *agentState) matchesAny(refs []string) bool {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	for _, ref := range refs {
		if _, ok := agent.pending[ref]; ok {
			return true
		}
	}
	return false
}

func updateAgentPending(agent *agentState, requests []*ai.ToolRequest) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	clear(agent.pending)
	for _, request := range requests {
		if request != nil && request.Ref != "" {
			agent.pending[request.Ref] = struct{}{}
		}
	}
}

func watchAgentLifecycle(cfg *middlewareConfig, key string, agent *agentState) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-agent.done:
			return
		case <-ticker.C:
			if !agent.span.IsRecording() {
				removeAgent(cfg, key, agent)
				return
			}
		}
	}
}

func removeAgent(cfg *middlewareConfig, key string, agent *agentState) {
	cfg.agentMu.Lock()
	if cfg.agents[key] == agent {
		delete(cfg.agents, key)
	}
	cfg.agentMu.Unlock()
	agent.closeOnce.Do(func() { close(agent.done) })
}

func addAgentMetrics(agent *agentState, metrics map[string]float64) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	for _, key := range []string{"prompt_tokens", "completion_tokens", "tokens"} {
		if value, ok := metrics[key]; ok {
			agent.metrics[key] += value
		}
	}
}

func writeAgentMetrics(cfg *middlewareConfig, agent *agentState) {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.metrics) > 0 {
		setJSONAttr(cfg, agent.span, "braintrust.metrics", agent.metrics)
	}
}

func finishAgent(cfg *middlewareConfig, key string, agent *agentState, resp *ai.ModelResponse) {
	if resp != nil && resp.Message != nil {
		setJSONAttr(cfg, agent.span, "braintrust.output_json", messageContent(resp.Message))
	}
	writeAgentMetrics(cfg, agent)
	removeAgent(cfg, key, agent)
}

func finishAgentError(cfg *middlewareConfig, key string, agent *agentState, err error, metrics map[string]float64) {
	if agent == nil {
		return
	}
	addAgentMetrics(agent, metrics)
	writeAgentMetrics(cfg, agent)
	agent.span.RecordError(err)
	agent.span.SetStatus(codes.Error, err.Error())
	removeAgent(cfg, key, agent)
}

func latestToolResponses(req *ai.ModelRequest) []*ai.ToolResponse {
	if req == nil {
		return nil
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		var responses []*ai.ToolResponse
		for _, part := range req.Messages[i].Content {
			if part != nil && part.ToolResponse != nil {
				responses = append(responses, part.ToolResponse)
			}
		}
		if len(responses) > 0 {
			return responses
		}
	}
	return nil
}

// cleanupForInput converts Genkit messages to canonical OpenAI messages.
func cleanupForInput(req *ai.ModelRequest) any {
	if req == nil || len(req.Messages) == 0 {
		return nil
	}
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		messages = append(messages, canonicalMessages(msg)...)
	}
	if len(messages) == 0 {
		return nil
	}
	return messages
}

func canonicalMessages(msg *ai.Message) []map[string]any {
	if msg == nil {
		return nil
	}
	var toolResponses []*ai.ToolResponse
	var toolRequests []*ai.ToolRequest
	var contentParts []*ai.Part
	for _, part := range msg.Content {
		switch {
		case part == nil:
		case part.ToolRequest != nil:
			toolRequests = append(toolRequests, part.ToolRequest)
		case part.ToolResponse != nil:
			toolResponses = append(toolResponses, part.ToolResponse)
		default:
			contentParts = append(contentParts, part)
		}
	}

	var messages []map[string]any
	if len(contentParts) > 0 || len(toolRequests) > 0 || len(toolResponses) == 0 {
		content := partsContent(contentParts)
		if len(toolRequests) > 0 && len(contentParts) == 0 {
			content = nil
		}
		message := map[string]any{"role": openAIRole(msg.Role), "content": content}
		if len(toolRequests) > 0 {
			calls := make([]map[string]any, 0, len(toolRequests))
			for _, request := range toolRequests {
				calls = append(calls, toolCall(request))
			}
			message["tool_calls"] = calls
		}
		messages = append(messages, message)
	}
	for _, response := range toolResponses {
		messages = append(messages, map[string]any{
			"role":         "tool",
			"tool_call_id": response.Ref,
			"content":      jsonString(response.Output),
		})
	}
	return messages
}

// cleanupForOutput emits OpenAI Chat Completions choices.
func cleanupForOutput(resp *ai.ModelResponse) any {
	if resp == nil || resp.Message == nil {
		return nil
	}
	message := canonicalMessages(resp.Message)
	if len(message) == 0 {
		return nil
	}
	return []map[string]any{{
		"index":         float64(0),
		"finish_reason": finishReason(resp),
		"message":       message[0],
	}}
}

func finishReason(resp *ai.ModelResponse) string {
	if len(resp.ToolRequests()) > 0 {
		return "tool_calls"
	}
	switch resp.FinishReason {
	case ai.FinishReasonStop, "":
		return "stop"
	case ai.FinishReasonLength:
		return "length"
	case ai.FinishReasonBlocked:
		return "content_filter"
	default:
		return string(resp.FinishReason)
	}
}

func toolCall(request *ai.ToolRequest) map[string]any {
	return map[string]any{
		"id":   request.Ref,
		"type": "function",
		"function": map[string]any{
			"name":      request.Name,
			"arguments": jsonString(request.Input),
		},
	}
}

func ensureToolCallRefs(resp *ai.ModelResponse) {
	if resp == nil || resp.Message == nil {
		return
	}
	for _, part := range resp.Message.Content {
		if part == nil || part.ToolRequest == nil || part.ToolRequest.Ref != "" {
			continue
		}
		part.ToolRequest.Ref = "call_" + uuid.NewString()
	}
}

func jsonString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func openAIRole(role ai.Role) string {
	if role == ai.RoleModel {
		return "assistant"
	}
	return string(role)
}

func messageContent(msg *ai.Message) any {
	messages := canonicalMessages(msg)
	if len(messages) == 0 {
		return nil
	}
	if len(messages) == 1 {
		return messages[0]["content"]
	}
	return messages
}

func partsContent(parts []*ai.Part) any {
	if len(parts) == 1 && parts[0] != nil && parts[0].IsText() {
		return parts[0].Text
	}
	blocks := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		if part == nil {
			continue
		}
		switch {
		case part.IsText(), part.IsData():
			blocks = append(blocks, map[string]any{"type": "text", "text": part.Text})
		case part.IsImage():
			blocks = append(blocks, map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": part.Text},
			})
		case part.IsMedia():
			blocks = append(blocks, map[string]any{
				"type": "file", "file": map[string]any{
					"filename":  attachmentFilename(part.ContentType),
					"file_data": part.Text,
				},
			})
		case part.IsReasoning():
			block := map[string]any{"type": "reasoning", "text": part.Text}
			if signature, ok := part.Metadata["signature"]; ok {
				block["signature"] = signature
			}
			blocks = append(blocks, block)
		case part.IsResource() && part.Resource != nil:
			blocks = append(blocks, map[string]any{
				"type": "file", "file": map[string]any{
					"filename":  "attachment",
					"file_data": part.Resource.Uri,
				},
			})
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return blocks
}

func enableStreamUsage(req *ai.ModelRequest) {
	if req == nil {
		return
	}
	switch config := req.Config.(type) {
	case *openaigo.ChatCompletionNewParams:
		cloned := *config
		cloned.StreamOptions.IncludeUsage = openaigo.Bool(true)
		req.Config = &cloned
	case openaigo.ChatCompletionNewParams:
		config.StreamOptions.IncludeUsage = openaigo.Bool(true)
		req.Config = config
	}
}

// extractMetrics emits only metrics allowed by the instrumentation spec.
func extractMetrics(resp *ai.ModelResponse, ttft time.Duration) map[string]float64 {
	metrics := make(map[string]float64)
	if usage, ok := googleUsage(resp); ok {
		metrics = usage
	} else if resp != nil && resp.Usage != nil {
		usage := resp.Usage
		promptKnown := usage.InputTokens > 0
		completionKnown := usage.OutputTokens > 0
		if promptKnown {
			metrics["prompt_tokens"] = float64(usage.InputTokens)
		}
		if completionKnown {
			metrics["completion_tokens"] = float64(usage.OutputTokens)
		}
		if usage.TotalTokens > 0 {
			metrics["tokens"] = float64(usage.TotalTokens)
		} else if promptKnown || completionKnown {
			metrics["tokens"] = float64(usage.InputTokens + usage.OutputTokens)
		}
		if usage.CachedContentTokens > 0 {
			metrics["prompt_cached_tokens"] = float64(usage.CachedContentTokens)
		}
		if usage.ThoughtsTokens > 0 {
			metrics["completion_reasoning_tokens"] = float64(usage.ThoughtsTokens)
		}
		if audio := usage.Custom["audioTokens"]; audio > 0 {
			metrics["completion_audio_tokens"] = audio
		}
	}
	if ttft > 0 {
		metrics["time_to_first_token"] = ttft.Seconds()
	}
	return metrics
}

func googleUsage(resp *ai.ModelResponse) (map[string]float64, bool) {
	if resp == nil {
		return nil, false
	}
	custom, ok := normalizeJSON(resp.Custom).(map[string]any)
	if !ok {
		return nil, false
	}
	usage, ok := custom["usageMetadata"].(map[string]any)
	if !ok {
		return nil, false
	}
	metrics := make(map[string]float64)
	prompt, promptOK := numericField(usage, "promptTokenCount")
	toolPrompt, toolPromptOK := numericField(usage, "toolUsePromptTokenCount")
	if promptOK || toolPromptOK {
		metrics["prompt_tokens"] = prompt + toolPrompt
	}
	completion, completionOK := numericField(usage, "candidatesTokenCount")
	thoughts, thoughtsOK := numericField(usage, "thoughtsTokenCount")
	if completionOK || thoughtsOK {
		metrics["completion_tokens"] = completion + thoughts
	}
	if thoughtsOK {
		metrics["completion_reasoning_tokens"] = thoughts
	}
	if total, ok := numericField(usage, "totalTokenCount"); ok {
		metrics["tokens"] = total
	}
	if cached, ok := numericField(usage, "cachedContentTokenCount"); ok {
		metrics["prompt_cached_tokens"] = cached
	}
	if audio, ok := modalityTokens(usage, "promptTokensDetails", "AUDIO"); ok {
		metrics["prompt_audio_tokens"] = audio
	}
	if audio, ok := modalityTokens(usage, "candidatesTokensDetails", "AUDIO"); ok {
		metrics["completion_audio_tokens"] = audio
	}
	if images, ok := modalityTokens(usage, "candidatesTokensDetails", "IMAGE"); ok {
		metrics["completion_image_tokens"] = images
	}
	return metrics, true
}

func numericField(m map[string]any, key string) (float64, bool) {
	value, ok := m[key]
	if !ok {
		return 0, false
	}
	return toFloat(value)
}

func modalityTokens(usage map[string]any, field, modality string) (float64, bool) {
	details, ok := usage[field].([]any)
	if !ok {
		return 0, false
	}
	var total float64
	var found bool
	for _, detail := range details {
		item, ok := detail.(map[string]any)
		if !ok || !strings.EqualFold(fmt.Sprint(item["modality"]), modality) {
			continue
		}
		if count, ok := numericField(item, "tokenCount"); ok {
			total += count
			found = true
		}
	}
	return total, found
}

func requestMetadata(cfg *middlewareConfig, req *ai.ModelRequest) map[string]any {
	metadata := map[string]any{}
	if cfg.model != "" {
		metadata["model"] = cfg.model
	}
	if cfg.provider != "" {
		metadata["provider"] = cfg.provider
	}
	if req == nil {
		return metadata
	}
	if req.ToolChoice != "" {
		metadata["tool_choice"] = string(req.ToolChoice)
	} else if len(req.Tools) > 0 {
		metadata["tool_choice"] = "auto"
	}
	if tools := toolDefinitions(req.Tools); len(tools) > 0 {
		metadata["tools"] = tools
	}
	if req.Output != nil && req.Output.Format != "" {
		metadata["response_format"] = string(req.Output.Format)
	}
	for key, value := range configMetadata(req.Config) {
		metadata[key] = value
	}
	if _, ok := metadata["provider"]; !ok {
		if provider := inferProvider(req.Config, fmt.Sprint(metadata["model"])); provider != "" {
			metadata["provider"] = provider
		}
	}
	return metadata
}

func toolDefinitions(definitions []*ai.ToolDefinition) []map[string]any {
	tools := make([]map[string]any, 0, len(definitions))
	for _, definition := range definitions {
		if definition == nil || definition.Name == "" {
			continue
		}
		function := map[string]any{"name": definition.Name}
		if definition.Description != "" {
			function["description"] = definition.Description
		}
		if definition.InputSchema != nil {
			function["parameters"] = normalizeJSON(definition.InputSchema)
		}
		tools = append(tools, map[string]any{"type": "function", "function": function})
	}
	return tools
}

func responseMetadata(cfg *middlewareConfig, resp *ai.ModelResponse) map[string]any {
	metadata := map[string]any{}
	if cfg.model != "" {
		metadata["model"] = cfg.model
	}
	if cfg.provider != "" {
		metadata["provider"] = cfg.provider
	}
	if resp == nil {
		return metadata
	}
	custom, _ := normalizeJSON(resp.Custom).(map[string]any)
	if model, ok := firstString(custom, "model", "modelVersion"); ok {
		metadata["model"] = model
	}
	if provider, ok := stringValue(custom["provider"]); ok {
		metadata["provider"] = provider
	} else if _, ok := custom["usageMetadata"]; ok {
		metadata["provider"] = "google"
	} else if _, ok := custom["systemFingerprint"]; ok {
		metadata["provider"] = "openai"
	}
	if _, ok := metadata["provider"]; !ok {
		if provider := inferProvider(nil, fmt.Sprint(metadata["model"])); provider != "" {
			metadata["provider"] = provider
		}
	}
	return metadata
}

func configMetadata(config any) map[string]any {
	configMap := normalizedConfig(config)
	metadata := map[string]any{}
	if model, ok := firstString(configMap, "model", "model_name", "modelName"); ok {
		metadata["model"] = model
	}
	if value, ok := firstFloat(configMap, "temperature", "Temperature"); ok {
		metadata["temperature"] = value
	}
	if value, ok := firstFloat(configMap, "top_p", "topP", "TopP"); ok {
		metadata["top_p"] = value
	}
	if value, ok := firstInt(configMap, "max_output_tokens", "maxOutputTokens", "max_completion_tokens", "maxCompletionTokens", "max_tokens", "maxTokens"); ok {
		metadata["max_tokens"] = value
	}
	if value, ok := firstValue(configMap, "stop_sequences", "stopSequences", "stop", "Stop"); ok {
		metadata["stop"] = value
	}
	if value, ok := firstValue(configMap, "response_format", "responseFormat"); ok {
		metadata["response_format"] = value
	}
	return metadata
}

func attachmentFilename(contentType string) string {
	extensions, _ := mime.ExtensionsByType(contentType)
	if len(extensions) == 0 {
		return "attachment"
	}
	return "attachment" + extensions[0]
}

func inferProvider(config any, model string) string {
	typeName := ""
	if config != nil {
		typeName = strings.ToLower(reflect.TypeOf(config).String())
	}
	model = strings.ToLower(model)
	switch {
	case strings.Contains(typeName, "openai"), strings.HasPrefix(model, "openai/"):
		return "openai"
	case strings.Contains(typeName, "genai"), strings.HasPrefix(model, "googleai/"), strings.HasPrefix(model, "gemini"):
		return "google"
	}
	if provider, _, ok := strings.Cut(model, "/"); ok {
		return provider
	}
	return ""
}

func normalizedConfig(config any) map[string]any {
	configMap, ok := normalizeJSON(config).(map[string]any)
	if !ok {
		return nil
	}
	return configMap
}

func normalizeJSON(value any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

func firstValue(m map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := m[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	value, ok := firstValue(m, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
}

func stringValue(value any) (string, bool) {
	str, ok := value.(string)
	return str, ok && str != ""
}

func firstFloat(m map[string]any, keys ...string) (float64, bool) {
	value, ok := firstValue(m, keys...)
	if !ok {
		return 0, false
	}
	return toFloat(value)
}

func firstInt(m map[string]any, keys ...string) (int, bool) {
	value, ok := firstFloat(m, keys...)
	return int(value), ok
}

func toFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, number >= 0
	case float32:
		return float64(number), number >= 0
	case int:
		return float64(number), number >= 0
	case int64:
		return float64(number), number >= 0
	default:
		return 0, false
	}
}
