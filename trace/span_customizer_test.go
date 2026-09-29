package trace

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/attachmentprocessor"
)

// These tests exercise local exporter policy, not the Braintrust API. OTel's
// in-memory exporter lets us assert exactly what leaves the customization boundary.
func customizerTestSpan(name string) sdktrace.ReadOnlySpan {
	return tracetest.SpanStub{
		Name: name,
		SpanContext: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: oteltrace.TraceID{1}, SpanID: oteltrace.SpanID{2},
		}),
		Parent: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: oteltrace.TraceID{1}, SpanID: oteltrace.SpanID{3}, Remote: true,
		}),
		Attributes: []attribute.KeyValue{attribute.String("braintrust.input_json", `"secret"`)},
		Events:     []sdktrace.Event{{Name: "event", Attributes: []attribute.KeyValue{attribute.String("secret", "event")}}},
		Links:      []sdktrace.Link{{Attributes: []attribute.KeyValue{attribute.String("secret", "link")}}},
	}.Snapshot()
}

type customizerAttributes struct {
	sdktrace.ReadOnlySpan
	attrs []attribute.KeyValue
}

func (s customizerAttributes) Attributes() []attribute.KeyValue { return s.attrs }

func TestSpanCustomizersOrderedReplacementAndIsolation(t *testing.T) {
	original := customizerTestSpan("ordered")
	sink := tracetest.NewInMemoryExporter()
	customizers := []config.SpanCustomizer{
		{}, // Optional hooks do not interrupt the chain.
		{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			span.Attributes()[0] = attribute.String("braintrust.input_json", `"redacted"`)
			span.Events()[0].Attributes[0] = attribute.String("secret", "redacted")
			span.Links()[0].Attributes[0] = attribute.String("secret", "redacted")
			return span, nil
		}},
		{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			require.Equal(t, `"redacted"`, span.Attributes()[0].Value.AsString())
			// Replacement removes input entirely and changes the destination.
			return customizerAttributes{span, []attribute.KeyValue{
				attribute.String(ParentOtelAttrKey, "project_name:redacted"),
				attribute.Bool("customized", true),
			}}, nil
		}},
	}
	exporter := newCustomizingExporter(sink, customizers, logger.Discard())
	customizers[1].OnSpanExport = func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
		return nil, errors.New("configuration changed after setup")
	}
	batch := []sdktrace.ReadOnlySpan{original}
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))
	exported := sink.GetSpans()
	require.Len(t, exported, 1)
	assert.Equal(t, []attribute.KeyValue{
		attribute.String(ParentOtelAttrKey, "project_name:redacted"),
		attribute.Bool("customized", true),
	}, exported[0].Attributes)
	assert.Equal(t, original, batch[0])
	assert.Equal(t, `"secret"`, original.Attributes()[0].Value.AsString())
	assert.Equal(t, "event", original.Events()[0].Attributes[0].Value.AsString())
	assert.Equal(t, "link", original.Links()[0].Attributes[0].Value.AsString())
}

// recordingLogger captures diagnostics to assert they never include hook error text.
type recordingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recordingLogger) record(msg string, args []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprint(append([]any{msg}, args...)...))
}

func (l *recordingLogger) Debug(msg string, args ...any) { l.record(msg, args) }
func (l *recordingLogger) Info(msg string, args ...any)  { l.record(msg, args) }
func (l *recordingLogger) Warn(msg string, args ...any)  { l.record(msg, args) }
func (l *recordingLogger) Error(msg string, args ...any) { l.record(msg, args) }

