// Package eino provides OpenTelemetry tracing for CloudWeGo Eino applications.
//
// Eino is a Go LLM application framework from CloudWeGo. This package
// implements the eino callbacks.Handler interface to capture LLM invocations
// as Braintrust-compatible OpenTelemetry spans.
//
// Usage — register the handler globally before any graph executions:
//
//	import (
//		"go.opentelemetry.io/otel/sdk/trace"
//		"github.com/cloudwego/eino/callbacks"
//		traceeino "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/cloudwego/eino"
//	)
//
//	tp := trace.NewTracerProvider(...)
//	handler := traceeino.NewHandlerWithOptions(traceeino.HandlerOptions{
//		TracerProvider: tp,
//	})
//	callbacks.AppendGlobalHandlers(handler)
//
// Alternatively, register per-invocation using compose.WithCallbacks:
//
//	result, err := runner.Run(
//		compose.WithCallbacks(ctx, handler),
//		input,
//	)
package eino

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	traceinternal "github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// spanKey is the context key used to store the active span between OnStart and OnEnd.
type spanKey struct{}

// startTimeKey stores the span start time for time_to_first_token measurement.
type startTimeKey struct{}

// Handler implements the CloudWeGo Eino callbacks.Handler interface to provide
// OpenTelemetry tracing for Eino LLM applications.
type Handler struct {
	opts HandlerOptions
	wg   sync.WaitGroup // tracks in-flight streaming goroutines
}

// Wait blocks until all in-flight streaming goroutines have completed.
// Call this after streaming calls to ensure spans are fully populated
// before the TracerProvider is shut down or spans are read.
func (h *Handler) Wait() {
	h.wg.Wait()
}

// HandlerOptions configures the Handler.
type HandlerOptions struct {
	// TracerProvider is an optional custom TracerProvider for testing or custom configurations.
	// If not provided, the global otel.GetTracerProvider() is used.
	TracerProvider trace.TracerProvider
}

// defaultHandler is the package-level singleton used by orchestrion and DefaultHandler().
var defaultHandler = &Handler{}

// DefaultHandler returns the package-level singleton Handler. Use this when
// you need a reference to the handler that orchestrion injects, e.g. to call
// Wait() before shutting down the TracerProvider.
func DefaultHandler() *Handler {
	return defaultHandler
}

// NewHandler creates a new Handler for tracing Eino operations using the global TracerProvider.
func NewHandler() *Handler {
	return &Handler{}
}

// NewHandlerWithOptions creates a new Handler with custom options.
func NewHandlerWithOptions(opts HandlerOptions) *Handler {
	return &Handler{opts: opts}
}

func (h *Handler) tracer() trace.Tracer {
	tp := h.opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// OnStart is called before a component begins processing. It creates a span for
// ChatModel and Tool components, capturing their inputs.
func (h *Handler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	if modelInput := model.ConvCallbackInput(input); modelInput != nil {
		return h.onStartModel(ctx, info, modelInput)
	}
	if toolInput := tool.ConvCallbackInput(input); toolInput != nil {
		return h.onStartTool(ctx, info, toolInput)
	}
	if embInput := embedding.ConvCallbackInput(input); embInput != nil {
		return h.onStartEmbedding(ctx, info, embInput)
	}
	return ctx
}

func (h *Handler) onStartModel(ctx context.Context, info *callbacks.RunInfo, modelInput *model.CallbackInput) context.Context {
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))

	if err := traceinternal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		span.RecordError(err)
	}

	if len(modelInput.Messages) > 0 {
		msgs := convertMessages(modelInput.Messages)
		if err := traceinternal.SetJSONAttr(span, "braintrust.input_json", msgs); err != nil {
			span.RecordError(err)
		}
	}

	metadata := buildMetadata(info, modelInput.Config)
	if err := traceinternal.SetJSONAttr(span, "braintrust.metadata", metadata); err != nil {
		span.RecordError(err)
	}

	// OTel semantic conventions for GenAI
	if modelInput.Config != nil && modelInput.Config.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", modelInput.Config.Model))
	}

	childCtx = context.WithValue(childCtx, startTimeKey{}, time.Now())
	return context.WithValue(childCtx, spanKey{}, span)
}

