package tracemcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
)

// MCP integration tests use mcp.NewInMemoryTransports rather than VCR: the official
// Go SDK speaks JSON-RPC over stdio/streamable HTTP/in-memory links, not a single
// recordable HTTP API surface like an LLM provider client.

type greetArgs struct {
	Name string `json:"name"`
}

func setupOtel(t *testing.T) *oteltest.Exporter {
	t.Helper()
	tp, exporter := oteltest.Setup(t)
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(original)
	})
	return exporter
}

func setupInMemorySession(t *testing.T, instrumentClient, instrumentServer bool) (*mcp.ClientSession, func()) {
	t.Helper()

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	if instrumentServer {
		InstrumentServer(server)
	}
	registerTestTools(server)

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil)
	if instrumentClient {
		InstrumentClient(client)
	}

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)

	cleanup := func() {
		require.NoError(t, clientSession.Close())
		require.NoError(t, serverSession.Wait())
	}
	return clientSession, cleanup
}

func registerTestTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{Name: "greet", Description: "say hi"},
		func(_ context.Context, _ *mcp.CallToolRequest, args greetArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "hi " + args.Name}},
			}, nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "makeProgress", Description: "report progress"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
			if token := req.Params.GetProgressToken(); token != nil {
				for i := range 3 {
					_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
						ProgressToken: token,
						Message:       "working",
						Progress:      float64(i),
						Total:         2,
					})
				}
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "done"}},
			}, nil, nil
		})
}

func TestInstrumentClient_CallTool(t *testing.T) {
	exporter := setupOtel(t)
	session, cleanup := setupInMemorySession(t, true, false)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "greet",
		Arguments: greetArgs{Name: "world"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	span := spans[0]
	span.AssertNameIs("mcp.tools.call [greet]")
	assert.Equal(t, codes.Unset, span.Status().Code)
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "tool"})
	assert.Equal(t, map[string]any{
		"name":      "greet",
		"arguments": map[string]any{"name": "world"},
	}, span.Input())
	assert.Equal(t, map[string]any{"content": "hi world"}, span.Output())
	assert.Equal(t, "mcp", span.Metadata()["provider"])
	assert.Equal(t, "client", span.Metadata()["role"])
	assert.Equal(t, clientSessionCallToolAPI, span.Metadata()["api"])
	assert.Equal(t, "greet", span.Metadata()["name"])
}

func TestInstrumentClient_ListTools(t *testing.T) {
	exporter := setupOtel(t)
	session, cleanup := setupInMemorySession(t, true, false)
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.Len(t, spans, 1)

	span := spans[0]
	span.AssertNameIs("mcp.tools.list")
	span.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "task"})
	assert.Equal(t, clientSessionListToolsAPI, span.Metadata()["api"])
	output, ok := span.Output().(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(2), output["count"])
}

func TestInstrumentServer_ListTools(t *testing.T) {
	exporter := setupOtel(t)
	session, cleanup := setupInMemorySession(t, false, true)
	defer cleanup()

	result, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	spans[0].AssertNameIs("mcp.tools.list")
	assert.Equal(t, "server", spans[0].Metadata()["role"])
}

func TestInstrumentServer_CallTool(t *testing.T) {
	exporter := setupOtel(t)
	session, cleanup := setupInMemorySession(t, false, true)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "greet",
		Arguments: greetArgs{Name: "server"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	require.Len(t, spans, 2)

	handlerSpan := findSpanNamed(spans, "mcp.tools.handler [greet]")
	require.NotNil(t, handlerSpan)
	handlerSpan.AssertJSONAttrEquals("braintrust.span_attributes", map[string]any{"type": "tool"})
	assert.Equal(t, map[string]any{"content": "hi server"}, handlerSpan.Output())
	assert.Equal(t, "server", handlerSpan.Metadata()["role"])

	rpcSpan := findSpanNamed(spans, "mcp.tools.call [greet]")
	require.NotNil(t, rpcSpan)
	assert.Equal(t, oteltrace.SpanKindServer, rpcSpan.Stub.SpanKind)
}

func TestInstrumentClient_CallToolProgress(t *testing.T) {
	exporter := setupOtel(t)
	session, cleanup := setupInMemorySession(t, true, true)
	defer cleanup()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "makeProgress",
		Meta: mcp.Meta{"progressToken": "abc123"},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	spans := exporter.Flush()
	clientSpan := findSpanWithMetadata(spans, "mcp.tools.call [makeProgress]", "client")
	require.NotNil(t, clientSpan)
	assert.GreaterOrEqual(t, len(clientSpan.Events()), 1)

	output, ok := clientSpan.Output().(map[string]any)
	require.True(t, ok)
	progress, ok := output["progress"].([]any)
	require.True(t, ok)
	assert.NotEmpty(t, progress)
}

func TestInstrumentClient_CallToolError(t *testing.T) {
	exporter := setupOtel(t)
	session, cleanup := setupInMemorySession(t, true, false)
	defer cleanup()

	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "missing"})
	require.Error(t, err)

	spans := exporter.Flush()
	require.Len(t, spans, 1)
	assert.Equal(t, codes.Error, spans[0].Status().Code)
}

func TestCallToolOutput(t *testing.T) {
	t.Parallel()

	assert.Nil(t, callToolOutput(&mcp.CallToolResult{}))
	assert.Equal(t, map[string]any{
		"content": "hello",
	}, callToolOutput(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "hello"}},
	}))
	assert.Equal(t, map[string]any{
		"is_error": true,
		"content":  "failed",
	}, callToolOutput(&mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "failed"}},
	}))
}

func TestListToolsOutput(t *testing.T) {
	t.Parallel()

	output := listToolsOutput(&mcp.ListToolsResult{
		Tools: []*mcp.Tool{
			{Name: "alpha", Description: "first"},
			{Name: "beta"},
		},
		NextCursor: "next",
	})
	assert.Equal(t, 2, output["count"])
	assert.Equal(t, "next", output["next_cursor"])
}

func findSpanNamed(spans []oteltest.Span, name string) *oteltest.Span {
	for i := range spans {
		if spans[i].Name() == name {
			return &spans[i]
		}
	}
	return nil
}

func findSpanWithMetadata(spans []oteltest.Span, name, role string) *oteltest.Span {
	for i := range spans {
		if spans[i].Name() != name {
			continue
		}
		if spans[i].Metadata()["role"] == role {
			return &spans[i]
		}
	}
	return nil
}