func (l *recordingLogger) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestSpanCustomizersFailEntireBatch(t *testing.T) {
	failure := errors.New("redaction failed: secret payload")
	for _, tc := range []struct {
		name string
		hook func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error)
	}{
		{"error", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { return nil, failure }},
		{"nil", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { return nil, nil }},
		{"typed nil", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			var span *customizerAttributes
			return span, nil
		}},
		{"panic", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { panic("secret payload") }},
		{"nil panic", func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) { panic(nil) }},
		{"identity", func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			stub := tracetest.SpanStubFromReadOnlySpan(span)
			stub.SpanContext = stub.SpanContext.WithSpanID(oteltrace.SpanID{9})
			return stub.Snapshot(), nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := tracetest.NewInMemoryExporter()
			log := &recordingLogger{}
			laterCalls := 0
			exporter := newCustomizingExporter(sink, []config.SpanCustomizer{
				{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					span.Attributes()[0] = attribute.String("braintrust.input_json", `"redacted"`)
					if span.Name() == "second" {
						return tc.hook(span)
					}
					return span, nil
				}},
				{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					laterCalls++
					return span, nil
				}},
			}, log)
			batch := []sdktrace.ReadOnlySpan{customizerTestSpan("first"), customizerTestSpan("second")}
			err := exporter.ExportSpans(context.Background(), batch)
			require.Error(t, err)
			// Diagnostics identify the hook and failure kind, never hook-provided text.
			assert.Contains(t, err.Error(), "span customizer 0 ")
			assert.NotErrorIs(t, err, failure)
			assert.NotContains(t, err.Error(), "secret")
			assert.Contains(t, log.String(), "span customization failed")
			assert.NotContains(t, log.String(), "secret")
			assert.Empty(t, sink.GetSpans())
			assert.Equal(t, 1, laterCalls)
			for _, span := range batch {
				assert.Equal(t, `"secret"`, span.Attributes()[0].Value.AsString())
			}
		})
	}
}

func TestSpanCustomizersProtectIdentityAfterEachHook(t *testing.T) {
	for _, tc := range []struct {
		name   string
		root   bool
		change func(*tracetest.SpanStub)
	}{
		{name: "trace", change: func(s *tracetest.SpanStub) { s.SpanContext = s.SpanContext.WithTraceID(oteltrace.TraceID{9}) }},
		{name: "span", change: func(s *tracetest.SpanStub) { s.SpanContext = s.SpanContext.WithSpanID(oteltrace.SpanID{9}) }},
		{name: "parent trace", change: func(s *tracetest.SpanStub) { s.Parent = s.Parent.WithTraceID(oteltrace.TraceID{9}) }},
		{name: "parent span", change: func(s *tracetest.SpanStub) { s.Parent = s.Parent.WithSpanID(oteltrace.SpanID{9}) }},
		{name: "remove parent", change: func(s *tracetest.SpanStub) { s.Parent = oteltrace.SpanContext{} }},
		{name: "add root parent", root: true, change: func(s *tracetest.SpanStub) { s.Parent = s.SpanContext }},
		{name: "invalid parent trace", root: true, change: func(s *tracetest.SpanStub) { s.Parent = s.Parent.WithTraceID(oteltrace.TraceID{9}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := tracetest.SpanStubFromReadOnlySpan(customizerTestSpan("identity"))
			if tc.root {
				stub.Parent = oteltrace.SpanContext{}
			}
			original := stub.Snapshot()
			sink := tracetest.NewInMemoryExporter()
			laterCalled := false
			exporter := newCustomizingExporter(sink, []config.SpanCustomizer{
				{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					changed := tracetest.SpanStubFromReadOnlySpan(span)
					tc.change(&changed)
					return changed.Snapshot(), nil
				}},
				{OnSpanExport: func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					laterCalled = true
					return original, nil // Restoring IDs later must not bypass validation.
				}},
			}, logger.Discard())
			require.Error(t, exporter.ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{original}))
			assert.False(t, laterCalled)
			assert.Empty(t, sink.GetSpans())
		})
	}
}

func TestSpanCustomizersAllowContextMetadataAndRerunOnResubmission(t *testing.T) {
	sink := tracetest.NewInMemoryExporter()
	calls := 0
	exporter := newCustomizingExporter(sink, []config.SpanCustomizer{{
		OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			calls++
			stub := tracetest.SpanStubFromReadOnlySpan(span)
			stub.Parent = stub.Parent.WithRemote(false)
			stub.SpanContext = stub.SpanContext.WithTraceFlags(oteltrace.FlagsSampled)
			return stub.Snapshot(), nil
		},
	}}, logger.Discard())
	batch := []sdktrace.ReadOnlySpan{customizerTestSpan("retry")}
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))
	require.NoError(t, exporter.ExportSpans(context.Background(), batch))
	assert.Equal(t, 2, calls)
	require.Len(t, sink.GetSpans(), 2)
	assert.False(t, sink.GetSpans()[0].Parent.IsRemote())
	assert.True(t, sink.GetSpans()[0].SpanContext.IsSampled())
}

