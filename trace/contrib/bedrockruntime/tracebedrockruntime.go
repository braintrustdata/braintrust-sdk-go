// Package bedrockruntime provides OpenTelemetry instrumentation for AWS Bedrock
// Runtime API calls.
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
// Then register the middleware on your Bedrock client:
//
//	cfg, err := config.LoadDefaultConfig(ctx)
//	if err != nil {
//		log.Fatal(err)
//	}
//	client := bedrockruntime.NewFromConfig(cfg, tracebedrockruntime.NewMiddleware())
//
//	// All Converse / ConverseStream / InvokeModel calls are now traced.
//	out, err := client.Converse(ctx, &bedrockruntime.ConverseInput{
//		ModelId: aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
//		Messages: []types.Message{ ... },
//	})
//
// Coverage notes:
//   - Converse and ConverseStream are fully instrumented (input/output/metrics).
//   - InvokeModel and InvokeModelWithResponseStream normalize Anthropic Claude
//     messages, streamed output, and token metrics. Other model-specific body
//     formats are not captured because their schemas are provider-defined.
//   - CountTokens records the prompt_tokens estimate and, for Claude InvokeModel-
//     or Converse-shaped requests, the normalized input. There is no generation,
//     so no output or completion metrics are recorded.
//   - InvokeModelWithBidirectionalStream is not instrumented.
package bedrockruntime

import (
	"context"
	"time"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/smithy-go/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// middlewareConfig holds shared configuration for the middleware.
type middlewareConfig struct {
	tracerProvider trace.TracerProvider
	logger         logger.Logger
}

// MiddlewareOption configures the middleware.
type MiddlewareOption func(*middlewareConfig)

// WithTracerProvider sets a custom TracerProvider for the middleware.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.tracerProvider = tp
	}
}

// WithLogger sets a custom logger for the middleware.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) MiddlewareOption {
	return func(c *middlewareConfig) {
		c.logger = log
	}
}

func (c *middlewareConfig) tracer() trace.Tracer {
	tp := c.tracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return tp.Tracer("braintrust")
}

// setJSONAttr writes a JSON-encoded span attribute. Marshal errors are
// vanishingly rare for the map/slice/primitive inputs we pass, but when one
// does happen it's logged at Debug via the configured logger so the miss
// isn't completely silent. No error is returned — call sites don't have a
// useful recovery path.
func setJSONAttr(log logger.Logger, span trace.Span, key string, value any) {
	if err := internal.SetJSONAttr(span, key, value); err != nil && log != nil {
		log.Debug("braintrust bedrockruntime: failed to set attribute", "key", key, "error", err)
	}
}

// NewMiddleware returns an optFn for bedrockruntime.NewFromConfig that registers
// Braintrust tracing into the AWS SDK's Smithy middleware stack.
//
// Example:
//
//	client := bedrockruntime.NewFromConfig(cfg, tracebedrockruntime.NewMiddleware())
func NewMiddleware(opts ...MiddlewareOption) func(*bedrockruntime.Options) {
	cfg := &middlewareConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(o *bedrockruntime.Options) {
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			initMW := middleware.InitializeMiddlewareFunc(
				"BraintrustInitialize",
				initializeHandler(cfg),
			)
			if err := stack.Initialize.Add(initMW, middleware.After); err != nil {
				return err
			}
			// Deserialize is registered Before (outermost) so the typed
			// out.Result is populated by the inner SDK operation deserializer
			// before we inspect it.
			deserMW := middleware.DeserializeMiddlewareFunc(
				"BraintrustDeserialize",
				deserializeHandler(cfg),
			)
			return stack.Deserialize.Add(deserMW, middleware.Before)
		})
	}
}

// opTracer is the per-operation tracer interface.
//
// StartSpan creates a span, sets input attributes from the typed params, and
// returns the updated context + span.
//
// TagOutput finishes the span: for non-streaming operations it sets output
// attributes and calls span.End() inline; for streaming operations it may
// wrap the event stream reader inside the typed output so span.End() fires on
// stream close or drain.
type opTracer interface {
	StartSpan(ctx context.Context, start time.Time, in any) (context.Context, trace.Span)
	TagOutput(span trace.Span, out any, start time.Time)
}

// spanStateKey is the context key for stashed span state.
type spanStateKey struct{}

type spanState struct {
	span    trace.Span
	tracer  opTracer
	start   time.Time
	started bool
	// ended is set once the span has been closed so Initialize's error
	// unwinding doesn't double-end it when the failure originated downstream.
	ended bool
}

func contextWithSpanState(ctx context.Context, s *spanState) context.Context {
	return context.WithValue(ctx, spanStateKey{}, s)
}

func spanStateFromContext(ctx context.Context) *spanState {
	v, _ := ctx.Value(spanStateKey{}).(*spanState)
	return v
}

// pickTracer returns an opTracer for the given operation name. Returns nil for
// operations we don't instrument (bidirectional streams, management APIs).
func pickTracer(cfg *middlewareConfig, opName string) opTracer {
	switch opName {
	case "Converse":
		return &converseTracer{cfg: cfg}
	case "ConverseStream":
		return &converseStreamTracer{cfg: cfg}
	case "InvokeModel":
		return &invokeModelTracer{cfg: cfg}
	case "InvokeModelWithResponseStream":
		return &invokeModelStreamTracer{cfg: cfg}
	case "CountTokens":
		return &countTokensTracer{cfg: cfg}
	}
	return nil
}

func initializeHandler(cfg *middlewareConfig) func(context.Context, middleware.InitializeInput, middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
	return func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
		opName := awsmiddleware.GetOperationName(ctx)
		tracer := pickTracer(cfg, opName)
		if tracer == nil {
			return next.HandleInitialize(ctx, in)
		}

		start := time.Now()
		ctx, span := tracer.StartSpan(ctx, start, in.Parameters)
		ctx = contextWithSpanState(ctx, &spanState{
			span:    span,
			tracer:  tracer,
			start:   start,
			started: true,
		})
		state := spanStateFromContext(ctx)
		out, metadata, err := next.HandleInitialize(ctx, in)
		// If the rest of the initialize chain fails before Deserialize can tag
		// and end our span, close it here so we don't leak an open span.
		// Deserialize already handles its own errors and marks state.ended; the
		// check guards against double-recording when the error originated there.
		if err != nil && state != nil && !state.ended {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			state.ended = true
		}
		return out, metadata, err
	}
}

func deserializeHandler(cfg *middlewareConfig) func(context.Context, middleware.DeserializeInput, middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
	return func(ctx context.Context, in middleware.DeserializeInput, next middleware.DeserializeHandler) (middleware.DeserializeOutput, middleware.Metadata, error) {
		out, metadata, err := next.HandleDeserialize(ctx, in)

		st := spanStateFromContext(ctx)
		if st == nil || !st.started {
			return out, metadata, err
		}

		if err != nil {
			st.span.RecordError(err)
			st.span.SetStatus(codes.Error, err.Error())
			st.span.End()
			st.ended = true
			return out, metadata, err
		}

		// TagOutput handles both non-streaming (ends span inline) and streaming
		// (wraps stream reader so span.End() fires on drain).
		st.tracer.TagOutput(st.span, out.Result, st.start)
		// Non-streaming tracers end the span inside TagOutput; streaming ones
		// end it asynchronously when the stream drains. Either way, Initialize
		// shouldn't try to double-end on its way back up.
		st.ended = true
		return out, metadata, err
	}
}
