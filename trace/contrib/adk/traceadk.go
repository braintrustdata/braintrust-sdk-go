// Package adk provides OpenTelemetry tracing callbacks for Google ADK agents.
//
// First, set up tracing with braintrust.New():
//
//	tp := trace.NewTracerProvider()
//	defer tp.Shutdown(context.Background())
//	otel.SetTracerProvider(tp)
//
//	bt, err := braintrust.New(tp,
//		braintrust.WithProject("my-project"),
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//
// Then create your llmagent with tracing callbacks:
//
//	agent, err := llmagent.New(traceadk.TracedConfig(llmagent.Config{
//		Name:        "my-agent",
//		Model:       model,
//		Description: "My agent",
//		Tools:       tools,
//	}))
package adk

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// config holds configuration for the ADK tracing callbacks
type config struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// Option configures the ADK tracing callbacks
type Option func(*config)

// WithTracerProvider sets a custom TracerProvider for the callbacks.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) {
		c.tracerProvider = tp
	}
}

// WithLogger sets a custom logger for the callbacks.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) Option {
	return func(c *config) {
		c.logger = log
	}
}

// tracer returns the configured tracer
func (c *config) tracer() trace.Tracer {
	tp := c.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// Callbacks provides OpenTelemetry tracing callbacks for ADK agents.
type Callbacks struct {
	cfg    *config
	tracer trace.Tracer

	// spans stores active spans keyed by session ID and span type
	// Note: We use SessionID instead of InvocationID because ADK uses different
	// invocation IDs for agent vs model callbacks
	spans map[string]map[string]trace.Span
}

// NewCallbacks creates a new set of tracing callbacks for ADK agents.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	callbacks := adk.NewCallbacks()
func NewCallbacks(opts ...Option) *Callbacks {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	return &Callbacks{
		cfg:    cfg,
		tracer: cfg.tracer(),
		spans:  make(map[string]map[string]trace.Span),
	}
}

func (c *Callbacks) storeSpan(sessionID string, spanType string, span trace.Span) {
	sessionSpans, ok := c.spans[sessionID]
	if !ok {
		sessionSpans = make(map[string]trace.Span)
		c.spans[sessionID] = sessionSpans
	}
	sessionSpans[spanType] = span
}

// retrieveContext retrieves (but does not delete) a stored context
func (c *Callbacks) retrieveSpan(sessionID string, spanType string) (trace.Span, bool) {
	spans, ok := c.spans[sessionID]
	if ok {
		span, ok := spans[spanType]
		return span, ok
	}
	return nil, false
}

// deleteContext removes a stored context
func (c *Callbacks) deleteSpan(sessionID string, spanType string) {
	spans, ok := c.spans[sessionID]
	if ok {
		delete(spans, spanType)
	}
}

func (c *Callbacks) deleteAllSessionSpans(sessionID string) {
	delete(c.spans, sessionID)
}

// BeforeAgent is called before the agent starts its run.
// It creates a span that covers the agent run.
func (c *Callbacks) BeforeAgent(ctx agent.CallbackContext) (*genai.Content, error) {
	if c.cfg.logger != nil {
		c.cfg.logger.Info("BeforeAgent callback", "sessionID", ctx.SessionID())
	}

	// Create a span for the agent run
	_, span := c.tracer.Start(ctx, fmt.Sprintf("agent_run [%s]", ctx.AppName()),
		trace.WithSpanKind(trace.SpanKindInternal),
	)

	// Set span attributes
	span.SetAttributes(attribute.String("agent.name", ctx.AgentName()))
	span.SetAttributes(attribute.String("agent.invocation_id", ctx.InvocationID()))
	span.SetAttributes(attribute.String("agent.session_id", ctx.SessionID()))

	c.storeSpan(ctx.SessionID(), "agent", span)

	// Don't intercept the agent run, let it proceed normally
	return nil, nil
}

// AfterAgent is called after the agent completes its run.
// It completes the span created by BeforeAgent and cleans up all contexts.
func (c *Callbacks) AfterAgent(ctx agent.CallbackContext) (*genai.Content, error) {
	if c.cfg.logger != nil {
		c.cfg.logger.Info("AfterAgent callback", "sessionID", ctx.SessionID())
	}

	// Retrieve and end the agent span (using session ID)
	span, ok := c.retrieveSpan(ctx.SessionID(), "agent")
	if ok && span.IsRecording() {
		defer span.End()
		span.SetStatus(codes.Ok, "")
	}

	// Delete all spans from this session
	c.deleteAllSessionSpans(ctx.SessionID())

	// Don't modify the response
	return nil, nil
}

// BeforeModel is called before sending a request to the LLM model.
// It creates a span to trace the model invocation as a child of the agent span.
func (c *Callbacks) BeforeModel(ctx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
	if c.cfg.logger != nil {
		c.cfg.logger.Info("BeforeModel callback", "request", req)
	}

	// Create a span for the model call, using the agent span context as parent
	var spanCtx context.Context = ctx
	parentSpan, hasParent := c.retrieveSpan(ctx.SessionID(), "agent")
	if hasParent {
		spanCtx = trace.ContextWithSpan(ctx, parentSpan)
	}

	_, span := c.tracer.Start(spanCtx, "call_llm",
		trace.WithSpanKind(trace.SpanKindClient),
	)

	// Set span attributes
	if req.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", req.Model))
	}

	// Add input/prompt information
	if len(req.Contents) > 0 {
		// Serialize contents to JSON for the span
		contentsJSON, err := json.Marshal(req.Contents)
		if err == nil {
			span.SetAttributes(attribute.String("gen_ai.prompt", string(contentsJSON)))
		}
	}

	// Store the span and its context for later retrieval
	// The context is used by tool calls to establish parent-child relationships
	c.storeSpan(ctx.SessionID(), "model", span)

	// Don't intercept the model call, let it proceed normally
	return nil, nil
}

