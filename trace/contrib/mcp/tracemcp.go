// Package tracemcp provides OpenTelemetry tracing for Model Context Protocol (MCP)
// clients and servers using the official Go SDK.
//
// First, set up tracing with braintrust.New():
//
//	tp := trace.NewTracerProvider()
//	defer tp.Shutdown(context.Background())
//	otel.SetTracerProvider(tp)
//
//	bt, err := braintrust.New(tp, braintrust.WithProject("my-project"))
//	if err != nil {
//		log.Fatal(err)
//	}
//
// Then instrument your MCP client and/or server before Connect:
//
//	client := mcp.NewClient(&mcp.Implementation{Name: "my-client"}, nil)
//	tracemcp.InstrumentClient(client)
//
//	server := mcp.NewServer(&mcp.Implementation{Name: "my-server"}, nil)
//	tracemcp.InstrumentServer(server)
//
// InstrumentClient traces ClientSession.CallTool and ClientSession.ListTools via
// client middleware. InstrumentServer traces incoming tools/call (including the
// registered tool handler) and tools/list.
package tracemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

const (
	methodCallTool            = "tools/call"
	methodListTools           = "tools/list"
	methodProgress            = "notifications/progress"
	clientSessionCallToolAPI  = "ClientSession.CallTool"
	clientSessionListToolsAPI = "ClientSession.ListTools"
)

type side int

const (
	sideClient side = iota
	sideServer
)

type progressKey struct {
	sessionID string
	token     string
}

type activeCall struct {
	span     trace.Span
	progress []map[string]any
}

type callTracker struct {
	mu    sync.Mutex
	calls map[progressKey]*activeCall
}

func newCallTracker() *callTracker {
	return &callTracker{calls: make(map[progressKey]*activeCall)}
}

func (t *callTracker) register(session mcp.Session, token any, span trace.Span) *activeCall {
	key, ok := progressKeyFor(session, token)
	if !ok {
		return nil
	}
	call := &activeCall{span: span}
	t.mu.Lock()
	t.calls[key] = call
	t.mu.Unlock()
	return call
}

func (t *callTracker) unregister(session mcp.Session, token any) {
	key, ok := progressKeyFor(session, token)
	if !ok {
		return
	}
	t.mu.Lock()
	delete(t.calls, key)
	t.mu.Unlock()
}

func (t *callTracker) record(session mcp.Session, params *mcp.ProgressNotificationParams) {
	if params == nil {
		return
	}
	key, ok := progressKeyFor(session, params.ProgressToken)
	if !ok {
		return
	}
	entry := map[string]any{
		"progress": params.Progress,
	}
	if params.Total > 0 {
		entry["total"] = params.Total
	}
	if params.Message != "" {
		entry["message"] = params.Message
	}

	t.mu.Lock()
	call := t.calls[key]
	t.mu.Unlock()
	if call == nil {
		return
	}

	call.progress = append(call.progress, entry)
	call.span.AddEvent("mcp.progress", trace.WithAttributes(
		attribute.Float64("progress", params.Progress),
		attribute.Float64("total", params.Total),
		attribute.String("message", params.Message),
	))
}

func progressKeyFor(session mcp.Session, token any) (progressKey, bool) {
	if token == nil {
		return progressKey{}, false
	}
	return progressKey{
		sessionID: session.ID(),
		token:     fmt.Sprint(token),
	}, true
}

func progressToken(params mcp.Params) any {
	switch p := params.(type) {
	case *mcp.CallToolParams:
		if p != nil {
			return p.GetProgressToken()
		}
	case *mcp.CallToolParamsRaw:
		if p != nil {
			return p.GetProgressToken()
		}
	}
	return nil
}

// InstrumentClient adds Braintrust tracing middleware to an MCP client.
// It traces ClientSession.CallTool and ClientSession.ListTools, including
// in-flight progress notifications received during CallTool.
func InstrumentClient(c *mcp.Client) {
	if c == nil {
		return
	}
	tracker := newCallTracker()
	tracer := otel.GetTracerProvider().Tracer("braintrust")
	c.AddSendingMiddleware(tracingMiddleware(tracer, tracker, sideClient))
	c.AddReceivingMiddleware(progressMiddleware(tracer, tracker, sideClient))
}

