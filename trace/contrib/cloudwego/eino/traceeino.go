// Package eino provides OpenTelemetry tracing for CloudWeGo Eino applications.
//
// Eino is a Go LLM application framework from CloudWeGo. This package
// implements the eino callbacks.Handler interface to capture graph runs, LLM
// and embedding invocations, and tool executions as Braintrust-compatible
// OpenTelemetry spans.
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
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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

	// Provider overrides the provider inferred from callbacks.RunInfo.Type. Set
	// this for custom model implementations and gateways so pricing is attributed
	// to the provider that served the request.
	Provider string

	// Model is used when an Eino callback does not expose a model identifier.
	Model string
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

// OnStart is called before a component begins processing. It creates spans for
// graph tasks, chat models, embeddings, and tools, capturing their inputs.
func (h *Handler) OnStart(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	// Graph inputs and outputs often have the same concrete types as model inputs
	// and outputs. Check the component first so an agent run becomes a parent task
	// rather than an extra LLM span.
	if isTaskComponent(info) {
		return h.onStartTask(ctx, info, input)
	}
	if matchesComponent(info, components.ComponentOfChatModel) {
		if modelInput := model.ConvCallbackInput(input); modelInput != nil {
			return h.onStartModel(ctx, info, modelInput)
		}
	}
	if matchesComponent(info, components.ComponentOfTool) {
		if toolInput := tool.ConvCallbackInput(input); toolInput != nil {
			return h.onStartTool(ctx, info, toolInput)
		}
	}
	if matchesComponent(info, components.ComponentOfEmbedding) {
		if embInput := embedding.ConvCallbackInput(input); embInput != nil {
			return h.onStartEmbedding(ctx, info, embInput)
		}
	}
	return ctx
}

func (h *Handler) onStartModel(ctx context.Context, info *callbacks.RunInfo, modelInput *model.CallbackInput) context.Context {
	startTime := time.Now()
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))

	if err := traceinternal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"}); err != nil {
		span.RecordError(err)
	}

	msgs := convertMessages(modelInput.Messages)
	if err := traceinternal.SetJSONAttr(span, "braintrust.input_json", msgs); err != nil {
		span.RecordError(err)
	}

	metadata := h.buildMetadata(info, modelInput)
	if err := traceinternal.SetJSONAttr(span, "braintrust.metadata", metadata); err != nil {
		span.RecordError(err)
	}

	// OTel semantic conventions for GenAI
	if modelInput.Config != nil && modelInput.Config.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", modelInput.Config.Model))
	}

	childCtx = context.WithValue(childCtx, startTimeKey{}, startTime)
	return context.WithValue(childCtx, spanKey{}, span)
}

func (h *Handler) onStartTool(ctx context.Context, info *callbacks.RunInfo, toolInput *tool.CallbackInput) context.Context {
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName)

	spanAttrs := map[string]string{
		"type": "tool",
		"name": operationNameFromInfo(info),
	}
	if err := traceinternal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		span.RecordError(err)
	}

	// ArgumentsInJSON is normally serialized JSON. Preserve malformed or plain
	// string values as JSON strings so braintrust.input_json always remains valid.
	if json.Valid([]byte(toolInput.ArgumentsInJSON)) {
		span.SetAttributes(attribute.String("braintrust.input_json", toolInput.ArgumentsInJSON))
	} else if err := traceinternal.SetJSONAttr(span, "braintrust.input_json", toolInput.ArgumentsInJSON); err != nil {
		span.RecordError(err)
	}

	return context.WithValue(childCtx, spanKey{}, span)
}

func (h *Handler) onStartTask(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
	startTime := time.Now()
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName)

	spanAttrs := map[string]string{
		"type": "task",
		"name": operationNameFromInfo(info),
	}
	if err := traceinternal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		span.RecordError(err)
	}

	if modelInput := model.ConvCallbackInput(input); modelInput != nil {
		if err := traceinternal.SetJSONAttr(span, "braintrust.input_json", convertMessages(modelInput.Messages)); err != nil {
			span.RecordError(err)
		}
	} else if input != nil {
		if err := traceinternal.SetJSONAttr(span, "braintrust.input_json", input); err != nil {
			span.RecordError(err)
		}
	}

	childCtx = context.WithValue(childCtx, startTimeKey{}, startTime)
	return context.WithValue(childCtx, spanKey{}, span)
}

