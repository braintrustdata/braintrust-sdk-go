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
//	btCallbacks := traceadk.NewCallbacks()
//
//	agent, err := llmagent.New(btCallbacks.LLMAgentConfig(llmagent.Config{
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
	"strings"
	"sync"

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

// cleanupJSON recursively removes keys with empty values from JSON structures.
// Empty values include: nil, empty strings, empty slices, and empty maps.
// We do this because the ADK types do not always have omitempty annotations.
func cleanupJSON(log logger.Logger, value interface{}) interface{} {
	jsonStr, err := json.Marshal(value)
	if err != nil {
		log.Debug("Failed to marshal value to JSON", "error", err)
		return value
	}
	var generic interface{}
	if err := json.Unmarshal(jsonStr, &generic); err != nil {
		log.Debug("Failed to unmarshal value to generic structure", "error", err)
		return value
	}

	switch val := generic.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v := range val {
			cleaned := cleanupJSON(log, v)
			// Only include non-empty values
			if !isEmpty(cleaned) {
				result[k] = cleaned
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case []interface{}:
		result := make([]interface{}, 0, len(val))
		for _, item := range val {
			// include all items
			result = append(result, cleanupJSON(log, item))
		}
		return result
	default:
		return val
	}
}

// isEmpty checks if a value is considered empty (nil, empty string, empty slice, empty map).
func isEmpty(v interface{}) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case string:
		return val == ""
	case []interface{}:
		return len(val) == 0
	case map[string]interface{}:
		return len(val) == 0
	default:
		return false
	}
}

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
		if log == nil {
			c.logger = logger.Discard()
		} else {
			c.logger = log
		}
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
// It offers helper methods to wrap agent configurations with tracing.
type Callbacks interface {
	// Helpers to wrap different kinds of configs
	AgentConfig(agent.Config) agent.Config
	LLMAgentConfig(llmagent.Config) llmagent.Config
}

// Callbacks provides OpenTelemetry tracing callbacks for ADK agents.
type callbacksImpl struct {
	cfg    *config
	tracer trace.Tracer

	// spans stores active spans keyed by session ID and span type
	// Note: We use SessionID instead of InvocationID because ADK uses different
	// invocation IDs for agent vs model callbacks
	spans     map[string]map[string]trace.Span
	spansLock sync.Mutex
}