func (h *Handler) onStartTool(ctx context.Context, info *callbacks.RunInfo, toolInput *tool.CallbackInput) context.Context {
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName)

	if err := traceinternal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "tool"}); err != nil {
		span.RecordError(err)
	}

	if toolInput.ArgumentsInJSON != "" {
		// ArgumentsInJSON is already serialized JSON, so set the attribute directly
		// to avoid double-encoding via SetJSONAttr (which would marshal again).
		span.SetAttributes(attribute.String("braintrust.input_json", toolInput.ArgumentsInJSON))
	}

	if info != nil && info.Name != "" {
		if err := traceinternal.SetJSONAttr(span, "braintrust.metadata", map[string]any{"name": info.Name}); err != nil {
			span.RecordError(err)
		}
	}

	return context.WithValue(childCtx, spanKey{}, span)
}

func (h *Handler) onStartEmbedding(ctx context.Context, info *callbacks.RunInfo, embInput *embedding.CallbackInput) context.Context {
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))

	if err := traceinternal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		span.RecordError(err)
	}

	if len(embInput.Texts) > 0 {
		if err := traceinternal.SetJSONAttr(span, "braintrust.input_json", embInput.Texts); err != nil {
			span.RecordError(err)
		}
	}

	metadata := buildEmbeddingMetadata(info, embInput.Config)
	if err := traceinternal.SetJSONAttr(span, "braintrust.metadata", metadata); err != nil {
		span.RecordError(err)
	}

	if embInput.Config != nil && embInput.Config.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", embInput.Config.Model))
	}

	return context.WithValue(childCtx, spanKey{}, span)
}

// OnEnd is called after a component returns successfully. It captures output
// and metrics for ChatModel and Tool components, then ends the span.
func (h *Handler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok {
		return ctx
	}
	defer span.End()

	if modelOutput := model.ConvCallbackOutput(output); modelOutput != nil {
		if modelOutput.Message != nil {
			out := convertMessageToOutput(modelOutput.Message)
			if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", out); err != nil {
				span.RecordError(err)
			}
			// OTel semantic conventions
			if modelOutput.Message.ResponseMeta != nil && modelOutput.Message.ResponseMeta.FinishReason != "" {
				span.SetAttributes(attribute.String("gen_ai.finish_reason", modelOutput.Message.ResponseMeta.FinishReason))
			}
		}
		if modelOutput.TokenUsage != nil {
			metrics := modelTokenUsageToMetrics(modelOutput.TokenUsage)
			if len(metrics) > 0 {
				if err := traceinternal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
					span.RecordError(err)
				}
			}
		}
		return ctx
	}

	if toolOutput := tool.ConvCallbackOutput(output); toolOutput != nil {
		if toolOutput.Response != "" {
			// Response may be JSON or plain text. If valid JSON, set directly
			// to avoid double-encoding via SetJSONAttr.
			if json.Valid([]byte(toolOutput.Response)) {
				span.SetAttributes(attribute.String("braintrust.output_json", toolOutput.Response))
			} else if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", toolOutput.Response); err != nil {
				span.RecordError(err)
			}
		}
		return ctx
	}

	if embOutput := embedding.ConvCallbackOutput(output); embOutput != nil {
		out := embeddingOutputSummary(embOutput.Embeddings)
		if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", out); err != nil {
			span.RecordError(err)
		}
		if embOutput.TokenUsage != nil {
			metrics := embeddingTokenUsageToMetrics(embOutput.TokenUsage)
			if len(metrics) > 0 {
				if err := traceinternal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
					span.RecordError(err)
				}
			}
		}
		return ctx
	}

	return ctx
}

// OnError is called when a component returns a non-nil error. It records the error
// and ends the span.
func (h *Handler) OnError(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok {
		return ctx
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
	span.End()
	return ctx
}

// OnStartWithStreamInput is called when a component receives streaming input
// (Collect / Transform paradigms, e.g. when a ChatModel is inside a graph that
// receives a stream). We consume the stream to collect the input, then create a
// span so that the subsequent OnEnd / OnEndWithStreamOutput can close it.
func (h *Handler) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo,
	input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	defer input.Close()

	// Consume the stream, keeping the last model/tool input.
	var lastModelInput *model.CallbackInput
	var lastToolInput *tool.CallbackInput
	for {
		item, err := input.Recv()
		if err != nil {
			break
		}
		if mi := model.ConvCallbackInput(item); mi != nil {
			lastModelInput = mi
		}
		if ti := tool.ConvCallbackInput(item); ti != nil {
			lastToolInput = ti
		}
	}

	// Dispatch to the same onStart* helpers used by OnStart.
	if lastModelInput != nil {
		return h.onStartModel(ctx, info, lastModelInput)
	}
	if lastToolInput != nil {
		return h.onStartTool(ctx, info, lastToolInput)
	}
	return ctx
}

