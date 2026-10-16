package internal

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// noopTracer is a minimal MiddlewareTracer stub for exercising Middleware()
// independent of any real provider integration. VCR doesn't apply here: this
// tests Middleware()'s own status-mapping logic, not a call to any real API,
// so there's nothing to record a cassette against.
type noopTracer struct {
	cfg *config
}

type config struct {
	tracer trace.Tracer
}

func (nt *noopTracer) StartSpan(ctx context.Context, start time.Time, _ io.Reader) (context.Context, trace.Span, error) {
	ctx, span := nt.cfg.tracer.Start(ctx, "test-span", trace.WithTimestamp(start))
	return ctx, span, nil
}

func (nt *noopTracer) TagSpan(_ trace.Span, _ io.Reader) error {
	return nil
}

func TestMiddlewareRecordsResponseReadError(t *testing.T) {
	// VCR records response bytes, not failures while reading the body. Inject
	// a read failure here to exercise the mid-stream network error path.
	tp, exporter := oteltest.Setup(t)
	tracer := &noopTracer{cfg: &config{tracer: tp.Tracer("test")}}
	mw := Middleware(func(_ string) MiddlewareTracer { return tracer }, nil) //nolint:bodyclose // returns middleware func
	req := httptest.NewRequest("POST", "/v1/whatever", strings.NewReader(`{}`))
	resp, err := mw(req, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader("partial response"), iotest.ErrReader(io.ErrUnexpectedEOF),
			)),
		}, nil
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	data, err := io.ReadAll(resp.Body)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, "partial response", string(data))

	// The span should end as soon as reading fails, even before Close.
	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Contains(t, span.Status().Description, io.ErrUnexpectedEOF.Error())
	events := span.Events()
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Attributes, attribute.String("exception.message", io.ErrUnexpectedEOF.Error()))
	require.NoError(t, resp.Body.Close())
	assert.Empty(t, exporter.Flush(), "closing a failed stream must not end the span again")
}

func TestMiddlewareMarksHTTPErrorStatusAsError(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := &noopTracer{cfg: &config{tracer: tp.Tracer("test")}}

	router := func(_ string) MiddlewareTracer { return tracer }
	mw := Middleware(router, nil) //nolint:bodyclose // false positive - returns middleware func, body closed in test below

	req := httptest.NewRequest("POST", "/v1/whatever", strings.NewReader(`{}`))
	next := func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 400,
			Status:     "400 Bad Request",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"model not found"}`)),
		}, nil
	}

	resp, err := mw(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)

	_, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.NoError(t, resp.Body.Close())

	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Stub.Status.Code)
	assert.Contains(t, span.Stub.Status.Description, "400")
}

func TestMiddlewareLeavesSuccessStatusUnset(t *testing.T) {
	tp, exporter := oteltest.Setup(t)
	tracer := &noopTracer{cfg: &config{tracer: tp.Tracer("test")}}

	router := func(_ string) MiddlewareTracer { return tracer }
	mw := Middleware(router, nil) //nolint:bodyclose // false positive - returns middleware func, body closed in test below

	req := httptest.NewRequest("POST", "/v1/whatever", strings.NewReader(`{}`))
	next := func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		}, nil
	}

	resp, err := mw(req, next)
	require.NoError(t, err)
	require.NotNil(t, resp)

	_, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.NoError(t, resp.Body.Close())

	span := exporter.FlushOne()
	assert.Equal(t, codes.Unset, span.Stub.Status.Code)
}
