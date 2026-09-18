// Weaviate - generative search (RAG) with tracing.
//
// This expects a local Weaviate instance with the generative-openai module
// enabled, e.g.:
//
//	docker run -p 8080:8080 -p 50051:50051 \
//	  -e ENABLE_MODULES=generative-openai \
//	  -e AUTHENTICATION_ANONYMOUS_ACCESS_ENABLED=true \
//	  semitechnologies/weaviate:1.27.0
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/weaviate/weaviate-go-client/v5/weaviate"
	"github.com/weaviate/weaviate-go-client/v5/weaviate/graphql"
	"github.com/weaviate/weaviate/entities/models"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracewaeviate "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/weaviate"
)

var tracer = otel.Tracer("weaviate-examples")

const className = "Article"

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

	client, err := weaviate.NewClient(weaviate.Config{
		Host:             "localhost:8080",
		Scheme:           "http",
		ConnectionClient: tracewaeviate.WrapClient(nil),
		Headers:          map[string]string{"X-OpenAI-Api-Key": os.Getenv("OPENAI_API_KEY")},
	})
	if err != nil {
		log.Fatal(err)
	}

	if err := seedArticle(client); err != nil {
		log.Fatal(err)
	}

	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/weaviate/main.go")
	defer rootSpan.End()

	nearVector := client.GraphQL().NearVectorArgBuilder().WithVector([]float32{0.1, 0.2, 0.3, 0.4, 0.5})
	fields := []graphql.Field{{Name: "title"}, {Name: "content"}}

	singleResultGS := graphql.NewGenerativeSearch().SingleResult("Summarize this in one sentence: {content}")
	resp, err := client.GraphQL().Get().
		WithClassName(className).
		WithFields(fields...).
		WithNearVector(nearVector).
		WithGenerativeSearch(singleResultGS).
		Do(ctx)
	if err != nil {
		log.Fatalf("Generative search (single result): %v", err)
	}
	fmt.Printf("Generative search (single result) -> %+v\n", resp.Data)

	groupedResultGS := graphql.NewGenerativeSearch().GroupedResult("Write a one-sentence tweet about this.")
	resp, err = client.GraphQL().Get().
		WithClassName(className).
		WithFields(fields...).
		WithNearVector(nearVector).
		WithGenerativeSearch(groupedResultGS).
		Do(ctx)
	if err != nil {
		log.Fatalf("Generative search (grouped result): %v", err)
	}
	fmt.Printf("Generative search (grouped result) -> %+v\n", resp.Data)

	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}

// seedArticle creates the Article class and one object if they don't
// already exist, so the example is runnable against a fresh Weaviate
// instance without a separate setup step.
func seedArticle(client *weaviate.Client) error {
	ctx := context.Background()

	exists, err := client.Schema().ClassExistenceChecker().WithClassName(className).Do(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if err := client.Schema().ClassCreator().WithClass(&models.Class{
		Class:      className,
		Vectorizer: "none",
		Properties: []*models.Property{
			{Name: "title", DataType: []string{"text"}},
			{Name: "content", DataType: []string{"text"}},
		},
	}).Do(ctx); err != nil {
		return err
	}

	_, err = client.Data().Creator().
		WithClassName(className).
		WithProperties(map[string]any{
			"title":   "The Blue Whale",
			"content": "The blue whale is the largest animal known to have ever existed. It can grow up to 30 meters long and weigh up to 200 tons. It feeds primarily on krill.",
		}).
		WithVector([]float32{0.1, 0.2, 0.3, 0.4, 0.5}).
		Do(ctx)
	return err
}