// OnEndWithStreamOutput is called when a component produces streaming output.
// It consumes the stream in a background goroutine, then tags the span with the
// aggregated output and token usage before ending it.
func (h *Handler) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo,
	output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {

	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok {
		output.Close()
		return ctx
	}

	startTime, hasStartTime := ctx.Value(startTimeKey{}).(time.Time)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer output.Close()
		defer span.End()

		var chunks []*schema.Message
		var timeToFirstToken time.Duration
		for {
			item, err := output.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return
			}
			if timeToFirstToken == 0 && hasStartTime {
				timeToFirstToken = time.Since(startTime)
			}
			mo := model.ConvCallbackOutput(item)
			if mo != nil && mo.Message != nil {
				chunks = append(chunks, mo.Message)
			}
		}

		if len(chunks) == 0 {
			return
		}

		finalMsg, err := schema.ConcatMessages(chunks)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return
		}
		if finalMsg == nil {
			return
		}

		out := convertMessageToOutput(finalMsg)
		if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", out); err != nil {
			span.RecordError(err)
		}

		metrics := make(map[string]float64)
		if finalMsg.ResponseMeta != nil && finalMsg.ResponseMeta.Usage != nil {
			for k, v := range schemaTokenUsageToMetrics(finalMsg.ResponseMeta.Usage) {
				metrics[k] = float64(v)
			}
		}
		if timeToFirstToken > 0 {
			metrics["time_to_first_token"] = timeToFirstToken.Seconds()
		}
		if len(metrics) > 0 {
			if err := traceinternal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
				span.RecordError(err)
			}
		}
	}()

	return ctx
}

// Needed implements callbacks.TimingChecker so the framework only sets up
// stream-copy infrastructure for the timings we actually handle.
func (h *Handler) Needed(_ context.Context, _ *callbacks.RunInfo, timing callbacks.CallbackTiming) bool {
	switch timing {
	case callbacks.TimingOnStart,
		callbacks.TimingOnEnd,
		callbacks.TimingOnError,
		callbacks.TimingOnStartWithStreamInput,
		callbacks.TimingOnEndWithStreamOutput:
		return true
	default:
		return false
	}
}

// spanNameFromInfo derives a span name from RunInfo.
func spanNameFromInfo(info *callbacks.RunInfo) string {
	if info == nil {
		return "eino"
	}
	if info.Name != "" {
		return "eino." + info.Name
	}
	if info.Type != "" {
		return "eino." + info.Type
	}
	if string(info.Component) != "" {
		return "eino." + string(info.Component)
	}
	return "eino"
}

// buildMetadata constructs the braintrust.metadata map.
func buildMetadata(info *callbacks.RunInfo, cfg *model.Config) map[string]any {
	metadata := map[string]any{
		"provider": "cloudwego/eino",
	}

	if info != nil {
		// Use the implementation type (e.g. "OpenAI", "Anthropic") as the provider
		// when available, since it's more specific than the framework name.
		if info.Type != "" {
			metadata["provider"] = info.Type
		}
	}

	if cfg != nil {
		if cfg.Model != "" {
			metadata["model"] = cfg.Model
		}
		if cfg.MaxTokens != 0 {
			metadata["max_tokens"] = cfg.MaxTokens
		}
		// Eino uses plain float64 (not *float64), so we can't distinguish
		// "not set" from "explicitly set to 0". Accept dropping explicit 0.
		if cfg.Temperature != 0 {
			metadata["temperature"] = cfg.Temperature
		}
		if cfg.TopP != 0 {
			metadata["top_p"] = cfg.TopP
		}
		if len(cfg.Stop) > 0 {
			metadata["stop"] = cfg.Stop
		}
	}

	return metadata
}

// convertMessages converts a slice of schema.Message to the OpenAI-compatible format
// expected by the Braintrust UI.
func convertMessages(msgs []*schema.Message) []map[string]any {
	result := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		result = append(result, convertMessage(msg))
	}
	return result
}

// convertMessage converts a single schema.Message to a map in OpenAI format.
// Used for input messages — includes tool_call_id but not finish_reason.
// See convertMessageToOutput for the output counterpart.
func convertMessage(msg *schema.Message) map[string]any {
	m := map[string]any{
		"role":    string(msg.Role),
		"content": msg.Content,
	}
	if len(msg.ToolCalls) > 0 {
		m["tool_calls"] = convertToolCalls(msg.ToolCalls)
	}
	if msg.ToolCallID != "" {
		m["tool_call_id"] = msg.ToolCallID
	}
	return m
}