func TestSpanCustomizersProcessorPreservesOtherConsumers(t *testing.T) {
	ctx := context.Background()
	sink := tracetest.NewInMemoryExporter()
	originals := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(ctx)) })
	require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{
		Exporter: sink, Logger: logger.Discard(),
		SpanCustomizers: []config.SpanCustomizer{{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			return customizerAttributes{span, []attribute.KeyValue{attribute.String("exported", "redacted")}}, nil
		}}},
	}))
	tp.RegisterSpanProcessor(originals)
	_, span := tp.Tracer("application").Start(ctx, "manual")
	span.SetAttributes(attribute.String("application", "original"))
	span.End()
	require.NoError(t, tp.ForceFlush(ctx))
	require.Len(t, sink.GetSpans(), 1)
	assert.Equal(t, []attribute.KeyValue{attribute.String("exported", "redacted")}, sink.GetSpans()[0].Attributes)
	require.Len(t, originals.Ended(), 1)
	assert.Contains(t, originals.Ended()[0].Attributes(), attribute.String("application", "original"))
}

// These tests observe upload enqueueing locally: no server-side cassette can
// prove that a redacted attachment was never submitted to the uploader.
type customizerAttachmentUploader struct {
	attachmentprocessor.NoopUploader
	data [][]byte
}

func (u *customizerAttachmentUploader) Enqueue(_ attachmentprocessor.Reference, data []byte) bool {
	u.data = append(u.data, data)
	return true
}

func TestSpanCustomizersRunBeforeAttachments(t *testing.T) {
	ctx := context.Background()
	privateData := "private attachment content"
	publicData := "public attachment content"
	attachmentJSON := func(data string) string {
		return `{"type":"base64_attachment","content":"data:image/png;base64,` +
			base64.StdEncoding.EncodeToString([]byte(data)) + `"}`
	}
	for _, mode := range []string{"redact", "replace", "reject-batch"} {
		t.Run(mode, func(t *testing.T) {
			uploader := &customizerAttachmentUploader{}
			sink := tracetest.NewInMemoryExporter()
			tp := sdktrace.NewTracerProvider()
			t.Cleanup(func() { require.NoError(t, tp.Shutdown(ctx)) })
			var seen []string
			require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{
				Exporter: sink, Logger: logger.Discard(),
				AutoConvertAIAttachments: true,
				AttachmentUploader:       uploader,
				SpanCustomizers: []config.SpanCustomizer{{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					seen = append(seen, span.Name())
					assert.Empty(t, uploader.data, "no uploads may begin before the entire batch is customized")
					for _, attr := range span.Attributes() {
						if attr.Key == inputJSONAttrKey || attr.Key == outputJSONAttrKey {
							assert.Equal(t, attachmentJSON(privateData), attr.Value.AsString())
						}
					}
					if mode == "reject-batch" {
						if span.Name() == "second" {
							return nil, errors.New("cannot redact attachment")
						}
						return span, nil
					}
					attrs := make([]attribute.KeyValue, 0, len(span.Attributes()))
					for _, attr := range span.Attributes() {
						if attr.Key != inputJSONAttrKey && attr.Key != outputJSONAttrKey {
							attrs = append(attrs, attr)
						}
					}
					if mode == "replace" {
						attrs = append(attrs, attribute.String(string(outputJSONAttrKey), attachmentJSON(publicData)))
					}
					return customizerAttributes{span, attrs}, nil
				}}},
			}))
			for _, name := range []string{"first", "second"} {
				_, span := tp.Tracer("customizer-attachments").Start(ctx, name)
				span.SetAttributes(
					attribute.String(string(inputJSONAttrKey), attachmentJSON(privateData)),
					attribute.String(string(outputJSONAttrKey), attachmentJSON(privateData)),
				)
				span.End()
			}
			err := tp.ForceFlush(ctx)
			assert.Equal(t, []string{"first", "second"}, seen)
			if mode == "reject-batch" {
				require.Error(t, err)
				assert.Empty(t, uploader.data)
				assert.Empty(t, sink.GetSpans())
				return
			}
			require.NoError(t, err)
			spans := sink.GetSpans()
			require.Len(t, spans, 2)
			if mode == "redact" {
				assert.Empty(t, uploader.data)
			} else {
				assert.Equal(t, [][]byte{[]byte(publicData), []byte(publicData)}, uploader.data)
			}
			for _, span := range spans {
				var output string
				for _, attr := range span.Attributes {
					assert.NotEqual(t, inputJSONAttrKey, attr.Key)
					if attr.Key == outputJSONAttrKey {
						output = attr.Value.AsString()
					}
				}
				if mode == "redact" {
					assert.Empty(t, output)
				} else {
					assert.Contains(t, output, "braintrust_attachment")
					assert.NotContains(t, output, base64.StdEncoding.EncodeToString([]byte(publicData)))
				}
			}
		})
	}
}