// AfterModel is called after receiving a response from the LLM model.
// It completes the span created by BeforeModel and records the response.
func (c *Callbacks) AfterModel(ctx agent.CallbackContext, resp *model.LLMResponse, err error) (*model.LLMResponse, error) {
	if c.cfg.logger != nil {
		c.cfg.logger.Info("AfterModel callback", "response", resp, "error", err)
	}

	// Retrieve the span (but don't remove it yet)
	span, ok := c.retrieveSpan(ctx.SessionID(), "model")
	if !ok || !span.IsRecording() {
		// No span found, maybe BeforeModel wasn't called
		return nil, nil
	}
	// Do not delete the span, tool calls may want to use it as a parent
	defer span.End()

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil
	}

	if resp != nil {
		// Record response content
		if resp.Content != nil {
			contentJSON, marshalErr := json.Marshal(resp.Content)
			if marshalErr == nil {
				span.SetAttributes(attribute.String("gen_ai.completion", string(contentJSON)))
			}
		}

		// Record token usage if available
		if resp.UsageMetadata != nil {
			if resp.UsageMetadata.PromptTokenCount > 0 {
				span.SetAttributes(attribute.Int64("gen_ai.usage.prompt_tokens", int64(resp.UsageMetadata.PromptTokenCount)))
			}
			if resp.UsageMetadata.CandidatesTokenCount > 0 {
				span.SetAttributes(attribute.Int64("gen_ai.usage.completion_tokens", int64(resp.UsageMetadata.CandidatesTokenCount)))
			}
			if resp.UsageMetadata.TotalTokenCount > 0 {
				span.SetAttributes(attribute.Int64("gen_ai.usage.total_tokens", int64(resp.UsageMetadata.TotalTokenCount)))
			}
		}

		// Record finish reason if available
		if resp.FinishReason != "" {
			span.SetAttributes(attribute.String("gen_ai.finish_reason", string(resp.FinishReason)))
		}
	}

	span.SetStatus(codes.Ok, "")

	// Don't modify the response
	return nil, nil
}

