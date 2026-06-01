package bedrockruntime_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/btx"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	tracebedrockruntime "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/bedrockruntime"
)

func TestBTXSpec(t *testing.T) {
	btx.RunProviderSpecs(t, executeBedrock, "bedrock")
}

// --- AWS Bedrock executor ---

// bedrockRegion is the AWS region used for Bedrock cassettes. Both record and
// replay must use the same region because go-vcr matches on method+URL, and
// the URL includes the regional hostname.
const bedrockRegion = "us-east-2"

// executeBedrock dispatches to the correct Bedrock executor based on endpoint.
func executeBedrock(ctx context.Context, spec btx.LlmSpanSpec, tp oteltrace.TracerProvider, httpClient *http.Client) error {
	region := os.Getenv("AWS_REGION")
	if region == "" {
		region = bedrockRegion
	}

	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithHTTPClient(httpClient),
	}
	if vcr.GetVCRMode() == vcr.ModeReplay {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("AKIAFAKE", "fake", ""),
		))
	}

	// Disable retries — VCR records a single request/response pair, and
	// retries would produce requests that don't match the cassette.
	opts = append(opts, awsconfig.WithRetryMaxAttempts(1))

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	client := bedrockruntime.NewFromConfig(cfg,
		tracebedrockruntime.NewMiddleware(tracebedrockruntime.WithTracerProvider(tp)),
	)

	switch {
	case strings.HasSuffix(spec.Endpoint, "/converse"):
		return executeBedrockConverse(ctx, spec, client)
	case strings.HasSuffix(spec.Endpoint, "/converse-stream"):
		return executeBedrockConverseStream(ctx, spec, client)
	default:
		return fmt.Errorf("unsupported Bedrock endpoint: %s", spec.Endpoint)
	}
}

// executeBedrockConverse handles non-streaming Bedrock Converse calls.
func executeBedrockConverse(ctx context.Context, spec btx.LlmSpanSpec, client *bedrockruntime.Client) error {
	for _, req := range spec.Requests {
		modelID := stringFromMap(req, "modelId")
		messages := buildBedrockMessages(req)

		input := &bedrockruntime.ConverseInput{
			ModelId:  &modelID,
			Messages: messages,
		}

		_, err := client.Converse(ctx, input)
		if err != nil {
			return fmt.Errorf("bedrock converse: %w", err)
		}
	}
	return nil
}

// executeBedrockConverseStream handles streaming Bedrock ConverseStream calls.
func executeBedrockConverseStream(ctx context.Context, spec btx.LlmSpanSpec, client *bedrockruntime.Client) error {
	for _, req := range spec.Requests {
		modelID := stringFromMap(req, "modelId")
		messages := buildBedrockMessages(req)

		input := &bedrockruntime.ConverseStreamInput{
			ModelId:  &modelID,
			Messages: messages,
		}

		out, err := client.ConverseStream(ctx, input)
		if err != nil {
			return fmt.Errorf("bedrock converse stream: %w", err)
		}

		// Consume the stream to trigger span finalization.
		for ev := range out.GetStream().Events() {
			_ = ev // Drain all events.
		}
		if err := out.GetStream().Close(); err != nil {
			return fmt.Errorf("closing bedrock stream: %w", err)
		}
	}
	return nil
}

// buildBedrockMessages converts spec request messages to Bedrock typed messages.
func buildBedrockMessages(req map[string]any) []brtypes.Message {
	rawMsgs, ok := req["messages"].([]any)
	if !ok {
		return nil
	}

	var messages []brtypes.Message
	for _, rm := range rawMsgs {
		mm, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		msg := brtypes.Message{
			Role: brtypes.ConversationRole(stringFromMap(mm, "role")),
		}
		if rawContent, ok := mm["content"].([]any); ok {
			for _, rc := range rawContent {
				block := buildBedrockContentBlock(rc)
				if block != nil {
					msg.Content = append(msg.Content, block)
				}
			}
		}
		messages = append(messages, msg)
	}
	return messages
}

// buildBedrockContentBlock converts a spec content block to a Bedrock typed content block.
func buildBedrockContentBlock(raw any) brtypes.ContentBlock {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	// Text block: {text: "..."}
	if text, ok := m["text"].(string); ok {
		return &brtypes.ContentBlockMemberText{Value: text}
	}

	// Image block: {image: {format: "png", source: {bytes: "<base64>"}}}
	if img, ok := m["image"].(map[string]any); ok {
		format := stringFromMap(img, "format")
		if src, ok := img["source"].(map[string]any); ok {
			if b64, ok := src["bytes"].(string); ok {
				data, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					return nil
				}
				return &brtypes.ContentBlockMemberImage{
					Value: brtypes.ImageBlock{
						Format: brtypes.ImageFormat(format),
						Source: &brtypes.ImageSourceMemberBytes{Value: data},
					},
				}
			}
		}
	}

	return nil
}

// --- Map helpers ---

func stringFromMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolFromMap(m map[string]any, key string) bool {
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
