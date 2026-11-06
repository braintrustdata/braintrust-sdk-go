package genkit

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	compatopenai "github.com/firebase/genkit/go/plugins/compat_oai/openai"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

func TestGenkitFlowExportsInputAndOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	const braintrustURL = "https://api.braintrust.dev"
	braintrustKey := "dummy-api-key-for-replay"
	if vcr.GetVCRMode() != vcr.ModeReplay {
		braintrustKey = os.Getenv("BRAINTRUST_API_KEY")
		require.NotEmpty(t, braintrustKey, "BRAINTRUST_API_KEY is required in record/off mode")
	}
	modelKey := openaiAPIKey(t)
	httpClient := vcr.NewHTTPClient(t)
	log := logger.Discard()
	session, err := auth.NewSession(ctx, auth.Options{
		APIKey:       braintrustKey,
		APIURL:       braintrustURL,
		AppURL:       braintrustURL,
		AppPublicURL: "https://www.braintrust.dev",
		Client:       https.NewWrappedClient(braintrustKey, braintrustURL, httpClient, log),
		Logger:       log,
	})
	require.NoError(t, err)
	t.Cleanup(session.Close)
	require.NoError(t, session.Login(ctx))

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider()
	require.NoError(t, bttrace.AddSpanProcessor(tp, session, bttrace.Config{Exporter: exporter, Logger: log}))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(previous); _ = tp.Shutdown(context.Background()) })

	g := genkit.Init(ctx,
		genkit.WithPlugins(&compatopenai.OpenAI{
			APIKey: modelKey,
			Opts:   []option.RequestOption{option.WithHTTPClient(httpClient)},
		}),
		genkit.WithDefaultModel("openai/gpt-4o-mini"),
	)
	flow := DefineFlow(g, "answer-question", func(ctx context.Context, input string) (string, error) {
		response, err := genkit.Generate(ctx, g,
			ai.WithPrompt(input),
			ai.WithConfig(&openai.ChatCompletionNewParams{
				MaxCompletionTokens: openai.Int(32),
			}),
		)
		if err != nil {
			return "", err
		}
		return response.Text(), nil
	})
	const input = "What is the capital of France? Answer in one word."
	result, err := flow.Run(ctx, input)
	require.NoError(t, err)
	require.NotEmpty(t, result)
	require.NoError(t, tp.ForceFlush(ctx))

	var flowAttrs map[string]string
	for _, span := range exporter.GetSpans() {
		attrs := make(map[string]string)
		for _, a := range span.Attributes {
			attrs[string(a.Key)] = a.Value.AsString()
		}
		if attrs["genkit:metadata:subtype"] == "flow" {
			flowAttrs = attrs
			break
		}
	}
	require.NotNil(t, flowAttrs, "expected a Genkit flow span; got: %+v", exporter.GetSpans())
	require.JSONEq(t, `"What is the capital of France? Answer in one word."`, flowAttrs["braintrust.input_json"])
	var exportedOutput string
	require.NoError(t, json.Unmarshal([]byte(flowAttrs["braintrust.output_json"]), &exportedOutput))
	require.Equal(t, result, exportedOutput)
}
