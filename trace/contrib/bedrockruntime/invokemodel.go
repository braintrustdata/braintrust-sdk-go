package bedrockruntime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// invokeModelTracer handles the non-streaming InvokeModel operation.
//
// InvokeModel bodies are model-specific JSON. Anthropic Claude message bodies
// are normalized into allowlisted input, output, metadata, and metrics fields.
// Unknown provider-defined schemas are intentionally not copied wholesale.
type invokeModelTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
	modelID  string
}

func (t *invokeModelTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.invoke_model", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "invoke_model",
	}

	params, ok := in.(*bedrockruntime.InvokeModelInput)
	if !ok || params == nil {
		return ctx, span
	}
	if params.ModelId != nil {
		t.modelID = *params.ModelId
		t.metadata["model"] = t.modelID
	}
	if input, ok := normalizeInvokeModelInput(t.modelID, params.Body, t.metadata); ok {
		setJSONAttr(t.cfg.logger, span, "braintrust.input_json", input)
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)
	setJSONAttr(t.cfg.logger, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	return ctx, span
}

func (t *invokeModelTracer) TagOutput(span trace.Span, out any, _ time.Time) {
	defer span.End()

	resp, ok := out.(*bedrockruntime.InvokeModelOutput)
	if !ok || resp == nil {
		return
	}

	response := decodeClaudeResponse(t.modelID, resp.Body)
	if output, resolvedModel, ok := normalizeInvokeModelOutput(response); ok {
		setJSONAttr(t.cfg.logger, span, "braintrust.output_json", output)
		if resolvedModel != "" {
			t.metadata["model"] = resolvedModel
		}
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)

	metrics := make(map[string]any)
	for key, value := range extractUsageForModel(t.modelID, response) {
		metrics[key] = value
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metrics", metrics)
}

// invokeModelStreamTracer handles the streaming variant of InvokeModel.
//
// Body chunks arrive as raw JSON bytes in `*types.PayloadPart.Bytes`. We
// accumulate them as they pass through and parse Claude responses at stream
// end. Unknown provider-defined event schemas are not copied wholesale.
type invokeModelStreamTracer struct {
	cfg      *middlewareConfig
	metadata map[string]any
	modelID  string
}

func (t *invokeModelStreamTracer) StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span) {
	ctx, span := t.cfg.tracer().Start(ctx, "bedrock.invoke_model_stream", trace.WithTimestamp(start))

	t.metadata = map[string]any{
		"provider": "bedrock",
		"endpoint": "invoke_model_stream",
		"stream":   true,
	}

	params, ok := in.(*bedrockruntime.InvokeModelWithResponseStreamInput)
	if !ok || params == nil {
		return ctx, span
	}
	if params.ModelId != nil {
		t.modelID = *params.ModelId
		t.metadata["model"] = t.modelID
	}
	if input, ok := normalizeInvokeModelInput(t.modelID, params.Body, t.metadata); ok {
		setJSONAttr(t.cfg.logger, span, "braintrust.input_json", input)
	}
	setJSONAttr(t.cfg.logger, span, "braintrust.metadata", t.metadata)
	setJSONAttr(t.cfg.logger, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	return ctx, span
}

func (t *invokeModelStreamTracer) TagOutput(span trace.Span, out any, start time.Time) {
	resp, ok := out.(*bedrockruntime.InvokeModelWithResponseStreamOutput)
	if !ok || resp == nil || resp.GetStream() == nil || resp.GetStream().Reader == nil {
		span.End()
		return
	}
	stream := resp.GetStream()
	observed := &observedInvokeModelStream{
		log:         t.cfg.logger,
		inner:       stream.Reader,
		events:      make(chan types.ResponseStream),
		done:        make(chan struct{}),
		span:        span,
		start:       start,
		metadata:    t.metadata,
		modelID:     t.modelID,
		accumulator: internal.NewClaudeStreamAccumulator(),
	}
	stream.Reader = observed
	go observed.pump()
}

// observedInvokeModelStream keeps the span open until the response stream is
// drained or closed while transparently forwarding every provider event.
type observedInvokeModelStream struct {
	log    logger.Logger
	inner  bedrockruntime.ResponseStreamReader
	events chan types.ResponseStream
	done   chan struct{}

	closeOnce sync.Once
	finalOnce sync.Once

	span     trace.Span
	start    time.Time
	metadata map[string]any
	modelID  string

	mu           sync.Mutex
	ttftRecorded bool
	timeToFirst  time.Duration
	accumulator  *internal.ClaudeStreamAccumulator
}

func (o *observedInvokeModelStream) Events() <-chan types.ResponseStream { return o.events }

func (o *observedInvokeModelStream) Close() error {
	o.closeOnce.Do(func() { close(o.done) })
	err := o.inner.Close()
	o.finalize()
	return err
}

func (o *observedInvokeModelStream) Err() error { return o.inner.Err() }

func (o *observedInvokeModelStream) pump() {
	defer close(o.events)
	defer o.finalize()
	for event := range o.inner.Events() {
		o.observe(event)
		select {
		case o.events <- event:
		case <-o.done:
			return
		}
	}
}

func (o *observedInvokeModelStream) observe(event types.ResponseStream) {
	chunk, ok := event.(*types.ResponseStreamMemberChunk)
	if !ok || len(chunk.Value.Bytes) == 0 || !isClaudeModel(o.modelID) {
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal(chunk.Value.Bytes, &decoded); err != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.accumulator.Add(decoded) && !o.ttftRecorded {
		o.ttftRecorded = true
		o.timeToFirst = time.Since(o.start)
	}
}

func (o *observedInvokeModelStream) finalize() {
	o.finalOnce.Do(func() {
		o.mu.Lock()
		defer o.mu.Unlock()

		output := o.accumulator.Output()
		if len(output) > 0 {
			output[0]["content"] = normalizeClaudeContent(output[0]["content"])
			if stopReason := o.accumulator.StopReason(); stopReason != nil {
				output[0]["stop_reason"] = stopReason
			}
			setJSONAttr(o.log, o.span, "braintrust.output_json", output)
		}
		if model := o.accumulator.Model(); model != "" {
			o.metadata["model"] = model
		}
		setJSONAttr(o.log, o.span, "braintrust.metadata", o.metadata)

		metrics := make(map[string]any)
		usage := map[string]any{"usage": o.accumulator.Usage()}
		if normalized := extractUsageForModel(o.modelID, usage); normalized != nil {
			for key, value := range normalized {
				metrics[key] = value
			}
		}
		if o.ttftRecorded {
			metrics["time_to_first_token"] = o.timeToFirst.Seconds()
		}
		setJSONAttr(o.log, o.span, "braintrust.metrics", metrics)

		if err := o.inner.Err(); err != nil {
			o.span.RecordError(err)
			o.span.SetStatus(codes.Error, err.Error())
		}
		o.span.End()
	})
}

// normalizeInvokeModelInput extracts Claude messages as the span input and
// selects only specification-approved request parameters for metadata.
func normalizeInvokeModelInput(modelID string, body []byte, metadata map[string]any) (any, bool) {
	if !isClaudeModel(modelID) {
		return nil, false
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, false
	}
	messages, ok := request["messages"].([]any)
	if !ok {
		return nil, false
	}

	for _, key := range []string{"temperature", "top_p", "max_tokens"} {
		if value, exists := request[key]; exists {
			metadata[key] = value
		}
	}
	if stop, exists := request["stop_sequences"]; exists {
		metadata["stop"] = stop
	}
	if tools := normalizeClaudeTools(request["tools"]); len(tools) > 0 {
		metadata["tools"] = tools
	}
	if choice := normalizeClaudeToolChoice(request["tool_choice"]); choice != nil {
		metadata["tool_choice"] = choice
	}

	input := make([]any, 0, len(messages)+1)
	if system, exists := request["system"]; exists {
		input = append(input, map[string]any{"role": "system", "content": system})
	}
	input = append(input, messages...)
	return input, true
}

// normalizeInvokeModelOutput selects the Claude assistant message fields and
// returns the model resolved by the provider response.
func normalizeInvokeModelOutput(response map[string]any) (output any, resolvedModel string, ok bool) {
	if response == nil {
		return nil, "", false
	}
	content, exists := response["content"]
	if !exists {
		return nil, "", false
	}
	message := map[string]any{"role": "assistant", "content": normalizeClaudeContent(content)}
	if stopReason, exists := response["stop_reason"]; exists {
		message["stop_reason"] = stopReason
	}
	if model, isString := response["model"].(string); isString {
		resolvedModel = model
	}
	return []any{message}, resolvedModel, true
}

func normalizeClaudeContent(value any) any {
	content, ok := value.([]any)
	if !ok {
		return value
	}
	result := make([]any, 0, len(content))
	for _, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok || block["type"] != "thinking" {
			result = append(result, rawBlock)
			continue
		}
		reasoning := map[string]any{"type": "reasoning"}
		if text, exists := block["thinking"]; exists {
			reasoning["text"] = text
		}
		if signature, exists := block["signature"]; exists {
			reasoning["signature"] = signature
		}
		result = append(result, reasoning)
	}
	return result
}

func normalizeClaudeTools(value any) []any {
	tools, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]any, 0, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		function := map[string]any{}
		for _, key := range []string{"name", "description"} {
			if value, exists := tool[key]; exists {
				function[key] = value
			}
		}
		if parameters, exists := tool["input_schema"]; exists {
			function["parameters"] = parameters
		}
		if strict, exists := tool["strict"]; exists {
			function["strict"] = strict
		}
		result = append(result, map[string]any{"type": "function", "function": function})
	}
	return result
}