// convertMessageToOutput converts a response message to a map for braintrust.output_json.
// Used for output messages — includes finish_reason but not tool_call_id.
// See convertMessage for the input counterpart.
func convertMessageToOutput(msg *schema.Message) map[string]any {
	out := map[string]any{
		"role":    string(msg.Role),
		"content": msg.Content,
	}
	if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason != "" {
		out["finish_reason"] = msg.ResponseMeta.FinishReason
	}
	if len(msg.ToolCalls) > 0 {
		out["tool_calls"] = convertToolCalls(msg.ToolCalls)
	}
	return out
}

// convertToolCalls converts a slice of schema.ToolCall to OpenAI-compatible format.
func convertToolCalls(tcs []schema.ToolCall) []map[string]any {
	result := make([]map[string]any, len(tcs))
	for i, tc := range tcs {
		result[i] = map[string]any{
			"id":   tc.ID,
			"type": tc.Type,
			"function": map[string]any{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		}
	}
	return result
}

// modelTokenUsageToMetrics converts model.TokenUsage to the standard Braintrust metrics map.
func modelTokenUsageToMetrics(u *model.TokenUsage) map[string]int64 {
	metrics := make(map[string]int64)
	if u.PromptTokens > 0 {
		metrics["prompt_tokens"] = int64(u.PromptTokens)
	}
	if u.CompletionTokens > 0 {
		metrics["completion_tokens"] = int64(u.CompletionTokens)
	}
	if u.TotalTokens > 0 {
		metrics["tokens"] = int64(u.TotalTokens)
	}
	if u.PromptTokenDetails.CachedTokens > 0 {
		metrics["prompt_cached_tokens"] = int64(u.PromptTokenDetails.CachedTokens)
	}
	if u.CompletionTokensDetails.ReasoningTokens > 0 {
		metrics["completion_reasoning_tokens"] = int64(u.CompletionTokensDetails.ReasoningTokens)
	}
	return metrics
}

// schemaTokenUsageToMetrics converts schema.TokenUsage to the standard Braintrust metrics map.
// Separate from modelTokenUsageToMetrics because streaming (schema.TokenUsage) and
// non-streaming (model.TokenUsage) use different types with the same field layout.
func schemaTokenUsageToMetrics(u *schema.TokenUsage) map[string]int64 {
	metrics := make(map[string]int64)
	if u.PromptTokens > 0 {
		metrics["prompt_tokens"] = int64(u.PromptTokens)
	}
	if u.CompletionTokens > 0 {
		metrics["completion_tokens"] = int64(u.CompletionTokens)
	}
	if u.TotalTokens > 0 {
		metrics["tokens"] = int64(u.TotalTokens)
	}
	if u.PromptTokenDetails.CachedTokens > 0 {
		metrics["prompt_cached_tokens"] = int64(u.PromptTokenDetails.CachedTokens)
	}
	if u.CompletionTokensDetails.ReasoningTokens > 0 {
		metrics["completion_reasoning_tokens"] = int64(u.CompletionTokensDetails.ReasoningTokens)
	}
	return metrics
}

// buildEmbeddingMetadata constructs the braintrust.metadata map for embedding spans.
func buildEmbeddingMetadata(info *callbacks.RunInfo, cfg *embedding.Config) map[string]any {
	metadata := map[string]any{
		"provider": "cloudwego/eino",
	}
	if info != nil && info.Type != "" {
		metadata["provider"] = info.Type
	}
	if cfg != nil {
		if cfg.Model != "" {
			metadata["model"] = cfg.Model
		}
		if cfg.EncodingFormat != "" {
			metadata["encoding_format"] = cfg.EncodingFormat
		}
	}
	return metadata
}

// embeddingOutputSummary mirrors the Python SDK convention for multi-input
// embedding calls: {"embedding_length": N, "embeddings_count": M}.
func embeddingOutputSummary(embeddings [][]float64) map[string]any {
	out := map[string]any{
		"embeddings_count": len(embeddings),
	}
	if len(embeddings) > 0 {
		out["embedding_length"] = len(embeddings[0])
	}
	return out
}

// embeddingTokenUsageToMetrics converts embedding.TokenUsage into the standard
// Braintrust metrics map. Embeddings have no completion tokens.
func embeddingTokenUsageToMetrics(u *embedding.TokenUsage) map[string]int64 {
	metrics := make(map[string]int64)
	if u.PromptTokens > 0 {
		metrics["prompt_tokens"] = int64(u.PromptTokens)
	}
	if u.TotalTokens > 0 {
		metrics["tokens"] = int64(u.TotalTokens)
	}
	return metrics
}

// Compile-time assertion that Handler implements the required interfaces.
var _ callbacks.Handler = (*Handler)(nil)
var _ callbacks.TimingChecker = (*Handler)(nil)
