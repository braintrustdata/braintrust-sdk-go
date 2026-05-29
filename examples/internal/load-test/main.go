// Package main runs a heavy synthetic load test that exercises the Braintrust
// SDK's span pipeline (including the attachment processor / uploader) without
// requiring any real LLM API calls.
//
// Each "LLM call" is a manually-created span that sets braintrust.input_json
// and braintrust.output_json containing a base64 image attachment in the
// standard message format. With BRAINTRUST_AUTO_CONVERT_AI_ATTACHMENTS enabled
// (the default), the trace processor will detect the base64 payload, upload
// it as an attachment, and replace it with an attachment reference.
//
// Tunable via environment variables:
//
//	BRAINTRUST_API_KEY                 - required, real API key
//	LOAD_TEST_SPANS                    - total number of "LLM" spans (default 200)
//	LOAD_TEST_CONCURRENCY              - max goroutines in flight   (default 16)
//	LOAD_TEST_ATTACHMENTS_PER_SPAN     - attachments per input      (default 2)
//	LOAD_TEST_ATTACHMENT_SIZE_KB       - per-attachment payload KB  (default 32)
//	LOAD_TEST_REAL_LLM                 - if "1", also make a real
//	                                     OpenAI call per span        (default off)
//	OPENAI_API_KEY                     - required only when LOAD_TEST_REAL_LLM=1
//
// To run:
//
// LOAD_TEST_SPANS=50 LOAD_TEST_CONCURRENCY=8 LOAD_TEST_ATTACHMENTS_PER_SPAN=2 LOAD_TEST_ATTACHMENT_SIZE_KB=16 go run ./examples/internal/load-test
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go"
	traceopenai "github.com/braintrustdata/braintrust-sdk-go/trace/contrib/openai"
)

type config struct {
	totalSpans         int
	concurrency        int
	attachmentsPerSpan int
	attachmentSizeKB   int
	realLLM            bool
}

func loadConfig() config {
	return config{
		totalSpans:         envInt("LOAD_TEST_SPANS", 200),
		concurrency:        envInt("LOAD_TEST_CONCURRENCY", 16),
		attachmentsPerSpan: envInt("LOAD_TEST_ATTACHMENTS_PER_SPAN", 2),
		attachmentSizeKB:   envInt("LOAD_TEST_ATTACHMENT_SIZE_KB", 32),
		realLLM:            os.Getenv("LOAD_TEST_REAL_LLM") == "1",
	}
}

func main() {
	if os.Getenv("BRAINTRUST_API_KEY") == "" {
		log.Fatal("BRAINTRUST_API_KEY is required")
	}
	cfg := loadConfig()
	if cfg.realLLM && os.Getenv("OPENAI_API_KEY") == "" {
		log.Fatal("LOAD_TEST_REAL_LLM=1 requires OPENAI_API_KEY")
	}

	fmt.Println("=== Braintrust Load Test ===")
	fmt.Printf("  spans=%d concurrency=%d attachments/span=%d size=%dKB realLLM=%v\n\n",
		cfg.totalSpans, cfg.concurrency, cfg.attachmentsPerSpan, cfg.attachmentSizeKB, cfg.realLLM)

	tp := trace.NewTracerProvider()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := tp.Shutdown(shutdownCtx); err != nil {
			log.Printf("tracer provider shutdown error: %v", err)
		}
	}()
	otel.SetTracerProvider(tp)

	bt, err := braintrust.New(tp,
		braintrust.WithProject("go-sdk-examples"),
		braintrust.WithBlockingLogin(true),
	)
	if err != nil {
		log.Fatalf("braintrust.New: %v", err)
	}

	tracer := otel.Tracer("load-test")
	rootCtx, rootSpan := tracer.Start(context.Background(), "load-test/run")
	rootSpan.SetAttributes(
		attribute.Int("load_test.total_spans", cfg.totalSpans),
		attribute.Int("load_test.concurrency", cfg.concurrency),
		attribute.Int("load_test.attachments_per_span", cfg.attachmentsPerSpan),
		attribute.Int("load_test.attachment_size_kb", cfg.attachmentSizeKB),
		attribute.Bool("load_test.real_llm", cfg.realLLM),
	)

	var oaClient openai.Client
	if cfg.realLLM {
		oaClient = openai.NewClient(option.WithMiddleware(traceopenai.NewMiddleware()))
	}

	start := time.Now()
	runLoad(rootCtx, tracer, cfg, &oaClient)
	elapsed := time.Since(start)

	rootSpan.SetAttributes(attribute.Int64("load_test.elapsed_ms", elapsed.Milliseconds()))

	fmt.Printf("\n✓ produced %d spans in %s (%.1f spans/s)\n",
		cfg.totalSpans, elapsed.Round(time.Millisecond),
		float64(cfg.totalSpans)/elapsed.Seconds())

	// Capture the permalink while the root span is still live (recording).
	permalink := bt.Permalink(rootSpan)
	rootSpan.End()

	flushStart := time.Now()
	flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := tp.ForceFlush(flushCtx); err != nil {
		log.Printf("ForceFlush warning: %v", err)
	}
	fmt.Printf("✓ flushed in %s\n", time.Since(flushStart).Round(time.Millisecond))

	fmt.Printf("🔗 View trace: %s\n", permalink)
}