func normalizeClaudeToolChoice(value any) any {
	choice, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	switch choice["type"] {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		name, _ := choice["name"].(string)
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	default:
		return nil
	}
}

func isClaudeModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "anthropic.claude")
}

func decodeClaudeResponse(modelID string, body []byte) map[string]any {
	if !isClaudeModel(modelID) {
		return nil
	}
	var response map[string]any
	if err := json.Unmarshal(body, &response); err != nil {
		return nil
	}
	return response
}

// extractUsageForModel normalizes token metrics from an already-decoded
// Claude response body. Exported to the package-level test for coverage of
// the dispatch logic.
func extractUsageForModel(modelID string, body any) map[string]any {
	if body == nil {
		return nil
	}
	if !isClaudeModel(modelID) {
		return nil
	}
	m, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := m["usage"].(map[string]any)
	if !ok {
		return nil
	}

	// Reuse the anthropic-style map-based normalization.
	metrics := make(map[string]any)
	var input, cacheRead, cacheCreate int64
	hasPromptUsage := false
	for k, v := range usage {
		ok, i := internal.ToInt64(v)
		if !ok || i < 0 {
			continue
		}
		switch k {
		case "input_tokens":
			input = i
			hasPromptUsage = true
		case "cache_creation_input_tokens":
			cacheCreate = i
			hasPromptUsage = true
			metrics["prompt_cache_creation_tokens"] = i
		case "cache_read_input_tokens":
			cacheRead = i
			hasPromptUsage = true
			metrics["prompt_cached_tokens"] = i
		case "output_tokens":
			metrics["completion_tokens"] = i
		}
	}
	if cacheDetails, ok := usage["cache_creation"].(map[string]any); ok {
		if ok, value := internal.ToInt64(cacheDetails["ephemeral_5m_input_tokens"]); ok && value >= 0 {
			metrics["prompt_cache_creation_5m_tokens"] = value
		}
		if ok, value := internal.ToInt64(cacheDetails["ephemeral_1h_input_tokens"]); ok && value >= 0 {
			metrics["prompt_cache_creation_1h_tokens"] = value
		}
	}
	if hasPromptUsage {
		promptTotal := input + cacheRead + cacheCreate
		metrics["prompt_tokens"] = promptTotal
		if completion, ok := metrics["completion_tokens"].(int64); ok {
			metrics["tokens"] = promptTotal + completion
		}
	}
	return metrics
}
