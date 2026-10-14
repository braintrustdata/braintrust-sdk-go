package tracea2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/a2a"
	"github.com/a2aproject/a2a-go/a2aclient"
	"github.com/a2aproject/a2a-go/a2asrv"
	"github.com/a2aproject/a2a-go/a2asrv/eventqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// Tests use a real local httptest server speaking JSON-RPC rather than VCR:
// both sides are this SDK's own code, so there's no external API to record.

type echoExecutor struct{}

func (echoExecutor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, q eventqueue.Queue) error {
	reply := a2a.NewMessage(a2a.MessageRoleAgent, a2a.TextPart{Text: "hi " + textOf(reqCtx.Message)})
	return q.Write(ctx, reply)
}

func (echoExecutor) Cancel(_ context.Context, _ *a2asrv.RequestContext, _ eventqueue.Queue) error {
	return nil
}

type forwardingExecutor struct {
	client *a2aclient.Client
}

func (e forwardingExecutor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, q eventqueue.Queue) error {
	result, err := e.client.SendMessage(ctx, &a2a.MessageSendParams{Message: reqCtx.Message})
	if err != nil {
		return err
	}
	return q.Write(ctx, result)
}

func (forwardingExecutor) Cancel(_ context.Context, _ *a2asrv.RequestContext, _ eventqueue.Queue) error {
	return nil
}

type failingExecutor struct{}

func (failingExecutor) Execute(context.Context, *a2asrv.RequestContext, eventqueue.Queue) error {
	return errors.New("executor failed")
}

func (failingExecutor) Cancel(context.Context, *a2asrv.RequestContext, eventqueue.Queue) error {
	return errors.New("executor failed")
}

// multiEventExecutor emits two artifact events then a final status event -
// a real task-based stream, unlike echoExecutor's single terminal Message.
type multiEventExecutor struct{}

func (multiEventExecutor) Execute(ctx context.Context, reqCtx *a2asrv.RequestContext, q eventqueue.Queue) error {
	if err := q.Write(ctx, a2a.NewArtifactEvent(reqCtx, a2a.TextPart{Text: "part one"})); err != nil {
		return err
	}
	if err := q.Write(ctx, a2a.NewArtifactEvent(reqCtx, a2a.TextPart{Text: "part two"})); err != nil {
		return err
	}
	final := a2a.NewStatusUpdateEvent(reqCtx, a2a.TaskStateCompleted, nil)
	final.Final = true
	return q.Write(ctx, final)
}

func (multiEventExecutor) Cancel(_ context.Context, _ *a2asrv.RequestContext, _ eventqueue.Queue) error {
	return nil
}

func textOf(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	for _, part := range msg.Parts {
		if text, ok := part.(a2a.TextPart); ok {
			return text.Text
		}
	}
	return ""
}

func setupServerWithExecutor(t *testing.T, executor a2asrv.AgentExecutor) *httptest.Server {
	t.Helper()
	handler := a2asrv.NewHandler(executor, InstrumentServer())
	mux := http.NewServeMux()
	mux.Handle("/invoke", a2asrv.NewJSONRPCHandler(handler))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func setupServer(t *testing.T) *httptest.Server {
	t.Helper()
	return setupServerWithExecutor(t, echoExecutor{})
}

func setupClient(t *testing.T, serverURL string) *a2aclient.Client {
	t.Helper()
	client, err := a2aclient.NewFromEndpoints(t.Context(), []a2a.AgentInterface{
		{Transport: a2a.TransportProtocolJSONRPC, URL: serverURL + "/invoke"},
	})
	require.NoError(t, err)
	InstrumentClient(client)
	t.Cleanup(func() { _ = client.Destroy() })
	return client
}

func setupOtel(t *testing.T) *oteltest.Exporter {
	t.Helper()
	tp, exporter := oteltest.Setup(t)
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(original) })
	return exporter
}

func TestInstrumentClient_SendMessage(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServer(t)
	client := setupClient(t, server.URL)

	result, err := client.SendMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "Braintrust"}),
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	clientSpan := findSpanWithRole(t, spans, "a2a.SendMessage", "client")
	require.Equal(t, "a2a", clientSpan.Metadata()["protocol"])
	require.Contains(t, clientSpan.Attr("braintrust.input_json").String(), "Braintrust")
	require.NotContains(t, clientSpan.Attr("braintrust.input_json").String(), "configuration")
	require.Contains(t, clientSpan.Attr("braintrust.output_json").String(), "hi Braintrust")

	serverSpan := findSpanWithRole(t, spans, "a2a.OnSendMessage", "server")
	require.Equal(t, "server", serverSpan.Metadata()["role"])

	require.Equal(t, clientSpan.Stub.SpanContext.TraceID(), serverSpan.Stub.SpanContext.TraceID(),
		"client and server spans must share one trace")
	require.Equal(t, clientSpan.Stub.SpanContext.SpanID(), serverSpan.Stub.Parent.SpanID(),
		"server span must be a child of the client span")
	clientSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"name": "a2a.SendMessage",
		"type": "task",
	})
}