// InstrumentServer adds Braintrust tracing middleware to an MCP server.
// It traces tools/list and tools/call, including the registered tool handler
// and progress notifications emitted while a tool runs.
func InstrumentServer(s *mcp.Server) {
	if s == nil {
		return
	}
	tracker := newCallTracker()
	tracer := otel.GetTracerProvider().Tracer("braintrust")
	s.AddReceivingMiddleware(tracingMiddleware(tracer, tracker, sideServer))
	s.AddSendingMiddleware(progressMiddleware(tracer, tracker, sideServer))
}

func tracingMiddleware(tracer trace.Tracer, tracker *callTracker, from side) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case methodCallTool, methodListTools:
			default:
				return next(ctx, method, req)
			}

			if from == sideServer && method == methodCallTool {
				return traceServerCallTool(ctx, tracer, tracker, next, req)
			}

			spanName := spanName(method, req)
			spanKind := trace.SpanKindClient
			if from == sideServer {
				spanKind = trace.SpanKindInternal
			}

			ctx, span := tracer.Start(ctx, spanName, trace.WithSpanKind(spanKind))
			defer span.End()

			setSpanType(span, method)
			setMetadata(span, method, req, from)
			setInput(span, method, req)

			var active *activeCall
			if method == methodCallTool {
				active = tracker.register(req.GetSession(), progressToken(req.GetParams()), span)
				defer tracker.unregister(req.GetSession(), progressToken(req.GetParams()))
			}

			result, err := next(ctx, method, req)
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				return result, err
			}

			setOutput(span, method, result)
			if active != nil && len(active.progress) > 0 {
				appendProgressOutput(span, active.progress, result)
			}
			return result, err
		}
	}
}

func traceServerCallTool(
	ctx context.Context,
	tracer trace.Tracer,
	tracker *callTracker,
	next mcp.MethodHandler,
	req mcp.Request,
) (mcp.Result, error) {
	toolName := callToolName(req.GetParams())
	rpcName := "mcp.tools.call"
	if toolName != "" {
		rpcName = fmt.Sprintf("mcp.tools.call [%s]", toolName)
	}
	handlerName := "mcp.tools.handler"
	if toolName != "" {
		handlerName = fmt.Sprintf("mcp.tools.handler [%s]", toolName)
	}

	ctx, rpcSpan := tracer.Start(ctx, rpcName, trace.WithSpanKind(trace.SpanKindServer))
	defer rpcSpan.End()

	ctx, handlerSpan := tracer.Start(ctx, handlerName, trace.WithSpanKind(trace.SpanKindInternal))
	defer handlerSpan.End()

	setSpanType(handlerSpan, methodCallTool)
	setMetadata(rpcSpan, methodCallTool, req, sideServer)
	setMetadata(handlerSpan, methodCallTool, req, sideServer)
	setInput(handlerSpan, methodCallTool, req)

	token := progressToken(req.GetParams())
	active := tracker.register(req.GetSession(), token, handlerSpan)
	defer tracker.unregister(req.GetSession(), token)

	result, err := next(ctx, methodCallTool, req)
	if err != nil {
		handlerSpan.RecordError(err)
		handlerSpan.SetStatus(codes.Error, err.Error())
		rpcSpan.RecordError(err)
		rpcSpan.SetStatus(codes.Error, err.Error())
		return result, err
	}

	setOutput(handlerSpan, methodCallTool, result)
	if active != nil && len(active.progress) > 0 {
		appendProgressOutput(handlerSpan, active.progress, result)
	}
	return result, err
}

func progressMiddleware(_ trace.Tracer, tracker *callTracker, from side) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodProgress {
				return next(ctx, method, req)
			}
			params, ok := req.GetParams().(*mcp.ProgressNotificationParams)
			if ok && params != nil {
				switch from {
				case sideClient:
					tracker.record(req.GetSession(), params)
				case sideServer:
					tracker.record(req.GetSession(), params)
				}
			}
			return next(ctx, method, req)
		}
	}
}

func spanName(method string, req mcp.Request) string {
	switch method {
	case methodCallTool:
		if name := callToolName(req.GetParams()); name != "" {
			return fmt.Sprintf("mcp.tools.call [%s]", name)
		}
		return "mcp.tools.call"
	case methodListTools:
		return "mcp.tools.list"
	default:
		return method
	}
}

func setSpanType(span trace.Span, method string) {
	spanType := "task"
	if method == methodCallTool {
		spanType = "tool"
	}
	_ = internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{
		"type": spanType,
	})
}

