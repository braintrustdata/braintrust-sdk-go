package openai

//  this file parses the responses API.

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

// responsesTracer is a tracer for the openai v1/responses POST endpoint.
// See docs here: https://platform.openai.com/docs/api-reference/responses/create
type responsesTracer struct {
	cfg       *middlewareConfig
	streaming bool
	metadata  map[string]any
}

func newResponsesTracer(cfg *middlewareConfig) *responsesTracer {
	return &responsesTracer{
		cfg:       cfg,
		streaming: false,
		metadata: map[string]any{
			"provider": "openai",
			"endpoint": "/v1/responses",
		},
	}
}

func (rt *responsesTracer) StartSpan(ctx context.Context, t time.Time, request io.Reader) (context.Context, trace.Span, error) {
	ctx, span := rt.cfg.tracer().Start(
		ctx,
		"openai.responses.create",
		trace.WithTimestamp(t),
	)

	var raw map[string]interface{}
	if err := json.NewDecoder(request).Decode(&raw); err != nil {
		return ctx, span, err
	}

	metadataFields := []string{
		"model",
		"instructions",
		"user",
		"truncation",
		"service_tier",
		"temperature",
		"top_p",
		"max_output_tokens",
		"timeout",
		"parallel_tool_calls",
		"store",
		"stream",
		"tools",
		"tool_choice",
		"seed",
		"reasoning",
	}

	// handle simple fields here.
	for _, field := range metadataFields {
		if value, exists := raw[field]; exists {
			rt.metadata[field] = value
			// keep track of streaming requests so we can parse the streaming response later.
			if field == "stream" {
				if value, ok := value.(bool); ok {
					rt.streaming = value
				}
			}
		}
	}

	if input, ok := raw["input"]; ok {
		b, err := json.Marshal(input)
		if err != nil {
			return ctx, span, err
		}
		span.SetAttributes(attribute.String("braintrust.input_json", string(b)))
	}

	b, err := json.Marshal(rt.metadata)
	if err != nil {
		return ctx, span, err
	}
	span.SetAttributes(attribute.String("braintrust.metadata", string(b)))

	return ctx, span, nil
}

func (rt *responsesTracer) TagSpan(span trace.Span, body io.Reader) error {
	if rt.streaming {
		return rt.parseStreamingResponse(span, body)
	}
	return rt.parseResponse(span, body)
}

func (rt *responsesTracer) parseStreamingResponse(span trace.Span, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		line = strings.TrimPrefix(line, "data: ")
		var envelope map[string]any
		err := json.Unmarshal([]byte(line), &envelope)
		if err != nil {
			return err
		}

		if msgType, ok := envelope["type"].(string); ok {
			// the response.completed message has everything, so just parse that. Should we
			// parse the other messages too?
			if msgType == "response.completed" {
				if msg, ok := envelope["response"].(map[string]any); ok {
					// For streaming responses, copy extra fields from the envelope
					// that might be present in the outer wrapper
					for _, field := range []string{"created", "finished_reason", "stop_reason"} {
						if val, exists := envelope[field]; exists && msg[field] == nil {
							msg[field] = val
						}
					}

					if err := rt.handleResponseCompletedMessage(span, msg); err != nil {
						return err
					}
				}
			}
		}
	}

	return scanner.Err()
}

func (rt *responsesTracer) parseResponse(span trace.Span, body io.Reader) error {
	var raw map[string]interface{}
	err := json.NewDecoder(body).Decode(&raw)
	if err != nil {
		return err
	}

	return rt.handleResponseCompletedMessage(span, raw)
}

func (rt *responsesTracer) handleResponseCompletedMessage(span trace.Span, rawMsg map[string]any) error {

	attrs := []attribute.KeyValue{}

	metadataFields := []string{
		"id",
		"object",
		"system_fingerprint",
		"completion_tokens",
		"created",
		"finished_reason",
		"stop_reason",
		"tool_calls",
		"prompt_filter_results",
		"metadata",
		"choices",
		"content_filter_results",
		"reasoning",
	}

	for _, field := range metadataFields {
		if v, ok := rawMsg[field]; ok {
			rt.metadata[field] = v
		}
	}

	if err := internal.SetJSONAttr(span, "braintrust.metadata", rt.metadata); err != nil {
		return err
	}

	if usage, ok := rawMsg["usage"].(map[string]any); ok {
		metrics := parseUsageTokens(usage)
		if err := internal.SetJSONAttr(span, "braintrust.metrics", metrics); err != nil {
			return err
		}
	}

	if output, ok := rawMsg["output"]; ok {
		if err := internal.SetJSONAttr(span, "braintrust.output_json", output); err != nil {
			return err
		}
	}

	span.SetAttributes(attrs...)

	return nil
}
