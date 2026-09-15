// Package weaviate provides OpenTelemetry tracing for the Weaviate Go
// client's generative search (RAG) queries.
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
// Then create your Weaviate client with tracing:
//
//	client, err := weaviate.NewClient(weaviate.Config{
//		Host:             "localhost:8080",
//		Scheme:           "http",
//		ConnectionClient: tracewaeviate.WrapClient(nil),
//	})
//
// For tests or custom configurations, you can provide a TracerProvider:
//
//	httpClient := tracewaeviate.WrapClient(nil, tracewaeviate.WithTracerProvider(tp))
//
// Only GraphQL Get queries that use WithGenerativeSearch are traced. Plain
// vector search and CRUD calls (Data().Creator(), GraphQL().Get() without
// WithGenerativeSearch, etc.) produce no spans, since they aren't a
// generative-AI execution surface.
package weaviate

import (
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// config holds configuration for the HTTP client wrapper.
type config struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// Option configures the weaviate HTTP client wrapper.
type Option func(*config)

// WithTracerProvider sets a custom TracerProvider for the HTTP client wrapper.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(c *config) {
		c.tracerProvider = tp
	}
}

// WithLogger sets a custom logger for the HTTP client wrapper.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) Option {
	return func(c *config) {
		c.logger = log
	}
}

func (c *config) tracer() trace.Tracer {
	tp := c.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// Client returns a new http.Client configured with tracing middleware.
// This is equivalent to WrapClient(nil).
func Client(opts ...Option) *http.Client {
	return WrapClient(nil, opts...)
}

// WrapClient wraps an existing http.Client with tracing middleware, for use
// as weaviate.Config.ConnectionClient. If client is nil, a new client with
// the default transport is created.
func WrapClient(client *http.Client, opts ...Option) *http.Client {
	cfg := &config{}
	for _, opt := range opts {
		opt(cfg)
	}

	if client == nil {
		client = &http.Client{}
	}

	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	client.Transport = newRoundTripper(transport, cfg)
	return client
}

type roundTripper struct {
	base http.RoundTripper
	cfg  *config
}

func newRoundTripper(base http.RoundTripper, cfg *config) http.RoundTripper {
	return &roundTripper{base: base, cfg: cfg}
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	router := func(path string) internal.MiddlewareTracer {
		return weaviateRouter(rt.cfg, req.Method, path)
	}
	middleware := internal.Middleware(router, rt.cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by caller

	next := func(r *http.Request) (*http.Response, error) {
		return rt.base.RoundTrip(r)
	}

	return middleware(req, next)
}

// weaviateRouter only traces POST /v1/graphql requests. Whether a given
// request is actually a generative search (rather than plain vector search
// or CRUD, which are out of scope) can only be told from the request body,
// so the real decision happens in generativeSearchTracer.StartSpan.
func weaviateRouter(cfg *config, method, path string) internal.MiddlewareTracer {
	if method != http.MethodPost || path != "/v1/graphql" {
		return nil
	}
	return newGenerativeSearchTracer(cfg)
}