func TestInstrumentClient_NestedA2ACall(t *testing.T) {
	exporter := setupOtel(t)
	downstreamServer := setupServer(t)
	downstreamClient := setupClient(t, downstreamServer.URL)
	forwardingServer := setupServerWithExecutor(t, forwardingExecutor{client: downstreamClient})
	client := setupClient(t, forwardingServer.URL)

	_, err := client.SendMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "Braintrust"}),
	})
	require.NoError(t, err)

	spans := exporter.Flush()
	require.Len(t, spans, 4, "the outer and downstream A2A calls must each have client and server spans")

	byParent := make(map[trace.SpanID]oteltest.Span, len(spans))
	var root oteltest.Span
	for _, span := range spans {
		if !span.Stub.Parent.IsValid() {
			root = span
			continue
		}
		byParent[span.Stub.Parent.SpanID()] = span
	}
	require.Equal(t, "a2a.SendMessage", root.Name())
	require.Equal(t, "client", root.Metadata()["role"])

	roles := []string{"server", "client", "server"}
	current := root
	for _, role := range roles {
		child, ok := byParent[current.Stub.SpanContext.SpanID()]
		require.True(t, ok, "missing %s child of %s span", role, current.Metadata()["role"])
		require.Equal(t, role, child.Metadata()["role"])
		current = child
	}
}

func TestInstrumentClient_SendStreamingMessage(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServer(t)
	client := setupClient(t, server.URL)

	var gotFinal bool
	for event, err := range client.SendStreamingMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "Braintrust"}),
	}) {
		require.NoError(t, err)
		require.NotNil(t, event)
		gotFinal = true
	}
	require.True(t, gotFinal, "should have received at least one event")

	spans := exporter.Flush()
	clientSpan := findSpanWithRole(t, spans, "a2a.SendStreamingMessage", "client")
	require.Contains(t, clientSpan.Attr("braintrust.output_json").String(), "hi Braintrust")
	require.GreaterOrEqual(t, clientSpan.Metrics()["time_to_first_token"], float64(0))
}

// Proves non-terminal events don't end the span early, and output
// accumulates every event's content instead of just the last one.
func TestInstrumentClient_SendStreamingMessage_MultiEvent(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServerWithExecutor(t, multiEventExecutor{})
	client := setupClient(t, server.URL)

	var eventCount int
	for event, err := range client.SendStreamingMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "go"}),
	}) {
		require.NoError(t, err)
		eventCount++
		if statusUpdate, ok := event.(*a2a.TaskStatusUpdateEvent); ok {
			require.True(t, statusUpdate.Final)
			continue
		}
		require.IsType(t, &a2a.TaskArtifactUpdateEvent{}, event)
		// The server may finish sending before the client consumes its first
		// event. Only the client span must remain open until the terminal event.
		for _, span := range exporter.InMemoryExporter().GetSpans() {
			require.NotEqual(t, "a2a.SendStreamingMessage", span.Name, "client span must not be exported after a non-terminal event")
		}
	}
	require.Equal(t, 3, eventCount)

	spans := exporter.Flush()
	clientSpan := findSpanWithRole(t, spans, "a2a.SendStreamingMessage", "client")
	output := clientSpan.Attr("braintrust.output_json").String()
	assert.Contains(t, output, "part one", "first artifact must survive in accumulated output")
	assert.Contains(t, output, "part two", "second artifact must survive in accumulated output")
	assert.Contains(t, output, "completed", "final status must also be present in accumulated output")

	var task map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &task))
	require.Equal(t, "task", task["kind"], "stream output must match the non-streaming Task shape")
	require.Len(t, task["artifacts"], 2)
	require.Equal(t, "completed", task["status"].(map[string]any)["state"])
}

func TestInstrumentServer_TracesErrors(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServerWithExecutor(t, failingExecutor{})
	client := setupClient(t, server.URL)

	_, err := client.SendMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "fail"}),
	})
	require.Error(t, err)

	spans := exporter.Flush()
	clientSpan := findSpanWithRole(t, spans, "a2a.SendMessage", "client")
	require.Equal(t, "a2a", clientSpan.Metadata()["protocol"])

	assert.Equal(t, codes.Error, clientSpan.Status().Code)
	assert.NotEmpty(t, clientSpan.Status().Description)
	clientSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"name": "a2a.SendMessage",
		"type": "task",
	})

	events := clientSpan.Events()
	require.NotEmpty(t, events, "span must record an exception event on error")
	var recordedErr string
	for _, event := range events {
		for _, attr := range event.Attributes {
			if string(attr.Key) == "exception.message" {
				recordedErr = attr.Value.AsString()
			}
		}
	}
	assert.NotEmpty(t, recordedErr, "exception.message must be recorded on the span")
}