// NewCallbacks creates a new set of tracing callbacks for ADK agents.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	callbacks := adk.NewCallbacks()
func NewCallbacks(opts ...Option) Callbacks {
	cfg := &config{
		logger: logger.Discard(),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return &callbacksImpl{
		cfg:    cfg,
		tracer: cfg.tracer(),
		spans:  make(map[string]map[string]trace.Span),
	}
}

func setSpanAttributes(span trace.Span, ctx agent.CallbackContext) {
	span.SetAttributes(attribute.String("adk.agent.name", ctx.AgentName()))
	span.SetAttributes(attribute.String("adk.agent.invocation_id", ctx.InvocationID()))
	span.SetAttributes(attribute.String("adk.agent.session_id", ctx.SessionID()))
	span.SetAttributes(attribute.String("adk.agent.branch", ctx.Branch()))
}

const rootSpanKey = "root"

func getAgentSpanKey(invocationID string) string {
	return fmt.Sprintf("agent:%s", invocationID)
}

func getModelSpanKey(ctx agent.CallbackContext) string {
	return fmt.Sprintf("model:%s", ctx.InvocationID())
}

func getToolSpanKey(ctx tool.Context) string {
	return fmt.Sprintf("tool:%s", ctx.FunctionCallID())
}

func (c *callbacksImpl) storeSpan(sessionID string, spanKey string, span trace.Span) {
	c.spansLock.Lock()
	defer c.spansLock.Unlock()

	sessionSpans, ok := c.spans[sessionID]
	if !ok {
		sessionSpans = make(map[string]trace.Span)
		c.spans[sessionID] = sessionSpans
		if strings.HasPrefix(spanKey, "agent:") {
			// Mark the first agent span as the root, as a fallback
			sessionSpans[rootSpanKey] = span
		}
	}
	sessionSpans[spanKey] = span
}

// retrieveContext retrieves (but does not delete) a stored context
func (c *callbacksImpl) retrieveSpan(sessionID string, spanKey string) (trace.Span, bool) {
	c.spansLock.Lock()
	defer c.spansLock.Unlock()

	spans, ok := c.spans[sessionID]
	if ok {
		span, ok := spans[spanKey]
		return span, ok
	}
	return nil, false
}

func (c *callbacksImpl) retrieveRootSpan(sessionID string) (trace.Span, bool) {
	return c.retrieveSpan(sessionID, rootSpanKey)
}

// deleteContext removes a stored context
func (c *callbacksImpl) deleteSpan(sessionID string, spanKey string) {
	c.spansLock.Lock()
	defer c.spansLock.Unlock()

	spans, ok := c.spans[sessionID]
	if ok {
		delete(spans, spanKey)

		// To make sure we eventually clean up sessions and that the map doesn't
		// grow indefinitely, clean up the session if there are no more agent
		// spans
		for spanKey := range spans {
			if strings.HasPrefix(spanKey, "agent:") {
				return
			}
		}
		delete(c.spans, sessionID)
	}
}

// BeforeAgent is called before the agent starts its run.
// It creates a span that covers the agent run, linking to parent agent if one exists.
func (c *callbacksImpl) BeforeAgent(ctx agent.CallbackContext) (*genai.Content, error) {
	c.cfg.logger.Debug("BeforeAgent callback", "sessionID", ctx.SessionID(), "invocationID", ctx.InvocationID(), "agent", ctx.AgentName(), "branch", ctx.Branch())

	// Fall back to the root span as a parent, if there is one.
	// TODO: Unfortunately, ADK doesn't provide a great way to trace
	// parent/child agent relationships. We could parse branch, but that only
	// works if agent names are unique.
	var spanCtx context.Context = ctx
	parentSpan, hasParent := c.retrieveRootSpan(ctx.SessionID())
	if hasParent {
		spanCtx = trace.ContextWithSpan(ctx, parentSpan)
	}

	// Create agent span (root or child based on parent)
	_, span := c.tracer.Start(spanCtx, fmt.Sprintf("agent_run [%s]", ctx.AgentName()),
		trace.WithSpanKind(trace.SpanKindInternal))
	setSpanAttributes(span, ctx)

	c.storeSpan(ctx.SessionID(), getAgentSpanKey(ctx.InvocationID()), span)

	return nil, nil
}

// AfterAgent is called after the agent completes its run.
// It completes the span created by BeforeAgent and cleans up all contexts.
func (c *callbacksImpl) AfterAgent(ctx agent.CallbackContext) (*genai.Content, error) {
	c.cfg.logger.Debug("AfterAgent callback", "sessionID", ctx.SessionID(), "invocationID", ctx.InvocationID(), "agentName", ctx.AgentName())

	// Retrieve and end the agent span using agent name
	span, ok := c.retrieveSpan(ctx.SessionID(), getAgentSpanKey(ctx.InvocationID()))
	if ok {
		defer span.End()
		// Delete this agent's span
		c.deleteSpan(ctx.SessionID(), getAgentSpanKey(ctx.InvocationID()))
	}

	// Don't modify the response
	return nil, nil
}

// BeforeModel is called before sending a request to the LLM model.
// It creates a span to trace the model invocation as a child of the agent span.
func (c *callbacksImpl) BeforeModel(ctx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
	c.cfg.logger.Debug("BeforeModel callback", "sessionID", ctx.SessionID(), "invocationID", ctx.InvocationID(), "agentName", ctx.AgentName(), "request", req)

	// Create a span for the model call, using the agent span context as parent
	var spanCtx context.Context = ctx
	parentSpan, hasParent := c.retrieveSpan(ctx.SessionID(), getAgentSpanKey(ctx.InvocationID()))
	if !hasParent {
		// Fallback: look up root span
		parentSpan, hasParent = c.retrieveRootSpan(ctx.SessionID())
	}
	if hasParent {
		spanCtx = trace.ContextWithSpan(ctx, parentSpan)
	}

	_, span := c.tracer.Start(spanCtx, "call_llm",
		trace.WithSpanKind(trace.SpanKindClient))
	setSpanAttributes(span, ctx)

	spanAttrs := map[string]string{
		"type": "llm",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		c.cfg.logger.Debug("Failed to set braintrust.span_attributes", "error", err)
	}

	if req.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", req.Model))
	}
	if len(req.Contents) > 0 {
		err := internal.SetJSONAttr(span, "gen_ai.prompt", req.Contents)
		if err != nil {
			c.cfg.logger.Debug("Failed to set gen_ai.prompt", "error", err)
		}
		err = internal.SetJSONAttr(span, "braintrust.input_json", cleanupJSON(c.cfg.logger, req))
		if err != nil {
			c.cfg.logger.Debug("Failed to set braintrust.input_json", "error", err)
		}
	}

	c.storeSpan(ctx.SessionID(), getModelSpanKey(ctx), span)

	return nil, nil
}

