// Package tracea2a provides OpenTelemetry tracing for Agent2Agent (A2A)
// protocol clients and servers using the official Go SDK
// (github.com/a2aproject/a2a-go).
//
//	client, err := a2aclient.NewFromCard(ctx, card)
//	tracea2a.InstrumentClient(client)
//
//	requestHandler := a2asrv.NewHandler(executor, tracea2a.InstrumentServer())
package tracea2a

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/a2aproject/a2a-go/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// Explicit instance instead of otel.GetTextMapPropagator(), which defaults
// to a no-op unless the app configured one itself.
var a2aPropagator = propagation.TraceContext{}

// callMetaCarrier adapts a2aclient.CallMeta to propagation.TextMapCarrier.
type callMetaCarrier a2aclient.CallMeta

func (c callMetaCarrier) Get(key string) string {
	vals := a2aclient.CallMeta(c).Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (c callMetaCarrier) Set(key, value string) {
	meta := a2aclient.CallMeta(c)
	canonicalKey := strings.ToLower(key)
	for existingKey := range meta {
		if existingKey != canonicalKey && strings.EqualFold(existingKey, key) {
			delete(meta, existingKey)
		}
	}
	meta[canonicalKey] = []string{value}
}

func (c callMetaCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// requestMetaCarrier adapts a2asrv.RequestMeta to propagation.TextMapCarrier.
type requestMetaCarrier struct {
	meta *a2asrv.RequestMeta
}

func (c requestMetaCarrier) Get(key string) string {
	vals, ok := c.meta.Get(key)
	if !ok || len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (c requestMetaCarrier) Set(string, string) {} // extraction only

func (c requestMetaCarrier) Keys() []string {
	var keys []string
	for k := range c.meta.List() {
		keys = append(keys, k)
	}
	return keys
}

// streamingMethods' response is an iter.Seq2[a2a.Event, error]: After fires
// once per event instead of once per call.
var streamingMethods = map[string]bool{
	"SendStreamingMessage": true,
	"OnSendMessageStream":  true,
}

type spanContextKey struct{}
type suppressedContextKey struct{}

// callState accumulates every event payload seen across a call so the final
// output reflects everything produced, not just the last event.
//
// callID identifies the SDK request shared by every interceptor attached to
// one call. This suppresses duplicate instrumentation without suppressing a
// nested A2A call that inherits the parent call's context.
type callState struct {
	mu           sync.Mutex
	span         trace.Span
	events       []any
	started      time.Time
	firstContent bool
	callID       any
	owner        any
	ended        bool

	stopWatch func() bool
}

func (s *callState) addEvent(payload any) {
	if payload == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, payload)
}

func (s *callState) snapshotEvents() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]any(nil), s.events...)
}

func (s *callState) recordFirstContent(payload any) {
	if !hasContent(payload) {
		return
	}

	s.mu.Lock()
	if s.firstContent || s.ended {
		s.mu.Unlock()
		return
	}
	s.firstContent = true
	_ = internal.SetJSONAttr(s.span, "braintrust.metrics", map[string]float64{
		"time_to_first_token": time.Since(s.started).Seconds(),
	})
	s.mu.Unlock()
}

// setStopWatch is mutex-guarded because context.AfterFunc invokes its
// callback immediately, in its own goroutine, when ctx is already done -
// racing against the assignment of its own stop function.
func (s *callState) setStopWatch(stop func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopWatch = stop
}

// end runs exactly once per call, however it's triggered.
func (s *callState) end(fn func()) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	stop := s.stopWatch
	s.mu.Unlock()

	fn()
	s.span.End()
	if stop != nil {
		stop()
	}
}

func tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("github.com/braintrustdata/braintrust-sdk-go/trace/contrib/a2a")
}