func setMetadata(span trace.Span, method string, req mcp.Request, from side) {
	metadata := map[string]any{
		"provider": "mcp",
		"method":   method,
	}
	if from == sideClient {
		metadata["role"] = "client"
		if method == methodCallTool {
			metadata["api"] = clientSessionCallToolAPI
		}
		if method == methodListTools {
			metadata["api"] = clientSessionListToolsAPI
		}
	} else {
		metadata["role"] = "server"
	}
	if method == methodCallTool {
		if name := callToolName(req.GetParams()); name != "" {
			metadata["name"] = name
		}
	}
	_ = internal.SetJSONAttr(span, "braintrust.metadata", metadata)
}

func setInput(span trace.Span, method string, req mcp.Request) {
	switch method {
	case methodCallTool:
		if input := callToolInput(req.GetParams()); input != nil {
			_ = internal.SetJSONAttr(span, "braintrust.input_json", input)
		}
	case methodListTools:
		params, ok := req.GetParams().(*mcp.ListToolsParams)
		if !ok || params == nil {
			return
		}
		input := map[string]any{}
		if params.Cursor != "" {
			input["cursor"] = params.Cursor
		}
		_ = internal.SetJSONAttr(span, "braintrust.input_json", input)
	}
}

func setOutput(span trace.Span, method string, result mcp.Result) {
	switch method {
	case methodCallTool:
		res, ok := result.(*mcp.CallToolResult)
		if !ok || res == nil {
			return
		}
		_ = internal.SetJSONAttr(span, "braintrust.output_json", callToolOutput(res))
	case methodListTools:
		res, ok := result.(*mcp.ListToolsResult)
		if !ok || res == nil {
			return
		}
		_ = internal.SetJSONAttr(span, "braintrust.output_json", listToolsOutput(res))
	}
}

func appendProgressOutput(span trace.Span, progress []map[string]any, result mcp.Result) {
	output := map[string]any{"progress": progress}
	if res, ok := result.(*mcp.CallToolResult); ok {
		if toolOutput := callToolOutput(res); toolOutput != nil {
			for k, v := range toolOutput {
				output[k] = v
			}
		}
	}
	_ = internal.SetJSONAttr(span, "braintrust.output_json", output)
}

func callToolName(params mcp.Params) string {
	switch p := params.(type) {
	case *mcp.CallToolParams:
		if p != nil {
			return p.Name
		}
	case *mcp.CallToolParamsRaw:
		if p != nil {
			return p.Name
		}
	}
	return ""
}

func callToolInput(params mcp.Params) map[string]any {
	switch p := params.(type) {
	case *mcp.CallToolParams:
		if p == nil {
			return nil
		}
		input := map[string]any{"name": p.Name}
		if p.Arguments != nil {
			input["arguments"] = p.Arguments
		}
		return input
	case *mcp.CallToolParamsRaw:
		if p == nil {
			return nil
		}
		input := map[string]any{"name": p.Name}
		if len(p.Arguments) > 0 {
			var args any
			if err := json.Unmarshal(p.Arguments, &args); err == nil {
				input["arguments"] = args
			} else {
				input["arguments"] = json.RawMessage(p.Arguments)
			}
		}
		return input
	default:
		return nil
	}
}

func callToolOutput(result *mcp.CallToolResult) map[string]any {
	output := map[string]any{}
	if result.IsError {
		output["is_error"] = true
	}
	if result.StructuredContent != nil {
		output["structured_content"] = result.StructuredContent
	}
	if texts := contentTexts(result.Content); len(texts) == 1 {
		output["content"] = texts[0]
	} else if len(texts) > 1 {
		output["content"] = texts
	}
	if len(output) == 0 {
		return nil
	}
	return output
}

func listToolsOutput(result *mcp.ListToolsResult) map[string]any {
	tools := make([]map[string]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		if tool == nil {
			continue
		}
		entry := map[string]string{"name": tool.Name}
		if tool.Description != "" {
			entry["description"] = tool.Description
		}
		tools = append(tools, entry)
	}
	output := map[string]any{
		"tools": tools,
		"count": len(tools),
	}
	if result.NextCursor != "" {
		output["next_cursor"] = result.NextCursor
	}
	return output
}

func contentTexts(content []mcp.Content) []string {
	texts := make([]string, 0, len(content))
	for _, item := range content {
		if text, ok := item.(*mcp.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return texts
}
