// Package pinecone provides OpenTelemetry tracing for the Pinecone Inference
// API (github.com/pinecone-io/go-pinecone/v6/pinecone).
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
// Then create your Pinecone client with tracing:
//
//	pc, err := pinecone.NewClient(pinecone.NewClientParams{
//		ApiKey:     apiKey,
//		RestClient: tracepinecone.WrapClient(nil),
//	})
package pinecone

import (
	"net/http"
	"strings"

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

// Option configures the Pinecone HTTP client wrapper.
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
// as pinecone.NewClientParams.RestClient. If client is nil, a new client
// with the default transport is created.
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

// roundTripper wraps an http.RoundTripper with OpenTelemetry tracing.
type roundTripper struct {
	base http.RoundTripper
	cfg  *config
}

func newRoundTripper(base http.RoundTripper, cfg *config) http.RoundTripper {
	return &roundTripper{base: base, cfg: cfg}
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	router := func(path string) internal.MiddlewareTracer {
		return pineconeRouter(rt.cfg, path)
	}
	middleware := internal.Middleware(router, rt.cfg.logger) //nolint:bodyclose // false positive - returns middleware func, body closed by caller

	next := func(r *http.Request) (*http.Response, error) {
		return rt.base.RoundTrip(r)
	}

	return middleware(req, next)
}

// pineconeRouter maps Inference API paths to their corresponding tracers.
func pineconeRouter(cfg *config, path string) internal.MiddlewareTracer {
	if strings.HasSuffix(path, "/embed") {
		return newEmbedTracer(cfg)
	}
	if strings.HasSuffix(path, "/rerank") {
		return newRerankTracer(cfg)
	}
	return nil
}