// InstrumentClient adds Braintrust tracing to eligible direct message calls
// made by an a2aclient.Client. Detached task lifecycle and administration
// methods are intentionally excluded.
// Streaming calls that may stop iteration before a terminal event must use a
// cancellable context and cancel it when iteration stops.
func InstrumentClient(client *a2aclient.Client) {
	if client == nil {
		return
	}
	client.AddCallInterceptor(NewClientInterceptor())
}

// NewClientInterceptor is exposed for a2aclient.WithInterceptors(...); most
// callers should use InstrumentClient instead. The same streaming context
// requirements documented on InstrumentClient apply.
func NewClientInterceptor() a2aclient.CallInterceptor {
	return &clientTracer{tr: tracer()}
}

// InstrumentServer returns a RequestHandlerOption for a2asrv.NewHandler.
func InstrumentServer() a2asrv.RequestHandlerOption {
	return a2asrv.WithCallInterceptor(&serverTracer{tr: tracer()})
}

func isCurrentCall(ctx context.Context, callID any) bool {
	state, ok := ctx.Value(spanContextKey{}).(*callState)
	return ok && state.callID == callID
}

func shouldInstrumentCall(method string, payload any) bool {
	switch method {
	case "SendStreamingMessage", "OnSendMessageStream":
		return true
	case "SendMessage", "OnSendMessage":
		params, ok := payload.(*a2a.MessageSendParams)
		return !ok || params == nil || params.Config == nil || params.Config.Blocking == nil || *params.Config.Blocking
	default:
		// Detached task lifecycle, push notification, agent card, and other
		// administration calls are outside auto-instrumentation eligibility.
		return false
	}
}

func instrumentationSuppressed(ctx context.Context) bool {
	suppressed, _ := ctx.Value(suppressedContextKey{}).(bool)
	return suppressed
}

// startCall starts a span, stores its callState in ctx, and arms a
// context.AfterFunc that ends the span as abandoned if ctx finishes before
// finish() does. It only no-ops for another interceptor observing the same SDK
// request; nested A2A calls receive their own child span.
func startCall(ctx context.Context, tr trace.Tracer, name, method, role string, kind trace.SpanKind, payload, callID, owner any) context.Context {
	if isCurrentCall(ctx, callID) {
		return ctx
	}

	started := time.Now()
	ctx, span := tr.Start(ctx, name, trace.WithSpanKind(kind))
	setMetadata(span, method, role)
	setInput(span, inputForCall(payload))
	_ = internal.SetJSONAttr(span, "braintrust.span_attributes", map[string]string{
		"name": name,
		"type": "task",
	})

	state := &callState{span: span, started: started, callID: callID, owner: owner}
	stop := context.AfterFunc(ctx, func() {
		state.end(func() {
			setOutput(state.span, state.snapshotEvents())
			state.span.SetStatus(codes.Error, "stream abandoned: call context ended before a terminal event")
		})
	})
	state.setStopWatch(stop)

	return context.WithValue(ctx, spanContextKey{}, state)
}

// clientTracer implements a2aclient.CallInterceptor.
type clientTracer struct {
	a2aclient.PassthroughInterceptor
	tr trace.Tracer
}

func (c *clientTracer) Before(ctx context.Context, req *a2aclient.Request) (context.Context, error) {
	if !shouldInstrumentCall(req.Method, req.Payload) {
		return context.WithValue(ctx, suppressedContextKey{}, true), nil
	}

	ctx = context.WithValue(ctx, suppressedContextKey{}, false)
	ctx = startCall(ctx, c.tr, "a2a."+req.Method, req.Method, "client", trace.SpanKindClient, req.Payload, req, c)
	if req.Meta == nil {
		req.Meta = a2aclient.CallMeta{}
	}
	a2aPropagator.Inject(ctx, callMetaCarrier(req.Meta))
	return ctx, nil
}

