package braintrust

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	intlogger "github.com/braintrustdata/braintrust-sdk-go/internal/logger"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

func TestNew_WithMinimalConfig(t *testing.T) {
	t.Parallel()

	// Create a TracerProvider
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Create client with minimal config
	client, err := New(tp,
		WithProject("test-project"),
		WithLogger(intlogger.NewFailTestLogger(t)),
	)
	require.NoError(t, err)
	require.NotNil(t, client)

	// Test TracerProvider() accessor
	assert.Equal(t, tp, client.TracerProvider())

	// Test Tracer() method creates a working tracer
	tracer := client.Tracer("test-tracer")
	assert.NotNil(t, tracer)

	// Create a span to verify tracer works
	ctx, span := tracer.Start(context.Background(), "test-span")
	span.End()
	assert.NotNil(t, ctx)

	// Test String() output contains expected info
	str := client.String()
	assert.Contains(t, str, "test-project")
	assert.Contains(t, str, "Braintrust Client")
}

func TestNew_WithBlockingLogin(t *testing.T) {
	t.Parallel()

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Create client with blocking login
	client, err := New(tp,
		WithAPIKey(auth.TestAPIKey),
		WithProject("test-project"),
		WithBlockingLogin(true),
		WithLogger(intlogger.NewFailTestLogger(t)),
	)
	require.NoError(t, err)
	require.NotNil(t, client)

	// After blocking login, session info should be available
	org := client.session.OrgInfo()
	assert.Equal(t, "test-org-id", org.ID)
	assert.Equal(t, "test-org-name", org.Name)

	// String() should show org info
	str := client.String()
	assert.Contains(t, str, "test-org-name")
	assert.Contains(t, str, "test-org-id")
}

func TestNew_WithBlockingLoginFailureShutsDownUploader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apikey/login":
			http.Error(w, "nope", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	client, err := New(tp,
		WithAPIKey("bad-key"),
		WithProject("test-project"),
		WithAppURL(server.URL),
		WithAPIURL(server.URL),
		WithBlockingLogin(true),
		WithLogger(logger.Discard()),
	)
	require.Error(t, err)
	require.Nil(t, client)
}

func TestNew_MissingAPIKey(t *testing.T) {
	// Note: No t.Parallel() because we're setting environment variables

	// Clear environment variable to ensure no API key is set
	t.Setenv("BRAINTRUST_API_KEY", "")

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Try to create client without API key
	client, err := New(tp,
		WithProject("test-project"),
		WithLogger(logger.Discard()),
	)

	// Should fail with error about API key
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "API key")
}

func TestNew_MissingAppURL(t *testing.T) {
	// Note: No t.Parallel() because we're setting environment variables

	t.Setenv("BRAINTRUST_APP_URL", "")

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Try to create client without App URL (override the default)
	client, err := New(tp,
		WithAPIKey("test-key"),
		WithProject("test-project"),
		WithAppURL(""), // Explicitly set to empty
		WithLogger(logger.Discard()),
	)

	// Should fail with error about App URL
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "app URL")
}

func TestNew_MissingAPIURL(t *testing.T) {
	// Note: No t.Parallel() because we're setting environment variables

	t.Setenv("BRAINTRUST_API_URL", "")

	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Try to create client without API URL (override the default)
	client, err := New(tp,
		WithAPIKey("test-key"),
		WithProject("test-project"),
		WithAPIURL(""), // Explicitly set to empty
		WithLogger(logger.Discard()),
	)

	// Should fail with error about API URL
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "API URL")
}

func TestTracing_EndToEnd(t *testing.T) {
	t.Parallel()

	// Create a memory exporter to capture spans without making API calls
	exporter := tracetest.NewInMemoryExporter()

	// Create TracerProvider with simple processor
	tp := trace.NewTracerProvider(
		trace.WithSyncer(exporter),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Create client with custom exporter
	client, err := New(tp,
		WithAPIKey(auth.TestAPIKey),
		WithProject("test-project"),
		WithExporter(exporter),
		WithLogger(intlogger.NewFailTestLogger(t)),
	)
	require.NoError(t, err)

	// Create a span using the client's tracer
	tracer := client.Tracer("test-app")
	ctx, span := tracer.Start(context.Background(), "test-operation")
	span.End()

	// Flush to ensure span is exported
	err = client.TracerProvider().ForceFlush(context.Background())
	require.NoError(t, err)

	// Verify context is valid
	assert.NotNil(t, ctx)

	// Verify span was captured by our exporter
	spans := exporter.GetSpans()
	assert.GreaterOrEqual(t, len(spans), 1, "Expected at least one span to be exported")
}

func TestClientSetJSONUploadsAttachment(t *testing.T) {
	var statusReq map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apikey/login":
			require.Equal(t, http.MethodPost, r.Method)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"org_info": []map[string]any{{
					"id":        "org-id",
					"name":      "org-name",
					"api_url":   "http://" + r.Host,
					"proxy_url": "http://" + r.Host,
				}},
			})
		case "/attachment":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"signedUrl": "http://" + r.Host + "/upload",
				"headers":   map[string]string{},
			})
		case "/upload":
			w.WriteHeader(http.StatusOK)
		case "/attachment/status":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&statusReq))
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	client, err := New(tp,
		WithAPIKey("api-key"),
		WithProject("test-project"),
		WithAppURL(server.URL),
		WithAPIURL(server.URL),
		WithExporter(exporter),
		WithBlockingLogin(true),
		WithLogger(logger.Discard()),
	)
	require.NoError(t, err)

	ctx, span := tp.Tracer("raw-tracer").Start(context.Background(), "span")
	_, err = client.SetJSONAttachment(ctx, span, "braintrust.input_json", map[string]any{"hello": "world"})
	require.NoError(t, err)
	span.End()

	require.NoError(t, tp.ForceFlush(context.Background()))
	require.NotNil(t, statusReq)
	status, ok := statusReq["status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "done", status["upload_status"])
}

func TestTracing_WithExporter(t *testing.T) {
	t.Parallel()

	// Create a memory exporter for testing
	exporter := tracetest.NewInMemoryExporter()

	// Create TracerProvider with simple processor
	tp := trace.NewTracerProvider(
		trace.WithSyncer(exporter),
	)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	// Create client with custom exporter
	client, err := New(tp,
		WithAPIKey(auth.TestAPIKey),
		WithProject("test-project"),
		WithExporter(exporter),
		WithLogger(intlogger.NewFailTestLogger(t)),
	)
	require.NoError(t, err)

	// Create a span
	tracer := client.Tracer("test-app")
	_, span := tracer.Start(context.Background(), "test-span")
	span.End()

	// Force flush to ensure span is exported
	err = tp.ForceFlush(context.Background())
	require.NoError(t, err)

	// Verify span was captured
	spans := exporter.GetSpans()
	assert.GreaterOrEqual(t, len(spans), 1)
}
