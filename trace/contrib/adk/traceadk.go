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
	"slices"
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

	// spans stores active spans keyed by session ID and call identity.
	// Parallel agents use different invocation IDs from their parents.
	spans     map[string]map[string]trace.Span
	agentRuns map[string][]agentRun
	lock      sync.Mutex
}

// agentRun tracks an active call in start order. Sequential calls share a branch;
// parallel calls have distinct branches, so the list can identify each caller.
type agentRun struct {
	key    string
	span   trace.Span
	name   string
	branch string
}

// NewCallbacks creates a new set of tracing callbacks for ADK agents.
// By default, it uses the global TracerProvider. You can customize this with options.
// Reuse the callbacks across an agent tree to preserve nesting between runs.
//
// Example:
//
//	callbacks := adk.NewCallbacks()
func NewCallbacks(opts ...CallbacksOption) Callbacks {
	cb := &callbacksImpl{
		logger:    logger.Discard(),
		tracer:    otel.GetTracerProvider().Tracer("braintrust"),
		spans:     make(map[string]map[string]trace.Span),
		agentRuns: make(map[string][]agentRun),
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

func getAgentSpanKey(ctx agent.CallbackContext) string {
	return agentSpanKey(ctx.InvocationID(), ctx.Branch(), ctx.AgentName())
}

func agentSpanKey(invocationID, branch, name string) string {
	// Sequential agents share an invocation ID; parallel agents have distinct
	// branches. Include both the agent and branch to keep their spans separate.
	return fmt.Sprintf("agent:%q:%q:%q", invocationID, branch, name)
}

func getModelSpanKey(ctx agent.CallbackContext) string {
	return "model:" + getAgentSpanKey(ctx)
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
		if strings.HasPrefix(spanKey, "agent:") {
			runs := c.agentRuns[sessionID]
			for i := len(runs) - 1; i >= 0; i-- {
				if runs[i].key == spanKey {
					c.agentRuns[sessionID] = slices.Delete(runs, i, i+1)
					break
				}
			}
		}

		// Release model and tool references when the last agent finishes.
		if len(c.agentRuns[sessionID]) > 0 {
			return
		}
		delete(c.spans, sessionID)
		delete(c.agentRuns, sessionID)
	}
}

// retrieveParentAgentSpan finds the latest active caller in this branch's ancestry.
// ADK runs sequential children and LLM transfers synchronously, while parallel
// children receive separate branches. Configured parents can differ from callers
// when an agent transfers to a peer.
func (c *callbacksImpl) retrieveParentAgentSpan(ctx agent.CallbackContext) (trace.Span, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	runs := c.agentRuns[ctx.SessionID()]
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.branch == "" || ctx.Branch() == run.branch || strings.HasPrefix(ctx.Branch(), run.branch+".") {
			return run.span, true
		}
	}
	return nil, false
}

// retrieveAgentSpan also handles LLM callbacks, whose invocation ID differs
// from the enclosing agent callback in ADK v0.4.
func (c *callbacksImpl) retrieveAgentSpan(ctx agent.CallbackContext) (trace.Span, bool) {
	if span, ok := c.retrieveSpan(ctx.SessionID(), getAgentSpanKey(ctx)); ok {
		return span, true
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	runs := c.agentRuns[ctx.SessionID()]
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.name == ctx.AgentName() && run.branch == ctx.Branch() {
			return run.span, true
		}
	}
	return nil, false
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

	var spanCtx context.Context = ctx
	parentSpan, hasParent := c.retrieveParentAgentSpan(ctx)
	if !hasParent {
		parentSpan, hasParent = c.retrieveRootSpan(ctx.SessionID())
	}
	if hasParent {
		spanCtx = trace.ContextWithSpan(ctx, parentSpan)
	}

	// Create agent span (root or child based on parent)
	_, span := c.tracer.Start(spanCtx, fmt.Sprintf("agent_run [%s]", ctx.AgentName()),
		trace.WithSpanKind(trace.SpanKindInternal))
	setSpanAttributes(span, ctx)

	spanKey := getAgentSpanKey(ctx)
	c.storeSpan(ctx.SessionID(), spanKey, span)
	c.lock.Lock()
	c.agentRuns[ctx.SessionID()] = append(c.agentRuns[ctx.SessionID()], agentRun{
		key: spanKey, span: span, name: ctx.AgentName(), branch: ctx.Branch(),
	})
	c.lock.Unlock()

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
	span, ok := c.retrieveSpan(ctx.SessionID(), getAgentSpanKey(ctx))
	if ok {
		defer span.End()
		// Delete this agent's span
		c.deleteSpan(ctx.SessionID(), getAgentSpanKey(ctx))
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
	parentSpan, hasParent := c.retrieveAgentSpan(ctx)
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

	c.storeSpan(ctx.SessionID(), getModelSpanKey(ctx), &modelSpan{Span: span})

	return nil, nil
}

// AfterModel is called after receiving a response from the LLM model.
// It records the response and ends the span created by BeforeModel, unless
// the model wrapper is still consuming a stream.
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
	if s, ok := span.(*modelSpan); !ok || !s.managed.Load() {
		defer span.End()
	}

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

		if resp.UsageMetadata != nil {
			if metricsErr := internal.SetJSONAttr(span, "braintrust.metrics", usageMetrics(resp.UsageMetadata)); metricsErr != nil {
				c.logger.Debug("Failed to set braintrust.metrics", "error", metricsErr)
			}
		}

		// Record finish reason if available
		if resp.FinishReason != "" {
			span.SetAttributes(attribute.String("gen_ai.finish_reason", string(resp.FinishReason)))
		}
	}

	return nil, nil
}

// usageMetrics normalizes Google UsageMetadata to Braintrust metrics.
func usageMetrics(usage *genai.GenerateContentResponseUsageMetadata) map[string]int64 {
	if usage == nil {
		return map[string]int64{}
	}

	metrics := map[string]int64{
		"prompt_tokens":     int64(usage.PromptTokenCount) + int64(usage.ToolUsePromptTokenCount),
		"completion_tokens": int64(usage.CandidatesTokenCount) + int64(usage.ThoughtsTokenCount),
		"tokens":            int64(usage.TotalTokenCount),
	}
	if usage.ThoughtsTokenCount > 0 {
		metrics["completion_reasoning_tokens"] = int64(usage.ThoughtsTokenCount)
	}
	if usage.CachedContentTokenCount > 0 {
		metrics["prompt_cached_tokens"] = int64(usage.CachedContentTokenCount)
	}

	return metrics
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
// With callbacks from NewCallbacks, it also wraps the model to propagate the
// call_llm span to provider instrumentation, including streaming requests.
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
	if callbacks, ok := cb.(*callbacksImpl); ok && config.Model != nil {
		config.Model = &tracedModel{LLM: config.Model, callbacks: callbacks}
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