func (c *clientTracer) After(ctx context.Context, resp *a2aclient.Response) error {
	if instrumentationSuppressed(ctx) {
		return nil
	}
	state, ok := ctx.Value(spanContextKey{}).(*callState)
	if !ok || state.owner != c {
		return nil
	}
	finish(state, resp.Payload, resp.Err, streamingMethods[resp.Method])
	return nil
}

// serverTracer implements a2asrv.CallInterceptor.
type serverTracer struct {
	a2asrv.PassthroughCallInterceptor
	tr trace.Tracer
}

func (s *serverTracer) Before(ctx context.Context, callCtx *a2asrv.CallContext, req *a2asrv.Request) (context.Context, error) {
	if !shouldInstrumentCall(callCtx.Method(), req.Payload) {
		return context.WithValue(ctx, suppressedContextKey{}, true), nil
	}
	if isCurrentCall(ctx, req) {
		return context.WithValue(ctx, suppressedContextKey{}, false), nil
	}
	ctx = context.WithValue(ctx, suppressedContextKey{}, false)
	ctx = a2aPropagator.Extract(ctx, requestMetaCarrier{meta: callCtx.RequestMeta()})
	return startCall(ctx, s.tr, "a2a."+callCtx.Method(), callCtx.Method(), "server", trace.SpanKindServer, req.Payload, req, s), nil
}

func (s *serverTracer) After(ctx context.Context, callCtx *a2asrv.CallContext, resp *a2asrv.Response) error {
	if instrumentationSuppressed(ctx) {
		return nil
	}
	state, ok := ctx.Value(spanContextKey{}).(*callState)
	if !ok || state.owner != s {
		return nil
	}
	finish(state, resp.Payload, resp.Err, streamingMethods[callCtx.Method()])
	return nil
}

// finish accumulates payload and ends the span once the call reaches a
// terminal point (a non-streaming call is always terminal on its first
// response). Streamed content usually arrives via non-terminal
// TaskArtifactUpdateEvents before a final TaskStatusUpdateEvent that carries
// no content itself, so output must be the accumulated events, not just the
// last one.
func finish(state *callState, payload any, err error, streaming bool) {
	if err != nil {
		state.end(func() {
			state.span.RecordError(err)
			state.span.SetStatus(codes.Error, err.Error())
		})
		return
	}

	if streaming {
		state.recordFirstContent(payload)
	}
	state.addEvent(payload)

	if streaming && !isTerminalEvent(payload) {
		return
	}

	state.end(func() {
		setOutput(state.span, state.snapshotEvents())
	})
}

// isTerminalEvent covers a2a-go v0.3.15's four Event/SendMessageResult
// implementers (Message, Task, TaskStatusUpdateEvent, TaskArtifactUpdateEvent).
// A new variant added upstream would fall into default (non-terminal) until
// this is updated - the context.AfterFunc fallback in startCall still ends
// the span once the call's context ends.
func isTerminalEvent(payload any) bool {
	switch v := payload.(type) {
	case *a2a.Message:
		return true
	case *a2a.Task:
		return v.Status.State.Terminal() || v.Status.State == a2a.TaskStateInputRequired
	case *a2a.TaskStatusUpdateEvent:
		return v.Final
	case *a2a.TaskArtifactUpdateEvent:
		return false
	default:
		return false
	}
}

// A2A task metadata is intentionally limited to this explicit allowlist.
func setMetadata(span trace.Span, method, role string) {
	_ = internal.SetJSONAttr(span, "braintrust.metadata", map[string]any{
		"method":   method,
		"protocol": "a2a",
		"role":     role,
	})
}

func inputForCall(payload any) any {
	params, ok := payload.(*a2a.MessageSendParams)
	if !ok || params == nil {
		return nil
	}
	return params.Message
}

func setInput(span trace.Span, input any) {
	if input == nil {
		return
	}
	_ = internal.SetJSONAttr(span, "braintrust.input_json", input)
}