// BeforeTool is called before executing a tool.
// It creates a span to trace the tool execution as a child of the LLM span.
func (c *Callbacks) BeforeTool(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
	if c.cfg.logger != nil {
		c.cfg.logger.Info("BeforeTool callback", "tool", t.Name(), "args", args)
	}

	// Try to get the LLM span context to establish parent-child relationship
	var spanCtx context.Context = ctx
	parentSpan, hasParent := c.retrieveSpan(ctx.SessionID(), "model")
	if hasParent {
		spanCtx = trace.ContextWithSpan(ctx, parentSpan)
	}

	// Create a span for the tool call, using the LLM span context as parent
	_, span := c.tracer.Start(spanCtx, fmt.Sprintf("tool [%s]", t.Name()),
		trace.WithSpanKind(trace.SpanKindInternal),
	)

	// Set span attributes
	span.SetAttributes(attribute.String("tool.name", t.Name()))
	if desc := t.Description(); desc != "" {
		span.SetAttributes(attribute.String("tool.description", desc))
	}

	// Mark this span as a tool span for Braintrust UI
	spanAttrs := map[string]string{
		"type": "tool",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		span.RecordError(err)
	}

	// Add tool arguments
	if len(args) > 0 {
		argsJSON, err := json.Marshal(args)
		if err == nil {
			span.SetAttributes(attribute.String("tool.input", string(argsJSON)))
			// Also set as braintrust.input_json for consistency with other integrations
			if err := internal.SetJSONAttr(span, "braintrust.input_json", args); err != nil {
				span.RecordError(err)
			}
		}
	}

	// Store the span using a composite key of session ID and function call ID
	c.storeSpan(ctx.SessionID(), ctx.FunctionCallID(), span)

	// Don't modify args, let the tool execute normally
	return nil, nil
}

// AfterTool is called after a tool execution completes.
// It completes the span created by BeforeTool and records the result.
func (c *Callbacks) AfterTool(ctx tool.Context, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
	if c.cfg.logger != nil {
		c.cfg.logger.Info("AfterTool callback", "tool", t.Name(), "result", result, "error", err)
	}

	// Retrieve the span using composite key
	span, ok := c.retrieveSpan(ctx.SessionID(), ctx.FunctionCallID())
	if !ok || !span.IsRecording() {
		// No span found, maybe BeforeTool wasn't called
		return nil, nil
	}
	defer span.End()
	c.deleteSpan(ctx.SessionID(), ctx.FunctionCallID())

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil
	}

	if result != nil {
		// Record tool result
		resultJSON, marshalErr := json.Marshal(result)
		if marshalErr == nil {
			// Keep tool.output for backward compatibility
			span.SetAttributes(attribute.String("tool.output", string(resultJSON)))
			// Also set as braintrust.output_json for consistency with other integrations
			if err := internal.SetJSONAttr(span, "braintrust.output_json", result); err != nil {
				span.RecordError(err)
			}
		}
	}

	span.SetStatus(codes.Ok, "")

	// Don't try to clean up model span here - let AfterAgent handle all cleanup
	// This ensures all tools in a sequence can use the same parent context

	// Don't modify the result
	return nil, nil
}

// TracedConfig is a convenience function that wraps a llmagent.Config with Braintrust tracing callbacks.
// It automatically adds all tracing callbacks (BeforeAgent, AfterAgent, BeforeModel, AfterModel, BeforeTool, AfterTool).
//
// Example:
//
//	agent, err := llmagent.New(adk.TracedConfig(llmagent.Config{
//		Name:        "my-agent",
//		Model:       model,
//		Description: "My agent",
//		Tools:       tools,
//	}))
func TracedConfig(config llmagent.Config) llmagent.Config {
	callbacks := NewCallbacks()
	config.BeforeAgentCallbacks = append(config.BeforeAgentCallbacks, callbacks.BeforeAgent)
	config.AfterAgentCallbacks = append(config.AfterAgentCallbacks, callbacks.AfterAgent)
	config.BeforeModelCallbacks = append(config.BeforeModelCallbacks, callbacks.BeforeModel)
	config.AfterModelCallbacks = append(config.AfterModelCallbacks, callbacks.AfterModel)
	config.BeforeToolCallbacks = append(config.BeforeToolCallbacks, callbacks.BeforeTool)
	config.AfterToolCallbacks = append(config.AfterToolCallbacks, callbacks.AfterTool)
	return config
}
