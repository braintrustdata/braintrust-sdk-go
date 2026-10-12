package braintrust

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestSpanCustomizersOptionSnapshotsAndAppends(t *testing.T) {
	ctx := context.Background()
	sink := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(ctx)) })
	customizers := []config.SpanCustomizer{{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
		stub := tracetest.SpanStubFromReadOnlySpan(span)
		stub.Name += "-first"
		return stub.Snapshot(), nil
	}}}
	option := WithSpanCustomizers(customizers...)
	customizers[0].OnSpanExport = func(sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
		return nil, errors.New("caller changed registration")
	}
	client, err := New(tp, WithAPIKey(auth.TestAPIKey), WithLogger(logger.Discard()), WithExporter(sink), option,
		WithSpanCustomizers(config.SpanCustomizer{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
			stub := tracetest.SpanStubFromReadOnlySpan(span)
			stub.Name += "-second"
			return stub.Snapshot(), nil
		}}))
	require.NoError(t, err)
	_, span := client.Tracer("customizer-test").Start(ctx, "original")
	span.End()
	require.NoError(t, tp.ForceFlush(ctx))
	require.Len(t, sink.GetSpans(), 1)
	assert.Equal(t, "original-first-second", sink.GetSpans()[0].Name)
}

type customizerKeyResolver struct{ ready <-chan struct{} }

func (r customizerKeyResolver) APIKey(ctx context.Context) (string, bool) {
	select {
	case <-r.ready:
		return "resolved-key", true
	case <-ctx.Done():
		return "", false
	}
}

