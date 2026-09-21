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
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
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
	// the actual serialization and prove a rejected batch sends zero requests.
	for _, lazy := range []bool{false, true} {
		t.Run(fmt.Sprintf("lazy=%t", lazy), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var requests atomic.Int32
			payloads := make(chan *collectortrace.ExportTraceServiceRequest, 1)
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/apikey/login" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(w, `{"org_info":[{"id":"test-org-id","name":"test-org","api_url":%q,"proxy_url":%q}]}`, server.URL, server.URL)
					return
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
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			tp := sdktrace.NewTracerProvider()
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
				WithLogger(logger.Discard()), WithAutoConvertAIAttachments(false),
				WithSpanCustomizers(config.SpanCustomizer{OnSpanExport: func(span sdktrace.ReadOnlySpan) (sdktrace.ReadOnlySpan, error) {
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
			_, span := tracer.Start(ctx, "export")
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
			_, second := tracer.Start(ctx, "reject")
			second.End()
			require.Error(t, tp.ForceFlush(ctx))
			assert.Equal(t, int32(1), requests.Load(), "failed batch must not reach OTLP transport")
		})
	}
}