// AfterModel is called after receiving a response from the LLM model.
// It completes the span created by BeforeModel and records the response.
func (c *callbacksImpl) AfterModel(ctx agent.CallbackContext, resp *model.LLMResponse, err error) (*model.LLMResponse, error) {
	c.cfg.logger.Debug("AfterModel callback", "sessionID", ctx.SessionID(), "invocationID", ctx.InvocationID(), "agentName", ctx.AgentName(), "response", resp, "error", err)

	// Retrieve the span (but don't remove it yet)
	span, ok := c.retrieveSpan(ctx.SessionID(), getModelSpanKey(ctx))
	if !ok {
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
		err = internal.SetJSONAttr(span, "braintrust.output_json", cleanupJSON(c.cfg.logger, resp))
		if err != nil {
			c.cfg.logger.Debug("Failed to set braintrust.output_json", "error", err)
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

	return nil, nil
}

// BeforeTool is called before executing a tool.
// It creates a span to trace the tool execution as a child of the LLM span.
func (c *callbacksImpl) BeforeTool(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error) {
	c.cfg.logger.Debug("BeforeTool callback", "sessionID", ctx.SessionID(), "invocationID", ctx.InvocationID(), "agentName", ctx.AgentName(), "tool", t.Name(), "args", args)

	// Try to get the LLM span context to establish parent-child relationship
	var spanCtx context.Context = ctx
	parentSpan, hasParent := c.retrieveSpan(ctx.SessionID(), getModelSpanKey(ctx))
	if !hasParent {
		// Fallback: look up root span
		parentSpan, hasParent = c.retrieveRootSpan(ctx.SessionID())
	}
	if hasParent {
		spanCtx = trace.ContextWithSpan(ctx, parentSpan)
	}

	_, span := c.tracer.Start(spanCtx, fmt.Sprintf("tool [%s]", t.Name()),
		trace.WithSpanKind(trace.SpanKindInternal))
	setSpanAttributes(span, ctx)

	spanAttrs := map[string]string{
		"type": "tool",
	}
	if err := internal.SetJSONAttr(span, "braintrust.span_attributes", spanAttrs); err != nil {
		c.cfg.logger.Debug("Failed to set braintrust.span_attributes", "error", err)
	}

	span.SetAttributes(attribute.String("tool.name", t.Name()))
	if desc := t.Description(); desc != "" {
		span.SetAttributes(attribute.String("tool.description", desc))
	}
	if len(args) > 0 {
		err := internal.SetJSONAttr(span, "tool.input", args)
		if err != nil {
			c.cfg.logger.Debug("Failed to set tool.input", "error", err)
		}
		err = internal.SetJSONAttr(span, "braintrust.input_json", args)
		if err != nil {
			c.cfg.logger.Debug("Failed to set braintrust.input_json", "error", err)
		}
	}

	c.storeSpan(ctx.SessionID(), getToolSpanKey(ctx), span)

	return nil, nil
}

// AfterTool is called after a tool execution completes.
// It completes the span created by BeforeTool and records the result.
func (c *callbacksImpl) AfterTool(ctx tool.Context, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
	c.cfg.logger.Debug("AfterTool callback", "sessionID", ctx.SessionID(), "invocationID", ctx.InvocationID(), "agentName", ctx.AgentName(), "tool", t.Name(), "result", result, "error", err)

	// Retrieve the span using composite key
	span, ok := c.retrieveSpan(ctx.SessionID(), getToolSpanKey(ctx))
	if !ok {
		// No span found, maybe BeforeTool wasn't called
		return nil, nil
	}
	defer span.End()
	c.deleteSpan(ctx.SessionID(), getToolSpanKey(ctx))

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil
	}

	if result != nil {
		err := internal.SetJSONAttr(span, "tool.output", result)
		if err != nil {
			c.cfg.logger.Debug("Failed to set tool.output", "error", err)
		}
		err = internal.SetJSONAttr(span, "braintrust.output_json", result)
		if err != nil {
			c.cfg.logger.Debug("Failed to set braintrust.output_json", "error", err)
		}
	}

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
func (c *callbacksImpl) LLMAgentConfig(config llmagent.Config) llmagent.Config {
	config.BeforeAgentCallbacks = append(config.BeforeAgentCallbacks, c.BeforeAgent)
	config.AfterAgentCallbacks = append(config.AfterAgentCallbacks, c.AfterAgent)
	config.BeforeModelCallbacks = append(config.BeforeModelCallbacks, c.BeforeModel)
	config.AfterModelCallbacks = append(config.AfterModelCallbacks, c.AfterModel)
	config.BeforeToolCallbacks = append(config.BeforeToolCallbacks, c.BeforeTool)
	config.AfterToolCallbacks = append(config.AfterToolCallbacks, c.AfterTool)
	return config
}

func (c *callbacksImpl) AgentConfig(config agent.Config) agent.Config {
	config.BeforeAgentCallbacks = append(config.BeforeAgentCallbacks, c.BeforeAgent)
	config.AfterAgentCallbacks = append(config.AfterAgentCallbacks, c.AfterAgent)
	return config
}