func TestSpanCustomizersOTLPExport(t *testing.T) {
	// The wire payload contains generated span IDs/timestamps and binary gzip
	// protobuf. A local OTLP receiver, rather than a VCR cassette, lets us inspect
	// the actual serialization, stripping, routing and transport retries.
	for _, lazy := range []bool{false, true} {
		t.Run(fmt.Sprintf("lazy=%t", lazy), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var requests, attachmentRequests, hookCalls atomic.Int32
			payloads := make(chan *collectortrace.ExportTraceServiceRequest, 3)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/apikey/login" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"org_info":[{"id":"test-org-id","name":"test-org","api_url":%q,"proxy_url":%q}]}`, server.URL, server.URL)
					return
				}
				if r.URL.Path == "/attachment" {
					attachmentRequests.Add(1)
				}
				if r.URL.Path != "/otel/v1/traces" {
					http.NotFound(w, r)
					return
				}
				requests.Add(1)
				var reader io.Reader = r.Body
				if r.Header.Get("Content-Encoding") == "gzip" {
					decoded, err := gzip.NewReader(r.Body)
					if !assert.NoError(t, err) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer func() { _ = decoded.Close() }()
					reader = decoded
				}
				body, err := io.ReadAll(reader)
				if !assert.NoError(t, err) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				message := &collectortrace.ExportTraceServiceRequest{}
				if !assert.NoError(t, proto.Unmarshal(body, message)) {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				payloads <- message
				if requests.Load() == 2 {
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					w.WriteHeader(http.StatusOK)
				}
			}))
			defer server.Close()
			tp := sdktrace.NewTracerProvider(sdktrace.WithResource(resource.NewSchemaless(attribute.String("private.resource", "secret"))))
			defer func() { require.NoError(t, tp.Shutdown(ctx)) }()
			ready := make(chan struct{})
			keyOption := WithAPIKey("immediate-key")
			if lazy {
				keyOption = func(cfg *config.Config) {
					cfg.APIKey = ""
					cfg.APIKeyResolver = customizerKeyResolver{ready: ready}
				}
			}
			client, err := New(tp, keyOption, WithAPIURL(server.URL), WithAppURL(server.URL),
				WithLogger(logger.Discard()), WithProject("original"), WithAutoConvertAIAttachments(true),
				WithSpanCustomizers(config.SpanCustomizer{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
					hookCalls.Add(1)
					if span.Name() == "reject" {
						return nil, errors.New("cannot redact")
					}
					stub := tracetest.SpanStubFromReadOnlySpan(span)
					stub.Attributes = []attribute.KeyValue{attribute.String("braintrust.parent", "project_name:redacted")}
					return stub.Snapshot(), nil
				}}))
			require.NoError(t, err)
			close(ready) // Keep APIInfo empty until the lazy exporter is installed.
			tracer := client.Tracer("customizer-transport")
			parentCtx, span := tracer.Start(ctx, "export")
			span.SetAttributes(attribute.String("braintrust.input_json", `"secret"`))
			span.End()
			require.NoError(t, tp.ForceFlush(ctx))
			select {
			case message := <-payloads:
				require.Len(t, message.ResourceSpans, 1)
				require.Len(t, message.ResourceSpans[0].ScopeSpans, 1)
				spans := message.ResourceSpans[0].ScopeSpans[0].Spans
				require.Len(t, spans, 1)
				require.Len(t, spans[0].Attributes, 1)
				assert.Equal(t, "braintrust.parent", spans[0].Attributes[0].Key)
				assert.Equal(t, "project_name:redacted", spans[0].Attributes[0].Value.GetStringValue())
			case <-ctx.Done():
				t.Fatal("timed out waiting for OTLP payload")
			}
			start := time.Unix(100, 0)
			_, second := tracer.Start(parentCtx, "reject", oteltrace.WithTimestamp(start))
			second.SetAttributes(
				attribute.String("braintrust.context_json", `{"private":"secret"}`),
				attribute.String("braintrust.input_json", `{"type":"base64_attachment","content":"data:image/png;base64,c2VjcmV0"}`),
			)
			second.RecordError(errors.New("private exception"))
			second.End(oteltrace.WithTimestamp(start.Add(time.Second)))
			_, third := tracer.Start(ctx, "unrelated")
			third.End()
			require.NoError(t, tp.ForceFlush(ctx))
			var retried *collectortrace.ExportTraceServiceRequest
			for range 2 {
				select {
				case message := <-payloads:
					if retried != nil {
						assert.True(t, proto.Equal(retried, message), "transport retry reuses the stripped payload")
					}
					retried = message
					var stripped, unrelated *tracepb.Span
					for _, group := range message.ResourceSpans {
						for _, scope := range group.ScopeSpans {
							for _, exported := range scope.Spans {
								switch exported.Name {
								case "":
									stripped = exported
									assert.Empty(t, group.Resource.Attributes)
									assert.Empty(t, group.SchemaUrl)
									assert.Empty(t, scope.GetScope().GetName())
									assert.Empty(t, scope.GetScope().GetVersion())
									assert.Empty(t, scope.GetScope().GetAttributes())
									assert.Empty(t, scope.SchemaUrl)
								case "unrelated":
									unrelated = exported
								default:
									t.Errorf("unexpected span: %q", exported.Name)
								}
							}
						}
					}
					require.NotNil(t, stripped)
					require.NotNil(t, unrelated)
					identity, parent := second.SpanContext(), span.SpanContext()
					traceID, spanID, parentID := identity.TraceID(), identity.SpanID(), parent.SpanID()
					assert.Equal(t, traceID[:], stripped.TraceId)
					assert.Equal(t, spanID[:], stripped.SpanId)
					assert.Equal(t, parentID[:], stripped.ParentSpanId)
					assert.Equal(t, uint64(start.UnixNano()), stripped.StartTimeUnixNano)
					assert.Equal(t, uint64(start.Add(time.Second).UnixNano()), stripped.EndTimeUnixNano)
					assert.Empty(t, stripped.Events)
					assert.Empty(t, stripped.Links)
					assert.Empty(t, stripped.TraceState)
					assert.Empty(t, stripped.GetStatus().GetMessage())
					assert.Equal(t, tracepb.Status_STATUS_CODE_UNSET, stripped.GetStatus().GetCode())
					require.Len(t, stripped.Attributes, 2)
					attrs := map[string]string{}
					for _, attr := range stripped.Attributes {
						attrs[attr.Key] = attr.Value.GetStringValue()
					}
					assert.Equal(t, map[string]string{
						"braintrust.parent":       "project_name:original",
						"braintrust.context_json": `{"customizer_error":true}`,
					}, attrs)
				case <-ctx.Done():
					t.Fatal("timed out waiting for stripped OTLP payload")
				}
			}
			assert.Equal(t, int32(3), requests.Load())
			assert.Zero(t, attachmentRequests.Load(), "stripped attachments must never reach the uploader")
			assert.Equal(t, int32(3), hookCalls.Load(), "transport retry must not invoke hooks again")
		})
	}
}
