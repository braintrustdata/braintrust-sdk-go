// Pinecone Inference API - embeddings and reranking with tracing
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/pinecone-io/go-pinecone/v6/pinecone"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracepinecone "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/pinecone"
)

var tracer = otel.Tracer("pinecone-examples")

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

	pc, err := pinecone.NewClient(pinecone.NewClientParams{
		ApiKey:     os.Getenv("PINECONE_API_KEY"),
		RestClient: tracepinecone.Client(tracepinecone.WithTracerProvider(tp)),
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, rootSpan := tracer.Start(context.Background(), "examples/internal/pinecone/main.go")
	defer rootSpan.End()

	embedResp, err := pc.Inference.Embed(ctx, &pinecone.EmbedRequest{
		Model:      "multilingual-e5-large",
		TextInputs: []string{"Who created the first computer?"},
		Parameters: pinecone.EmbedParameters{"input_type": "passage"},
	})
	if err != nil {
		log.Fatalf("Embed: %v", err)
	}
	fmt.Printf("Embed -> %d embedding(s), vector_type=%s\n", len(embedResp.Data), embedResp.VectorType)

	returnDocuments := true
	rerankResp, err := pc.Inference.Rerank(ctx, &pinecone.RerankRequest{
		Model:           "bge-reranker-v2-m3",
		Query:           "i love to eat apples",
		ReturnDocuments: &returnDocuments,
		RankFields:      &[]string{"text"},
		Documents: []pinecone.Document{
			{"id": "doc1", "text": "Apple is a popular fruit known for its sweetness."},
			{"id": "doc2", "text": "Many people enjoy eating apples as a healthy snack."},
			{"id": "doc3", "text": "Apple Inc. has revolutionized the tech industry."},
		},
	})
	if err != nil {
		log.Fatalf("Rerank: %v", err)
	}
	fmt.Printf("Rerank -> top match: %v (score=%.3f)\n", (*rerankResp.Data[0].Document)["id"], rerankResp.Data[0].Score)

	fmt.Printf("View trace: %s\n", bt.Permalink(rootSpan))
}
