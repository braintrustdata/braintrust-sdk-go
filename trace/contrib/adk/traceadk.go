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
//	cfg := llmagent.Config{
//	   Name:        "my-agent",
//	   Model:       model,
//	   Description: "My agent",
//	   Tools:       tools,
//	}
//	traceadk.AddLLMAgentCallbacks(&cfg, btCallbacks)
//	agent, err := llmagent.New(cfg)
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

var globalCallbacks = NewCallbacks()

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

// CallbacksOption configures the ADK tracing callbacks
type CallbacksOption func(*callbacksImpl)

// WithTracerProvider sets a custom TracerProvider for the callbacks.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) CallbacksOption {
	return func(callbacks *callbacksImpl) {
		callbacks.tracer = tp.Tracer("braintrust")
	}
}

// WithLogger sets a custom logger for the callbacks.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) CallbacksOption {
	return func(callbacks *callbacksImpl) {
		if log != nil {
			callbacks.logger = log
		}
	}
}

// Callbacks provides OpenTelemetry tracing callbacks for ADK agents.
type Callbacks interface {
	BeforeAgent(ctx agent.CallbackContext) (*genai.Content, error)
	AfterAgent(ctx agent.CallbackContext) (*genai.Content, error)
	BeforeModel(ctx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error)
	AfterModel(ctx agent.CallbackContext, resp *model.LLMResponse, err error) (*model.LLMResponse, error)
	BeforeTool(ctx tool.Context, t tool.Tool, args map[string]any) (map[string]any, error)
	AfterTool(ctx tool.Context, t tool.Tool, args, result map[string]any, err error) (map[string]any, error)
}

// Callbacks provides OpenTelemetry tracing callbacks for ADK agents.
type callbacksImpl struct {
	logger logger.Logger
	tracer trace.Tracer

	// spans stores active spans keyed by session ID and span type
	// Note: We use SessionID instead of InvocationID because ADK uses different
	// invocation IDs for agent vs model callbacks
	spans map[string]map[string]trace.Span
	lock  sync.Mutex
}

// NewCallbacks creates a new set of tracing callbacks for ADK agents.
// By default, it uses the global TracerProvider. You can customize this with options.
//
// Example:
//
//	callbacks := adk.NewCallbacks()
func NewCallbacks(opts ...CallbacksOption) Callbacks {
	cb := &callbacksImpl{
		logger: logger.Discard(),
		tracer: otel.GetTracerProvider().Tracer("braintrust"),
		spans:  make(map[string]map[string]trace.Span),
	}
	for _, opt := range opts {
		opt(cb)
	}
	return cb
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
	c.lock.Lock()
	defer c.lock.Unlock()

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

func (c *callbacksImpl) retrieveSpan(sessionID string, spanKey string) (trace.Span, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

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

func (c *callbacksImpl) deleteSpan(sessionID string, spanKey string) {
	c.lock.Lock()
	defer c.lock.Unlock()

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
	c.logger.Debug(
		"BeforeAgent callback",
		"sessionID", ctx.SessionID(),
		"invocationID", ctx.InvocationID(),
		"agent", ctx.AgentName(),
		"branch", ctx.Branch(),
	)

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
	c.logger.Debug(
		"AfterAgent callback",
		"sessionID", ctx.SessionID(),
		"invocationID", ctx.InvocationID(),
		"agentName", ctx.AgentName(),
	)

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
	c.logger.Debug(
		"BeforeModel callback",
		"sessionID", ctx.SessionID(),
		"invocationID", ctx.InvocationID(),
		"agentName", ctx.AgentName(),
		"request", req,
	)

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
		c.logger.Debug("Failed to set braintrust.span_attributes", "error", err)
	}

	if req.Model != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", req.Model))
	}
	if len(req.Contents) > 0 {
		err := internal.SetJSONAttr(span, "braintrust.input_json", cleanupJSON(c.logger, req))
		if err != nil {
			c.logger.Debug("Failed to set braintrust.input_json", "error", err)
		}
	}

	c.storeSpan(ctx.SessionID(), getModelSpanKey(ctx), span)

	return nil, nil
}