func (h *Handler) onStartEmbedding(ctx context.Context, info *callbacks.RunInfo, embInput *embedding.CallbackInput) context.Context {
	spanName := spanNameFromInfo(info)
	childCtx, span := h.tracer().Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindClient))

	// Instrumentation errors from SetJSONAttr are silently dropped — the
	// underlying embedding operation is unaffected, and marking the span
	// as errored would misreport the caller's operation.
	_ = traceinternal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{"type": "llm"})

	_ = traceinternal.SetJSONAttr(span, "braintrust.input_json", embeddingInput(embInput.Texts))

	metadata := h.buildEmbeddingMetadata(info, embInput.Config)
	_ = traceinternal.SetJSONAttr(span, "braintrust.metadata", metadata)

	if embInput.Config != nil && embInput.Config.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", embInput.Config.Model))
	}

	return context.WithValue(childCtx, spanKey{}, span)
}

// OnEnd is called after a component returns successfully. It captures output
// and metrics for supported components, then ends the span.
func (h *Handler) OnEnd(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
	span, ok := ctx.Value(spanKey{}).(trace.Span)
	if !ok {
		return ctx
	}
	defer span.End()
	isTask := isTaskComponent(info)

	if modelOutput := model.ConvCallbackOutput(output); modelOutput != nil {
		if modelOutput.Message != nil {
			out := convertMessageToOutput(modelOutput.Message)
			if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", out); err != nil {
				span.RecordError(err)
			}
			// OTel GenAI conventions apply to the leaf model call, not its task parent.
			if !isTask && modelOutput.Message.ResponseMeta != nil && modelOutput.Message.ResponseMeta.FinishReason != "" {
				span.SetAttributes(attribute.String("gen_ai.finish_reason", modelOutput.Message.ResponseMeta.FinishReason))
			}
		}
		if !isTask && modelOutput.TokenUsage != nil {
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
		if toolOutput.ToolOutput != nil {
			converted, err := convertToolResult(toolOutput.ToolOutput)
			if err != nil {
				span.RecordError(err)
			} else if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", converted); err != nil {
				span.RecordError(err)
			}
		} else if err := setToolResponse(span, toolOutput.Response); err != nil {
			span.RecordError(err)
		}
		return ctx
	}

	if embOutput := embedding.ConvCallbackOutput(output); embOutput != nil {
		// Instrumentation errors are silently dropped — the embedding call
		// already succeeded; marking the span errored would misreport it.
		_ = traceinternal.SetJSONAttr(span, "braintrust.output_json", embeddingOutputSummary(embOutput.Embeddings))
		if embOutput.TokenUsage != nil {
			metrics := embeddingTokenUsageToMetrics(embOutput.TokenUsage)
			if len(metrics) > 0 {
				_ = traceinternal.SetJSONAttr(span, "braintrust.metrics", metrics)
			}
		}
		return ctx
	}

	if isTask && output != nil {
		if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			span.RecordError(err)
		}
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

	// Consume the stream, retaining every task input and the last recognized
	// leaf-component input.
	isTask := isTaskComponent(info)
	var taskInputs []callbacks.CallbackInput
	var lastModelInput *model.CallbackInput
	var lastToolInput *tool.CallbackInput
	var lastEmbeddingInput *embedding.CallbackInput
	for {
		item, err := input.Recv()
		if err != nil {
			break
		}
		if isTask {
			taskInputs = append(taskInputs, item)
		}
		if mi := model.ConvCallbackInput(item); mi != nil {
			lastModelInput = mi
		}
		if ti := tool.ConvCallbackInput(item); ti != nil {
			lastToolInput = ti
		}
		if ei := embedding.ConvCallbackInput(item); ei != nil {
			lastEmbeddingInput = ei
		}
	}

	// Graph inputs may be model-shaped, so component identity takes priority.
	if len(taskInputs) > 0 {
		return h.onStartTask(ctx, info, collapseStreamValues(taskInputs))
	}
	if matchesComponent(info, components.ComponentOfChatModel) && lastModelInput != nil {
		return h.onStartModel(ctx, info, lastModelInput)
	}
	if matchesComponent(info, components.ComponentOfTool) && lastToolInput != nil {
		return h.onStartTool(ctx, info, lastToolInput)
	}
	if matchesComponent(info, components.ComponentOfEmbedding) && lastEmbeddingInput != nil {
		return h.onStartEmbedding(ctx, info, lastEmbeddingInput)
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
	isTask := isTaskComponent(info)

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer output.Close()
		defer span.End()

		var chunks []*schema.Message
		var taskOutputs []callbacks.CallbackOutput
		var toolResponses strings.Builder
		var toolResultChunks []*schema.ToolResult
		var sawToolOutput bool
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
			} else if isTask {
				taskOutputs = append(taskOutputs, item)
			}
			if !isTask {
				if to := tool.ConvCallbackOutput(item); to != nil {
					sawToolOutput = true
					toolResponses.WriteString(to.Response)
					if to.ToolOutput != nil {
						toolResultChunks = append(toolResultChunks, to.ToolOutput)
					}
				}
			}
		}

		if len(chunks) == 0 {
			if len(taskOutputs) > 0 {
				if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", collapseStreamValues(taskOutputs)); err != nil {
					span.RecordError(err)
				}
			} else if len(toolResultChunks) > 0 {
				result, err := schema.ConcatToolResults(toolResultChunks)
				if err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, err.Error())
					return
				}
				converted, err := convertToolResult(result)
				if err != nil {
					span.RecordError(err)
				} else if err := traceinternal.SetJSONAttr(span, "braintrust.output_json", converted); err != nil {
					span.RecordError(err)
				}
			} else if sawToolOutput {
				if err := setToolResponse(span, toolResponses.String()); err != nil {
					span.RecordError(err)
				}
			}
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
		if !isTask && finalMsg.ResponseMeta != nil && finalMsg.ResponseMeta.Usage != nil {
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

func collapseStreamValues[T any](values []T) any {
	if len(values) == 1 {
		return values[0]
	}
	return values
}

func matchesComponent(info *callbacks.RunInfo, component components.Component) bool {
	return info == nil || info.Component == "" || info.Component == component
}

func isTaskComponent(info *callbacks.RunInfo) bool {
	if info == nil {
		return false
	}
	switch info.Component {
	case compose.ComponentOfGraph, compose.ComponentOfChain, compose.ComponentOfWorkflow:
		return true
	default:
		return false
	}
}

func operationNameFromInfo(info *callbacks.RunInfo) string {
	if info == nil {
		return "eino"
	}
	if info.Name != "" {
		return info.Name
	}
	if info.Type != "" {
		return info.Type
	}
	if string(info.Component) != "" {
		return string(info.Component)
	}
	return "eino"
}

// spanNameFromInfo derives a span name from RunInfo.
func spanNameFromInfo(info *callbacks.RunInfo) string {
	name := operationNameFromInfo(info)
	if name == "eino" {
		return name
	}
	return "eino." + name
}

// buildMetadata constructs the explicitly allowlisted braintrust.metadata map.
func (h *Handler) buildMetadata(info *callbacks.RunInfo, input *model.CallbackInput) map[string]any {
	metadata := map[string]any{
		"provider": h.providerFromInfo(info),
	}

	if input == nil {
		return metadata
	}

	cfg := input.Config
	if cfg != nil {
		if cfg.Model != "" {
			metadata["model"] = cfg.Model
		}
		if cfg.MaxTokens != 0 {
			metadata["max_tokens"] = cfg.MaxTokens
		}
		// Eino uses value fields, so an omitted value cannot be distinguished
		// from an explicit zero.
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

	if _, ok := metadata["model"]; !ok && h.opts.Model != "" {
		metadata["model"] = h.opts.Model
	}

	if tools := convertToolDefinitions(input.Tools); len(tools) > 0 {
		metadata["tools"] = tools
	}
	if choice, ok := normalizeToolChoice(input.ToolChoice); ok {
		metadata["tool_choice"] = choice
	}

	return metadata
}

func (h *Handler) providerFromInfo(info *callbacks.RunInfo) string {
	if h.opts.Provider != "" {
		return strings.ToLower(h.opts.Provider)
	}
	if info == nil || info.Type == "" {
		return "unknown"
	}

	provider := strings.ToLower(info.Type)
	switch provider {
	case "claude":
		return "anthropic"
	case "gemini":
		return "google"
	default:
		return provider
	}
}

func convertToolDefinitions(tools []*schema.ToolInfo) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, toolInfo := range tools {
		if toolInfo == nil || toolInfo.Name == "" {
			continue
		}

		function := map[string]any{"name": toolInfo.Name}
		if toolInfo.Desc != "" {
			function["description"] = toolInfo.Desc
		}
		if toolInfo.ParamsOneOf != nil {
			params, err := toolInfo.ToJSONSchema()
			if err == nil && params != nil {
				function["parameters"] = params
			}
		}
		result = append(result, map[string]any{
			"type":     "function",
			"function": function,
		})
	}
	return result
}

func normalizeToolChoice(choice *schema.ToolChoice) (string, bool) {
	if choice == nil {
		return "", false
	}
	switch *choice {
	case schema.ToolChoiceForbidden:
		return "none", true
	case schema.ToolChoiceAllowed:
		return "auto", true
	case schema.ToolChoiceForced:
		return "required", true
	default:
		return "", false
	}
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

// convertMessage converts a single schema.Message to an OpenAI chat message.
func convertMessage(msg *schema.Message) map[string]any {
	m := map[string]any{
		"role":    string(msg.Role),
		"content": messageContent(msg),
	}
	if len(msg.ToolCalls) > 0 {
		m["tool_calls"] = convertToolCalls(msg.ToolCalls)
		if msg.Content == "" && len(msg.UserInputMultiContent) == 0 && len(msg.AssistantGenMultiContent) == 0 && len(msg.MultiContent) == 0 {
			m["content"] = nil
		}
	}
	if msg.ToolCallID != "" {
		m["tool_call_id"] = msg.ToolCallID
	}
	return m
}

// convertMessageToOutput converts a response to canonical OpenAI choices.
func convertMessageToOutput(msg *schema.Message) []map[string]any {
	message := map[string]any{
		"role":    string(msg.Role),
		"content": messageContent(msg),
	}
	if len(msg.ToolCalls) > 0 {
		message["tool_calls"] = convertToolCalls(msg.ToolCalls)
		if msg.Content == "" && len(msg.AssistantGenMultiContent) == 0 && len(msg.MultiContent) == 0 {
			message["content"] = nil
		}
	}

	finishReason := ""
	if msg.ResponseMeta != nil {
		finishReason = msg.ResponseMeta.FinishReason
	}
	finishReason = normalizeFinishReason(finishReason, len(msg.ToolCalls) > 0)

	return []map[string]any{{
		"index":         0,
		"finish_reason": finishReason,
		"message":       message,
	}}
}

func normalizeFinishReason(reason string, hasToolCalls bool) string {
	switch strings.ToLower(reason) {
	case "tool_calls", "tool_use", "function_call":
		return "tool_calls"
	case "length", "max_tokens", "max_output_tokens":
		return "length"
	case "content_filter", "safety":
		return "content_filter"
	case "stop", "end_turn", "stop_sequence", "":
		if hasToolCalls {
			return "tool_calls"
		}
		return "stop"
	default:
		return strings.ToLower(reason)
	}
}

func messageContent(msg *schema.Message) any {
	if len(msg.UserInputMultiContent) > 0 {
		return convertInputParts(msg.UserInputMultiContent)
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		return convertOutputParts(msg.AssistantGenMultiContent)
	}
	if len(msg.MultiContent) > 0 {
		return convertLegacyParts(msg.MultiContent)
	}
	return msg.Content
}

func convertInputParts(parts []schema.MessageInputPart) []map[string]any {
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			result = append(result, map[string]any{"type": "text", "text": part.Text})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image != nil {
				imageURL := map[string]any{"url": mediaValue(part.Image.MessagePartCommon)}
				if part.Image.Detail != "" {
					imageURL["detail"] = part.Image.Detail
				}
				result = append(result, map[string]any{"type": "image_url", "image_url": imageURL})
			}
		case schema.ChatMessagePartTypeAudioURL:
			if part.Audio != nil {
				result = append(result, fileContentPart("", part.Audio.MessagePartCommon))
			}
		case schema.ChatMessagePartTypeVideoURL:
			if part.Video != nil {
				result = append(result, fileContentPart("", part.Video.MessagePartCommon))
			}
		case schema.ChatMessagePartTypeFileURL:
			if part.File != nil {
				result = append(result, fileContentPart(part.File.Name, part.File.MessagePartCommon))
			}
		}
	}
	return result
}

