package openai

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

func TestOpenAIResponsesStreamingError(t *testing.T) {
	client, _, exporter := setUpTest(t)

	// Exceed GPT-4's context window to record a real error delivered inside
	// an HTTP 200 event stream, rather than a transport or HTTP status error.
	var rawResponse *http.Response
	stream := client.Responses.NewStreaming(context.Background(), responses.ResponseNewParams{
		Model: openai.ChatModelGPT4,
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(strings.Repeat("hello ", 9000))},
	}, option.WithMaxRetries(0), option.WithResponseInto(&rawResponse))
	t.Cleanup(func() { require.NoError(t, stream.Close()) })
	for stream.Next() {
	}
	require.NotNil(t, rawResponse)
	require.Equal(t, http.StatusOK, rawResponse.StatusCode)
	require.ErrorContains(t, stream.Err(), "context_length_exceeded")
	require.NoError(t, stream.Close())

	span := exporter.FlushOne()
	assert.Equal(t, codes.Error, span.Status().Code)
	assert.Contains(t, span.Status().Description, "context_length_exceeded")
	events := span.Events()
	require.Len(t, events, 1)
	assert.Equal(t, "exception", events[0].Name)
	assert.Contains(t, events[0].Attributes, attribute.String("exception.message", span.Status().Description))
}

func TestResponsesStreamingErrorEvents(t *testing.T) {
	// The live API currently sends a nested error, covered by the VCR test.
	// Its documented flat error event and a standalone response.failed event
	// cannot be selected in a live request, so exercise these parser variants
	// directly rather than pretending they are recorded API interactions.
	for _, tt := range []struct {
		name    string
		event   string
		wantErr string
	}{
		{
			name:    "flat error",
			event:   `{"type":"error","code":"context_length_exceeded","message":"Input is too long","param":"input"}`,
			wantErr: "context_length_exceeded",
		},
		{
			name:    "nested error",
			event:   `{"type":"error","error":{"code":"context_length_exceeded","message":"Input is too long","param":"input"}}`,
			wantErr: "context_length_exceeded",
		},
		{
			name: "error before truncated event",
			// The SDK can close the stream after an error while the buffer
			// contains only part of the next event. VCR replays complete bodies.
			event:   "{\"type\":\"error\",\"code\":\"context_length_exceeded\"}\n\ndata: {\"type\":",
			wantErr: "context_length_exceeded",
		},
		{
			name:    "error before scanner failure",
			event:   "{\"type\":\"error\",\"code\":\"context_length_exceeded\"}\n\ndata: " + strings.Repeat("x", bufio.MaxScanTokenSize),
			wantErr: "context_length_exceeded",
		},
		{
			name:    "failed response",
			event:   `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"Try again"},"output":[],"usage":{"input_tokens":10,"output_tokens":0,"total_tokens":10}}}`,
			wantErr: "server_error",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tp, exporter := oteltest.Setup(t)
			_, span := tp.Tracer("test").Start(context.Background(), "responses")
			rt := newResponsesTracer(&middlewareConfig{})
			err := rt.parseStreamingResponse(span, strings.NewReader("data: "+tt.event+"\n\n"))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			span.End()
			if tt.name == "failed response" {
				ts := exporter.FlushOne()
				assert.Equal(t, "failed", ts.Metadata()["status"])
				assert.Equal(t, float64(10), ts.Metrics()["tokens"])
				ts.AssertJSONAttrEquals("braintrust.output_json", []any{})
			}
		})
	}
}