func hasContent(payload any) bool {
	switch v := payload.(type) {
	case *a2a.Message:
		return v != nil && len(v.Parts) > 0
	case *a2a.Task:
		if v == nil {
			return false
		}
		if v.Status.Message != nil && len(v.Status.Message.Parts) > 0 {
			return true
		}
		for _, artifact := range v.Artifacts {
			if artifact != nil && len(artifact.Parts) > 0 {
				return true
			}
		}
	case *a2a.TaskStatusUpdateEvent:
		return v != nil && v.Status.Message != nil && len(v.Status.Message.Parts) > 0
	case *a2a.TaskArtifactUpdateEvent:
		return v != nil && v.Artifact != nil && len(v.Artifact.Parts) > 0
	}
	return false
}

// setOutput reduces streamed task updates into the same Task or Message shape
// returned by a non-streaming call.
func setOutput(span trace.Span, events []any) {
	output := aggregateOutput(events)
	if output == nil {
		return
	}
	_ = internal.SetJSONAttr(span, "braintrust.output_json", output)
}

func aggregateOutput(events []any) any {
	if len(events) == 0 {
		return nil
	}
	if len(events) == 1 {
		switch events[0].(type) {
		case *a2a.Message, *a2a.Task:
			return events[0]
		}
	}

	var task *a2a.Task
	for _, event := range events {
		switch v := event.(type) {
		case *a2a.Message:
			return v
		case *a2a.Task:
			task = cloneTask(v)
		case *a2a.TaskStatusUpdateEvent:
			if v == nil {
				continue
			}
			task = ensureTask(task, v.TaskID, v.ContextID)
			task.Status = v.Status
			if v.Metadata != nil {
				if task.Metadata == nil {
					task.Metadata = make(map[string]any, len(v.Metadata))
				}
				maps.Copy(task.Metadata, v.Metadata)
			}
		case *a2a.TaskArtifactUpdateEvent:
			if v == nil || v.Artifact == nil {
				continue
			}
			task = ensureTask(task, v.TaskID, v.ContextID)
			applyArtifactUpdate(task, v)
		default:
			return events
		}
	}
	return task
}

func ensureTask(task *a2a.Task, taskID a2a.TaskID, contextID string) *a2a.Task {
	if task != nil {
		return task
	}
	return &a2a.Task{ID: taskID, ContextID: contextID}
}

func cloneTask(task *a2a.Task) *a2a.Task {
	if task == nil {
		return nil
	}
	cloned := *task
	cloned.Artifacts = make([]*a2a.Artifact, len(task.Artifacts))
	for i, artifact := range task.Artifacts {
		cloned.Artifacts[i] = cloneArtifact(artifact)
	}
	cloned.History = slices.Clone(task.History)
	cloned.Metadata = maps.Clone(task.Metadata)
	return &cloned
}

func cloneArtifact(artifact *a2a.Artifact) *a2a.Artifact {
	if artifact == nil {
		return nil
	}
	cloned := *artifact
	cloned.Parts = slices.Clone(artifact.Parts)
	cloned.Extensions = slices.Clone(artifact.Extensions)
	cloned.Metadata = maps.Clone(artifact.Metadata)
	return &cloned
}

func applyArtifactUpdate(task *a2a.Task, event *a2a.TaskArtifactUpdateEvent) {
	artifact := cloneArtifact(event.Artifact)
	index := slices.IndexFunc(task.Artifacts, func(existing *a2a.Artifact) bool {
		return existing != nil && existing.ID == artifact.ID
	})
	if index < 0 {
		task.Artifacts = append(task.Artifacts, artifact)
		return
	}
	if !event.Append {
		task.Artifacts[index] = artifact
		return
	}

	existing := task.Artifacts[index]
	existing.Parts = append(existing.Parts, artifact.Parts...)
	if artifact.Metadata != nil {
		if existing.Metadata == nil {
			existing.Metadata = make(map[string]any, len(artifact.Metadata))
		}
		maps.Copy(existing.Metadata, artifact.Metadata)
	}
}
