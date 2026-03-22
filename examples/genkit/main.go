// This example demonstrates basic Genkit tracing with Braintrust.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/googlegenai"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracegenkit "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/genkit"
)

func main() {
	tp := trace.NewTracerProvider()
	defer tp.Shutdown(context.Background()) //nolint:errcheck
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	g := genkit.Init(ctx,
		genkit.WithPlugins(&googlegenai.GoogleAI{
			APIKey: os.Getenv("GOOGLE_API_KEY"),
		}),
		genkit.WithDefaultModel("googleai/gemini-2.5-flash"),
	)

	tracer := otel.Tracer("genkit-example")
	ctx, span := tracer.Start(ctx, "examples/genkit/main.go")
	defer span.End()

	resp, err := tracegenkit.Generate(ctx, g,
		ai.WithPrompt("What is the capital of France?"),
	)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Response: %s\n", resp.Text())
	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