// AfterModel is called after receiving a response from the LLM model.
// It completes the span created by BeforeModel and records the response.
func (c *callbacksImpl) AfterModel(ctx agent.CallbackContext, resp *model.LLMResponse, err error) (*model.LLMResponse, error) {
	c.logger.Debug(
		"AfterModel callback",
		"sessionID", ctx.SessionID(),
		"invocationID", ctx.InvocationID(),
		"agentName", ctx.AgentName(),
		"response", resp,
		"error", err,
	)

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
		err = internal.SetJSONAttr(span, "braintrust.output_json", cleanupJSON(c.logger, resp))
		if err != nil {
			c.logger.Debug("Failed to set braintrust.output_json", "error", err)
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
	c.logger.Debug(
		"BeforeTool callback",
		"sessionID", ctx.SessionID(),
		"invocationID", ctx.InvocationID(),
		"agentName", ctx.AgentName(),
		"tool", t.Name(),
		"args", args,
	)

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
		c.logger.Debug("Failed to set braintrust.span_attributes", "error", err)
	}

	span.SetAttributes(attribute.String("tool.name", t.Name()))
	if desc := t.Description(); desc != "" {
		span.SetAttributes(attribute.String("tool.description", desc))
	}
	if len(args) > 0 {
		err := internal.SetJSONAttr(span, "tool.input", args)
		if err != nil {
			c.logger.Debug("Failed to set tool.input", "error", err)
		}
		err = internal.SetJSONAttr(span, "braintrust.input_json", args)
		if err != nil {
			c.logger.Debug("Failed to set braintrust.input_json", "error", err)
		}
	}

	c.storeSpan(ctx.SessionID(), getToolSpanKey(ctx), span)

	return nil, nil
}

// AfterTool is called after a tool execution completes.
// It completes the span created by BeforeTool and records the result.
func (c *callbacksImpl) AfterTool(ctx tool.Context, t tool.Tool, args, result map[string]any, err error) (map[string]any, error) {
	c.logger.Debug(
		"AfterTool callback",
		"sessionID", ctx.SessionID(),
		"invocationID", ctx.InvocationID(),
		"agentName", ctx.AgentName(),
		"tool", t.Name(),
		"result", result,
		"error", err,
	)

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
			c.logger.Debug("Failed to set tool.output", "error", err)
		}
		err = internal.SetJSONAttr(span, "braintrust.output_json", result)
		if err != nil {
			c.logger.Debug("Failed to set braintrust.output_json", "error", err)
		}
	}

	return nil, nil
}

// Option is a functional option for configuring callback instances.
type Option func() Callbacks

// WithCallback wraps a Callbacks instance as an Option.
func WithCallback(c Callbacks) Option {
	return func() Callbacks {
		return c
	}
}

// AddLLMAgentCallbacks modifies a llmagent.Config with Braintrust tracing callbacks.
// It automatically adds all tracing callbacks (BeforeAgent, AfterAgent, BeforeModel, AfterModel, BeforeTool, AfterTool).
// If an input callbacks object is passed in, that is used, otherwise we fallback to the global instance.
//
// Example:
//
//	cfg := llmagent.Config{
//	  Name:        "my-agent",
//	  Model:       model,
//	  Description: "My agent",
//	  Tools:       tools,
//	}
//	adk.AddLLMAgentCallbacks(&cfg)
//	agent, err := llmagent.New(cfg)
func AddLLMAgentCallbacks(config *llmagent.Config, opts ...Option) {
	cb := globalCallbacks
	for _, opt := range opts {
		cb = opt()
	}
	config.BeforeAgentCallbacks = append(config.BeforeAgentCallbacks, cb.BeforeAgent)
	config.AfterAgentCallbacks = append(config.AfterAgentCallbacks, cb.AfterAgent)
	config.BeforeModelCallbacks = append(config.BeforeModelCallbacks, cb.BeforeModel)
	config.AfterModelCallbacks = append(config.AfterModelCallbacks, cb.AfterModel)
	config.BeforeToolCallbacks = append(config.BeforeToolCallbacks, cb.BeforeTool)
	config.AfterToolCallbacks = append(config.AfterToolCallbacks, cb.AfterTool)
}

// AddAgentCallbacks modifies an agent.Config with Braintrust tracing callbacks.
// It automatically adds all tracing callbacks (BeforeAgent, AfterAgent).
// If an input callbacks object is passed in, that is used, otherwise we fallback to the global instance.
//
// Example:
//
//	cfg := agent.Config{
//	  Name:        "my-agent",
//	  SubAgents:   []agent.Agent{subAgent},
//	}
//	adk.AddAgentCallbacks(&cfg)
//	agent, err := loopagent.New(cfg)
func AddAgentCallbacks(config *agent.Config, opts ...Option) {
	cb := globalCallbacks
	for _, opt := range opts {
		cb = opt()
	}
	config.BeforeAgentCallbacks = append(config.BeforeAgentCallbacks, cb.BeforeAgent)
	config.AfterAgentCallbacks = append(config.AfterAgentCallbacks, cb.AfterAgent)
}