func TestInstrumentClient_DoesNotTraceDetachedLifecycleMethods(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServer(t)
	client := setupClient(t, server.URL)

	_, err := client.GetTask(context.Background(), &a2a.TaskQueryParams{ID: "does-not-exist"})
	require.Error(t, err)
	require.Empty(t, exporter.Flush(), "detached task retrieval must not be auto-instrumented")
}

func TestInstrumentClient_DoesNotTraceNonBlockingSend(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServer(t)
	client, err := a2aclient.NewFromEndpoints(t.Context(), []a2a.AgentInterface{
		{Transport: a2a.TransportProtocolJSONRPC, URL: server.URL + "/invoke"},
	}, a2aclient.WithConfig(a2aclient.Config{Polling: true}))
	require.NoError(t, err)
	InstrumentClient(client)
	t.Cleanup(func() { _ = client.Destroy() })

	_, err = client.SendMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "background"}),
	})
	require.NoError(t, err)
	require.Empty(t, exporter.Flush(), "non-blocking task submission must not be auto-instrumented")
}

func TestInstrumentClient_Idempotent(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServer(t)
	client := setupClient(t, server.URL) // already instrumented once by setupClient
	InstrumentClient(client)             // second call must be a no-op

	_, err := client.SendMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "Braintrust"}),
	})
	require.NoError(t, err)

	spans := exporter.Flush()
	clientSpans := 0
	for _, span := range spans {
		if span.Name() == "a2a.SendMessage" && span.Metadata()["role"] == "client" {
			clientSpans++
		}
	}
	assert.Equal(t, 1, clientSpans, "double InstrumentClient must not attach a duplicate interceptor")
}

// countingTracerProvider counts every Tracer.Start call, catching an
// orphaned span (created but never ended) that exported-span counting alone
// would miss.
type countingTracerProvider struct {
	trace.TracerProvider
	starts *atomic.Int64
}

func (p *countingTracerProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return &countingTracer{Tracer: p.TracerProvider.Tracer(name, opts...), starts: p.starts}
}

type countingTracer struct {
	trace.Tracer
	starts *atomic.Int64
}

func (t *countingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	t.starts.Add(1)
	return t.Tracer.Start(ctx, name, opts...)
}

// Simulates Orchestrion and a manual InstrumentClient call each attaching
// their own interceptor instance to the same client.
func TestDoubleInstrumentation_TwoInterceptorInstances(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	var starts atomic.Int64
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(&countingTracerProvider{TracerProvider: tp, starts: &starts})
	t.Cleanup(func() { otel.SetTracerProvider(original) })

	server := setupServer(t)

	client, err := a2aclient.NewFromEndpoints(t.Context(), []a2a.AgentInterface{
		{Transport: a2a.TransportProtocolJSONRPC, URL: server.URL + "/invoke"},
	})
	require.NoError(t, err)
	defer func() { _ = client.Destroy() }()

	client.AddCallInterceptor(NewClientInterceptor()) // e.g. Orchestrion
	InstrumentClient(client)                          // e.g. a manual call on top

	_, err = client.SendMessage(context.Background(), &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "Braintrust"}),
	})
	require.NoError(t, err)

	spans := exporter.Flush()
	clientSpans := 0
	for _, span := range spans {
		if span.Name() == "a2a.SendMessage" && span.Metadata()["role"] == "client" {
			clientSpans++
		}
	}
	assert.Equal(t, 1, clientSpans, "two independently attached interceptors must still produce exactly one exported span")

	// 1 client span + 1 server span; a redundant interceptor must not call Start.
	assert.Equal(t, int64(2), starts.Load(),
		"exactly one client-side span and one server-side span must be started - a second client interceptor must not call Start at all, not just fail to export")
}

