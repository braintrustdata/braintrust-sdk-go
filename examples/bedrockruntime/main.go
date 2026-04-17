// This example demonstrates basic AWS Bedrock Runtime tracing with Braintrust.
//
// It uses the Converse API against an Anthropic Claude model. Make sure the
// AWS environment is configured (AWS_REGION, credentials, and model access in
// your Bedrock-enabled region) before running.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	tracebedrockruntime "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime"
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
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatal(err)
	}

	client := bedrockruntime.NewFromConfig(cfg, tracebedrockruntime.NewMiddleware())

	tracer := otel.Tracer("bedrockruntime-example")
	ctx, span := tracer.Start(ctx, "examples/bedrockruntime/main.go")
	defer span.End()

	out, err := client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String("us.anthropic.claude-haiku-4-5-20251001-v1:0"),
		Messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: "What is the capital of France?"},
				},
			},
		},
		InferenceConfig: &types.InferenceConfiguration{
			MaxTokens: aws.Int32(256),
		},
	})
	if err != nil {
		log.Fatal(err)
	}

	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok || len(msg.Value.Content) == 0 {
		log.Fatal("unexpected response shape")
	}
	text, _ := msg.Value.Content[0].(*types.ContentBlockMemberText)
	if text != nil {
		fmt.Printf("Response: %s\n", text.Value)
	}
	fmt.Printf("View trace: %s\n", bt.Permalink(span))
}