func convertOutputParts(parts []schema.MessageOutputPart) []map[string]any {
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			result = append(result, map[string]any{"type": "text", "text": part.Text})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image != nil {
				result = append(result, map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": mediaValue(part.Image.MessagePartCommon)},
				})
			}
		case schema.ChatMessagePartTypeAudioURL:
			if part.Audio != nil {
				result = append(result, fileContentPart("", part.Audio.MessagePartCommon))
			}
		case schema.ChatMessagePartTypeVideoURL:
			if part.Video != nil {
				result = append(result, fileContentPart("", part.Video.MessagePartCommon))
			}
		case schema.ChatMessagePartTypeReasoning:
			if part.Reasoning != nil {
				reasoning := map[string]any{
					"type": "reasoning",
					"text": part.Reasoning.Text,
				}
				if part.Reasoning.Signature != "" {
					reasoning["signature"] = part.Reasoning.Signature
				}
				result = append(result, reasoning)
			}
		}
	}
	return result
}

// Eino still accepts MultiContent, so preserve legacy inputs in normalized form.
func convertLegacyParts(parts []schema.ChatMessagePart) []map[string]any { //nolint:staticcheck
	result := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			result = append(result, map[string]any{"type": "text", "text": part.Text})
		case schema.ChatMessagePartTypeImageURL:
			if part.ImageURL != nil {
				url := part.ImageURL.URL
				if url == "" {
					url = part.ImageURL.URI
				}
				imageURL := map[string]any{"url": url}
				if part.ImageURL.Detail != "" {
					imageURL["detail"] = part.ImageURL.Detail
				}
				result = append(result, map[string]any{"type": "image_url", "image_url": imageURL})
			}
		case schema.ChatMessagePartTypeAudioURL:
			if part.AudioURL != nil {
				result = append(result, legacyFileContentPart("", part.AudioURL.URL, part.AudioURL.URI))
			}
		case schema.ChatMessagePartTypeVideoURL:
			if part.VideoURL != nil {
				result = append(result, legacyFileContentPart("", part.VideoURL.URL, part.VideoURL.URI))
			}
		case schema.ChatMessagePartTypeFileURL:
			if part.FileURL != nil {
				result = append(result, legacyFileContentPart(part.FileURL.Name, part.FileURL.URL, part.FileURL.URI))
			}
		}
	}
	return result
}