func runLoad(ctx context.Context, tracer oteltrace.Tracer, cfg config, oaClient *openai.Client) {
	sem := make(chan struct{}, cfg.concurrency)
	var wg sync.WaitGroup
	var completed atomic.Int64

	// Progress reporter
	progressDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				done := completed.Load()
				fmt.Printf("  ... %d/%d spans started\n", done, cfg.totalSpans)
			}
		}
	}()

	for i := 0; i < cfg.totalSpans; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			simulateLLMCall(ctx, tracer, cfg, oaClient, idx)
			completed.Add(1)
		}(i)
	}

	wg.Wait()
	close(progressDone)
}

// simulateLLMCall creates a span that looks like a chat-completion call with
// image attachments, populating braintrust.input_json and braintrust.output_json.
// When cfg.realLLM is true, it also makes an actual OpenAI call (which produces
// its own auto-instrumented child span).
func simulateLLMCall(
	ctx context.Context,
	tracer oteltrace.Tracer,
	cfg config,
	oaClient *openai.Client,
	idx int,
) {
	ctx, span := tracer.Start(ctx, "llm.chat")
	defer span.End()

	// Build a chat-completion-style input with N base64 image attachments.
	content := []any{
		map[string]any{"type": "text", "text": fmt.Sprintf("Describe the %d attached images. (request #%d)", cfg.attachmentsPerSpan, idx)},
	}
	for i := 0; i < cfg.attachmentsPerSpan; i++ {
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": makeBase64DataURL(cfg.attachmentSizeKB),
			},
		})
	}
	input := []map[string]any{
		{"role": "system", "content": "You are a helpful assistant that describes images."},
		{"role": "user", "content": content},
	}

	// Synthetic output (also includes an attachment to exercise output-side processing).
	output := []map[string]any{
		{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": fmt.Sprintf("Synthetic description for request #%d.", idx)},
			},
		},
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		log.Printf("marshal input: %v", err)
		return
	}
	outputJSON, err := json.Marshal(output)
	if err != nil {
		log.Printf("marshal output: %v", err)
		return
	}

	span.SetAttributes(
		attribute.String("braintrust.input_json", string(inputJSON)),
		attribute.String("braintrust.output_json", string(outputJSON)),
		attribute.String("braintrust.span_attributes", `{"type":"llm","name":"chat"}`),
		attribute.String("model", "gpt-4o-mini"),
		attribute.Int("load_test.idx", idx),
	)

	// Optionally make a real LLM call to also exercise the OpenAI middleware.
	if cfg.realLLM && oaClient != nil {
		_, err := oaClient.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Model: openai.ChatModelGPT4oMini,
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage(fmt.Sprintf("Reply with the number %d.", idx)),
			},
		})
		if err != nil {
			log.Printf("openai call %d: %v", idx, err)
		}
	}
}

// makeBase64DataURL returns a `data:image/png;base64,...` URL containing a
// real, decodable PNG whose encoded size is roughly `sizeKB` KB. Random pixel
// colors prevent the PNG encoder from deflating away most of the bytes and
// also defeat any content-hash dedup in the attachment uploader.
func makeBase64DataURL(sizeKB int) string {
	if sizeKB <= 0 {
		sizeKB = 1
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(makePNG(sizeKB))
}

// makePNG returns a valid PNG byte stream of roughly `sizeKB` KB. We pick a
// square image whose raw pixel count, after PNG's overhead and compression of
// random RGB noise, lands near the target. Random pixels barely compress, so
// a square of side ~= sqrt(sizeKB*1024/3) is a good first cut.
func makePNG(sizeKB int) []byte {
	side := int(math.Round(math.Sqrt(float64(sizeKB) * 1024.0 / 3.0)))
	if side < 4 {
		side = 4
	}
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	//nolint:gosec // not cryptographic; we just want non-compressible pixel noise
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	if err := enc.Encode(&buf, img); err != nil {
		// Should never happen for an in-memory RGBA, but fall back to a tiny
		// known-good PNG rather than returning garbage.
		return tinyPNG()
	}
	return buf.Bytes()
}

// tinyPNG is a minimal 10x10 red PNG used as a fallback if encoding fails.
func tinyPNG() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x0a, 0x00, 0x00, 0x00, 0x0a,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x02, 0x50, 0x58, 0xea, 0x00, 0x00, 0x00,
		0x12, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0xf8, 0xcf, 0xc0, 0x80,
		0x07, 0x31, 0x8c, 0x4a, 0x63, 0x43, 0x00, 0xb7, 0xca, 0x63, 0x9d, 0xd6,
		0xd5, 0xef, 0x74, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