func TestInstrumentClient_StreamAbandonedOnContextCancel(t *testing.T) {
	exporter := setupOtel(t)
	server := setupServerWithExecutor(t, multiEventExecutor{})
	client := setupClient(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotFirstEvent bool
	for event, err := range client.SendStreamingMessage(ctx, &a2a.MessageSendParams{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.TextPart{Text: "go"}),
	}) {
		require.NoError(t, err)
		require.IsType(t, &a2a.TaskArtifactUpdateEvent{}, event, "must abandon before a terminal event to prove the fallback, not the normal path")
		gotFirstEvent = true
		cancel()
		break
	}
	require.True(t, gotFirstEvent)

	require.Eventually(t, func() bool {
		for _, span := range exporter.InMemoryExporter().GetSpans() {
			if span.Name == "a2a.SendStreamingMessage" {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond, "client span must eventually end once the call context is canceled")

	clientSpan := findSpanWithRole(t, exporter.Flush(), "a2a.SendStreamingMessage", "client")
	assert.Equal(t, codes.Error, clientSpan.Status().Code)
	assert.Contains(t, clientSpan.Status().Description, "abandoned")
}

func TestCallMetaCarrier_SetReplacesExistingValue(t *testing.T) {
	meta := a2aclient.CallMeta{
		"TraceParent": {"stale-parent"},
		"traceparent": {"another-stale-parent"},
		"other":       {"preserved"},
	}

	callMetaCarrier(meta).Set("traceparent", "current-parent")

	require.Equal(t, []string{"current-parent"}, meta.Get("traceparent"))
	require.Equal(t, []string{"preserved"}, meta.Get("other"))
	_, hasMixedCaseKey := meta["TraceParent"]
	assert.False(t, hasMixedCaseKey)
}

func TestAggregateOutput_LoneStatusUpdateBecomesTask(t *testing.T) {
	status := &a2a.TaskStatusUpdateEvent{
		TaskID:    "task-1",
		ContextID: "context-1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Final:     true,
	}

	output := aggregateOutput([]any{status})

	task, ok := output.(*a2a.Task)
	require.True(t, ok, "lone status update must use the normalized Task output shape")
	require.Equal(t, status.TaskID, task.ID)
	require.Equal(t, status.ContextID, task.ContextID)
	require.Equal(t, status.Status, task.Status)
}

func TestIsTerminalEvent_TaskState(t *testing.T) {
	tests := []struct {
		state    a2a.TaskState
		terminal bool
	}{
		{state: a2a.TaskStateSubmitted},
		{state: a2a.TaskStateWorking},
		{state: a2a.TaskStateInputRequired, terminal: true},
		{state: a2a.TaskStateCompleted, terminal: true},
		{state: a2a.TaskStateCanceled, terminal: true},
		{state: a2a.TaskStateFailed, terminal: true},
		{state: a2a.TaskStateRejected, terminal: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			assert.Equal(t, tt.terminal, isTerminalEvent(&a2a.Task{Status: a2a.TaskStatus{State: tt.state}}))
		})
	}
}

// Regression test for a data race: context.AfterFunc calls its callback
// immediately, in its own goroutine, if ctx is already done when
// registered. Only fails under -race.
func TestStartCall_AlreadyDoneContext_NoRace(t *testing.T) {
	setupOtel(t)

	ct := &clientTracer{tr: tracer()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done before Before is ever called

	for range 200 {
		_, err := ct.Before(ctx, &a2aclient.Request{Method: "SendMessage"})
		require.NoError(t, err)
	}
}

func TestShouldInstrumentCall(t *testing.T) {
	blocking := true
	nonBlocking := false
	tests := []struct {
		name    string
		method  string
		payload any
		want    bool
	}{
		{name: "blocking send", method: "SendMessage", payload: &a2a.MessageSendParams{Config: &a2a.MessageSendConfig{Blocking: &blocking}}, want: true},
		{name: "default send", method: "SendMessage", payload: &a2a.MessageSendParams{}, want: true},
		{name: "non-blocking send", method: "SendMessage", payload: &a2a.MessageSendParams{Config: &a2a.MessageSendConfig{Blocking: &nonBlocking}}},
		{name: "server non-blocking send", method: "OnSendMessage", payload: &a2a.MessageSendParams{Config: &a2a.MessageSendConfig{Blocking: &nonBlocking}}},
		{name: "stream", method: "SendStreamingMessage", want: true},
		{name: "server stream", method: "OnSendMessageStream", want: true},
		{name: "get task", method: "GetTask"},
		{name: "cancel task", method: "CancelTask"},
		{name: "resubscribe", method: "ResubscribeToTask"},
		{name: "push callback config", method: "SetTaskPushConfig"},
		{name: "agent card", method: "GetAgentCard"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldInstrumentCall(tt.method, tt.payload))
		})
	}
}

func TestStartCall_SetsTaskAttributes(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := tp.Tracer("test")

	ctx := startCall(context.Background(), tracer, "task-method", "SendMessage", "client", trace.SpanKindClient, nil, new(int), new(int))
	state := ctx.Value(spanContextKey{}).(*callState)
	state.end(func() { setOutput(state.span, []any{"result"}) })

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	spans[0].AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{
		"name": "task-method",
		"type": "task",
	})
}

func findSpanWithRole(t *testing.T, spans []oteltest.Span, name, role string) oteltest.Span {
	t.Helper()
	for _, span := range spans {
		if span.Name() != name {
			continue
		}
		if role == "" || span.Metadata()["role"] == role {
			return span
		}
	}
	t.Fatalf("no span named %q with role %q found among %d spans", name, role, len(spans))
	return oteltest.Span{}
}
