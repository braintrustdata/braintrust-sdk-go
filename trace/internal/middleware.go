// Package internal provides shared middleware functionality for OpenTelemetry tracers.
package internal

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// MiddlewareTracer adds tracing to API requests by parsing bodies of the request and response.
type MiddlewareTracer interface {
	StartSpan(ctx context.Context, start time.Time, request io.Reader) (context.Context, trace.Span, error)
	TagSpan(span trace.Span, response io.Reader) error
}

// NextMiddleware represents the next middleware to run in the client middleware chain.
type NextMiddleware = func(req *http.Request) (*http.Response, error)

// TracerRouter maps URL paths to specific tracers for different endpoints.
type TracerRouter func(path string) MiddlewareTracer

// Middleware creates a shared OpenTelemetry middleware that uses the provided router
// to determine which tracer to use for each endpoint. An optional logger can be provided
// for debug and error logging. If nil, logging is disabled.
func Middleware(getMiddlewareTracer TracerRouter, log logger.Logger) func(*http.Request, NextMiddleware) (*http.Response, error) {
	// Use discard logger if none provided
	if log == nil {
		log = logger.Discard()
	}

	return func(req *http.Request, next NextMiddleware) (*http.Response, error) {
		start := time.Now()

		// Determine which tracer to use first
		var mt MiddlewareTracer
		if req.URL != nil {
			mt = getMiddlewareTracer(req.URL.Path)
		}

		// Right now we don't bother tracing requests with a nil body, because they have no data.
		// If needed, we could change that but handle the nil body sanely.
		if mt == nil || req.Body == nil {
			// Some endpoints aren't traced. Just pass them along.
			return next(req)
		}

		// Supported endpoint, let's set up tracing.
		var buf bytes.Buffer
		reqBody := req.Body
		defer func() {
			_ = reqBody.Close() // Ignore error
		}()

		// Use TeeReader - as the tracer reads from tee, it will populate buf
		tee := io.TeeReader(reqBody, &buf)

		ctx, span, err := mt.StartSpan(req.Context(), start, tee)
		if err != nil {
			// Ignore span creation errors - we'll just not trace this request
			log.Warn("Error starting span", "error", err)
		}

		// After tracer has read from tee, set request body to read from buffer
		req.Body = io.NopCloser(&buf)
		req = req.WithContext(ctx)

		// Continue processing the request.
		resp, err := next(req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return resp, err
		}

		// Intercept the response body, so we can gather tracing data.
		//
		// It's critical that we don't try to parse the whole response body here because
		// we don't want to block clients waiting for streaming responses.
		onResponseDone := func(r io.Reader) {
			// NOTE: this could be done in a goroutine so we don't add any extra
			// latency to the response.
			now := time.Now()
			if err := mt.TagSpan(span, r); err != nil {
				log.Warn("Error tagging span", "error", err)
			}
			span.End(trace.WithTimestamp(now))
		}
		body := NewBufferedReader(resp.Body, onResponseDone)
		resp.Body = body
		return resp, nil
	}
}