func mediaValue(media schema.MessagePartCommon) string {
	if media.URL != nil {
		return *media.URL
	}
	if media.Base64Data == nil {
		return ""
	}
	if media.MIMEType == "" {
		return *media.Base64Data
	}
	return "data:" + media.MIMEType + ";base64," + *media.Base64Data
}

func fileContentPart(filename string, media schema.MessagePartCommon) map[string]any {
	file := map[string]any{"file_data": mediaValue(media)}
	if filename != "" {
		file["filename"] = filename
	}
	return map[string]any{"type": "file", "file": file}
}

func legacyFileContentPart(filename, url, uri string) map[string]any {
	if url == "" {
		url = uri
	}
	file := map[string]any{"file_data": url}
	if filename != "" {
		file["filename"] = filename
	}
	return map[string]any{"type": "file", "file": file}
}

func setToolResponse(span trace.Span, response string) error {
	if json.Valid([]byte(response)) {
		// Avoid double-encoding JSON tool responses.
		span.SetAttributes(attribute.String("braintrust.output_json", response))
		return nil
	}
	return traceinternal.SetJSONAttr(span, "braintrust.output_json", response)
}

func convertToolResult(result *schema.ToolResult) (map[string]any, error) {
	parts, err := result.ToMessageInputParts()
	if err != nil {
		return nil, err
	}
	return map[string]any{"parts": convertInputParts(parts)}, nil
}