// A local server can hold the worker while a real bounded queue fills; VCR
// cannot reproduce that scheduling constraint.
func newBlockedAttachmentUploader(t *testing.T, queueSize int) (*attachmentprocessor.S3Uploader, <-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/attachment":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"signedUrl":"http://` + r.Host + `/upload","headers":{}}`))
		case "/upload":
			if uploads.Add(1) == 1 {
				close(started)
				<-release
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	uploader := attachmentprocessor.NewS3Uploader(attachmentprocessor.UploaderConfig{
		APIURL: server.URL, APIKey: "key", OrgID: "org", HTTPClient: server.Client(), QueueSize: queueSize,
	})
	t.Cleanup(func() {
		unblock()
		uploader.Shutdown()
		server.Close()
	})
	return uploader, started, unblock, &uploads
}

func base64AttachmentJSON(data string) string {
	return `{"type":"base64_attachment","content":"data:image/png;base64,` +
		base64.StdEncoding.EncodeToString([]byte(data)) + `"}`
}

func TestAttachmentExportBackpressure(t *testing.T) {
	for _, mode := range []string{"hooks", "no-hooks", "cancel", "cancel-flush"} {
		t.Run(mode, func(t *testing.T) {
			uploader, started, unblock, uploads := newBlockedAttachmentUploader(t, 4)
			sink := tracetest.NewInMemoryExporter()
			attachments := &attachmentExporter{
				SpanExporter: sink,
				processor:    attachmentprocessor.NewProcessor(uploader, logger.Discard()),
			}
			var hooks []config.SpanCustomizer
			if mode == "hooks" {
				hooks = []config.SpanCustomizer{{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					assert.Zero(t, uploads.Load(), "the entire batch must be customized before uploads")
					return span, nil
				}}}
			}
			exporter := newCustomizingExporter(attachments, hooks, logger.Discard())
			processor := &spanProcessor{
				wrapped:            sdktrace.NewBatchSpanProcessor(exporter, sdktrace.WithBatchTimeout(time.Hour)),
				attachmentUploader: uploader,
				logger:             logger.Discard(),
			}
			t.Cleanup(func() {
				unblock()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				assert.NoError(t, processor.Shutdown(ctx))
			})
			encoded := base64.StdEncoding.EncodeToString([]byte("attachment payload"))
			item := `{"type":"base64_attachment","content":"data:image/png;base64,` + encoded + `"}`
			input := `[` + item + `,` + item + `,` + item + `]`
			batch := []sdktrace.ReadOnlySpan{
				customizerAttributes{customizerTestSpan("first"), []attribute.KeyValue{
					attribute.String(string(inputJSONAttrKey), input),
					attribute.String(string(outputJSONAttrKey), item),
				}},
				customizerAttributes{customizerTestSpan("second"), []attribute.KeyValue{
					attribute.String(string(inputJSONAttrKey), input),
					attribute.String(string(outputJSONAttrKey), item),
				}},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := make(chan error, 1)
			if mode == "cancel" {
				go func() { result <- exporter.ExportSpans(ctx, batch) }()
			} else {
				for _, span := range batch {
					sampled := tracetest.SpanStubFromReadOnlySpan(span)
					sampled.SpanContext = sampled.SpanContext.WithTraceFlags(oteltrace.FlagsSampled)
					processor.wrapped.OnEnd(sampled.Snapshot())
				}
				go func() { result <- processor.ForceFlush(ctx) }()
			}
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("first upload did not start")
			}
			if mode == "cancel" || mode == "cancel-flush" {
				cancel()
			} else {
				select {
				case err := <-result:
					t.Fatalf("export finished while uploads were blocked: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
				assert.Empty(t, sink.GetSpans(), "export must wait for capacity instead of sending inline data")
				unblock()
			}
			select {
			case err := <-result:
				if mode == "cancel" || mode == "cancel-flush" {
					require.ErrorIs(t, err, context.Canceled)
					assert.Empty(t, sink.GetSpans(), "a canceled conversion must not export inline fallbacks")
					return
				}
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("export did not stop")
			}
			assert.Equal(t, int32(8), uploads.Load(), "ForceFlush must also drain newly enqueued uploads")
			exported := sink.GetSpans()
			require.Len(t, exported, 2)
			for _, span := range exported {
				for _, attr := range span.Attributes {
					assert.Contains(t, attr.Value.AsString(), "braintrust_attachment")
					assert.NotContains(t, attr.Value.AsString(), encoded, "queue saturation must not retain inline data")
				}
			}
			for _, span := range batch {
				assert.Equal(t, input, span.Attributes()[0].Value.AsString(), "the original batch stays unchanged")
			}
		})
	}
}

func TestAttachmentExportTimeoutFallsBackInline(t *testing.T) {
	// Queue of 2 with one upload in flight and one queued leaves one free slot.
	uploader, started, unblock, uploads := newBlockedAttachmentUploader(t, 2)
	require.True(t, uploader.Enqueue(attachmentprocessor.NewReference("image/png"), []byte("in flight")))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first upload did not start")
	}
	require.True(t, uploader.Enqueue(attachmentprocessor.NewReference("image/png"), []byte("queued")))

	bg := context.Background()
	sink := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		unblock()
		require.NoError(t, tp.Shutdown(bg))
	})
	require.NoError(t, AddSpanProcessor(tp, newTestSession(), Config{
		Exporter: sink, Logger: logger.Discard(),
		AutoConvertAIAttachments: true,
		AttachmentUploader:       uploader,
		SpanCustomizers: []config.SpanCustomizer{{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			return customizerAttributes{span, append(slices.Clone(span.Attributes()), attribute.Bool("customized", true))}, nil
		}}},
	}))
	// Two attachments across input and output need two slots, so neither may
	// be enqueued on its own.
	_, span := tp.Tracer("attachment-timeout").Start(bg, "with-attachments")
	span.SetAttributes(
		attribute.String(string(inputJSONAttrKey), base64AttachmentJSON("input")),
		attribute.String(string(outputJSONAttrKey), base64AttachmentJSON("output")),
	)
	span.End()
	_, span = tp.Tracer("attachment-timeout").Start(bg, "plain")
	span.SetAttributes(attribute.String(string(inputJSONAttrKey), `"text"`))
	span.End()

	ctx, cancel := context.WithTimeout(bg, 300*time.Millisecond)
	defer cancel()
	require.NoError(t, tp.ForceFlush(ctx), "an expired upload wait must not fail the export")

	exported := sink.GetSpans()
	require.Len(t, exported, 2, "upload backpressure must not drop spans")
	for _, span := range exported {
		assert.Contains(t, span.Attributes, attribute.Bool("customized", true))
		if span.Name != "with-attachments" {
			continue
		}
		assert.Contains(t, span.Attributes, attribute.String(string(inputJSONAttrKey), base64AttachmentJSON("input")))
		assert.Contains(t, span.Attributes, attribute.String(string(outputJSONAttrKey), base64AttachmentJSON("output")))
	}
	unblock()
	require.True(t, uploader.ForceFlush(5*time.Second))
	assert.Equal(t, int32(2), uploads.Load(), "an inline fallback must not leave orphan uploads")
}