// convertToolCalls converts a slice of schema.ToolCall to OpenAI-compatible format.
func convertToolCalls(tcs []schema.ToolCall) []map[string]any {
	result := make([]map[string]any, len(tcs))
	for i, tc := range tcs {
		result[i] = map[string]any{
			"id":   tc.ID,
			"type": "function",
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
	addNonNegativeMetric(metrics, "prompt_tokens", u.PromptTokens)
	addNonNegativeMetric(metrics, "completion_tokens", u.CompletionTokens)
	addNonNegativeMetric(metrics, "tokens", u.TotalTokens)
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
	addNonNegativeMetric(metrics, "prompt_tokens", u.PromptTokens)
	addNonNegativeMetric(metrics, "completion_tokens", u.CompletionTokens)
	addNonNegativeMetric(metrics, "tokens", u.TotalTokens)
	if u.PromptTokenDetails.CachedTokens > 0 {
		metrics["prompt_cached_tokens"] = int64(u.PromptTokenDetails.CachedTokens)
	}
	if u.CompletionTokensDetails.ReasoningTokens > 0 {
		metrics["completion_reasoning_tokens"] = int64(u.CompletionTokensDetails.ReasoningTokens)
	}
	return metrics
}

// buildEmbeddingMetadata constructs the braintrust.metadata map for embedding spans.
func (h *Handler) buildEmbeddingMetadata(info *callbacks.RunInfo, cfg *embedding.Config) map[string]any {
	metadata := map[string]any{
		"provider": h.providerFromInfo(info),
	}
	if cfg != nil && cfg.Model != "" {
		metadata["model"] = cfg.Model
	} else if h.opts.Model != "" {
		metadata["model"] = h.opts.Model
	}
	return metadata
}

func embeddingInput(texts []string) map[string]any {
	inputs := make([]map[string]any, len(texts))
	for i, text := range texts {
		inputs[i] = map[string]any{"content": text}
	}
	return map[string]any{"inputs": inputs}
}

func embeddingOutputSummary(embeddings [][]float64) map[string]any {
	return map[string]any{"count": len(embeddings)}
}

// embeddingTokenUsageToMetrics converts embedding.TokenUsage into the standard
// Braintrust metrics map. Embeddings have no completion tokens.
func embeddingTokenUsageToMetrics(u *embedding.TokenUsage) map[string]int64 {
	metrics := make(map[string]int64)
	addNonNegativeMetric(metrics, "prompt_tokens", u.PromptTokens)
	addNonNegativeMetric(metrics, "tokens", u.TotalTokens)
	return metrics
}

func addNonNegativeMetric(metrics map[string]int64, name string, value int) {
	if value >= 0 {
		metrics[name] = int64(value)
	}
}

// Compile-time assertion that Handler implements the required interfaces.
var _ callbacks.Handler = (*Handler)(nil)
var _ callbacks.TimingChecker = (*Handler)(nil)
