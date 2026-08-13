package btx

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/internal/oteltest"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/trace/attachmentprocessor"
)

// skipSpecs lists spec display names that should be skipped.
// Add specs here when they test features not yet supported by the Go SDK.
var skipSpecs = map[string]string{}

// specRoot is set by TestMain after fetching specs.
var specRoot string

func TestMain(m *testing.M) {
	root, err := fetchSpec()
	if err != nil {
		fmt.Fprintf(os.Stderr, "btx: failed to fetch spec: %v\n", err)
		os.Exit(1)
	}
	specRoot = root

	// Default BRAINTRUST_AUTO_CONVERT_AI_ATTACHMENTS based on VCR mode,
	// but only if the user hasn't explicitly set it.
	if os.Getenv("BRAINTRUST_AUTO_CONVERT_AI_ATTACHMENTS") == "" {
		val := "false"
		if vcr.GetVCRMode() == vcr.ModeReplay {
			val = "true"
		}
		_ = os.Setenv("BRAINTRUST_AUTO_CONVERT_AI_ATTACHMENTS", val)
	}

	os.Exit(m.Run())
}

func TestBTXSpec(t *testing.T) {
	providers := []string{"openai", "anthropic", "google", "bedrock"}
	specs, err := loadSpecs(specRoot, providers)
	require.NoError(t, err, "failed to load specs")
	require.NotEmpty(t, specs, "no specs found")

	for _, spec := range specs {
		t.Run(spec.DisplayName, func(t *testing.T) {
			if reason, ok := skipSpecs[spec.DisplayName]; ok {
				t.Skipf("skipped: %s", reason)
			}

			runSpec(t, spec)
		})
	}
}

func runSpec(t *testing.T, spec LlmSpanSpec) {
	t.Helper()

	mode := vcr.GetVCRMode()
	httpClient := newBTXHTTPClient(t, spec)
	ctx := t.Context()

	var spans []map[string]any

	if mode == vcr.ModeReplay {
		// Replay mode: capture spans in-memory, no network calls.
		tp, exporter := oteltest.Setup(t)

		traceID, err := executeSpec(ctx, spec, tp, httpClient)
		require.NoError(t, err, "spec execution failed")
		require.NotEmpty(t, traceID, "empty trace ID")

		spans = convertExportedSpans(exporter)
	} else {
		// Record and off modes: hit real APIs and export spans to the real
		// Braintrust backend, then fetch them back via BTQL for validation.
		tp := sdktrace.NewTracerProvider()
		projectName := btxProjectName()
		_, err := braintrust.New(tp, braintrust.WithProject(projectName))
		require.NoError(t, err, "failed to create Braintrust client")

		traceID, err := executeSpec(ctx, spec, tp, httpClient)
		require.NoError(t, err, "spec execution failed")
		require.NotEmpty(t, traceID, "empty trace ID")

		// Shut down to flush all spans to the backend.
		require.NoError(t, tp.Shutdown(context.Background()), "failed to shutdown tracer provider")

		projectID := btxProjectID(t)
		spans, err = fetchSpansBTQL(traceID, projectID, len(spec.ExpectedBrainstoreSpans))
		require.NoError(t, err, "failed to fetch spans from BTQL")
	}

	err := validateSpans(spans, spec)
	if err != nil {
		t.Fatal(err)
	}
}

// convertExportedSpans converts in-memory OTel spans to brainstore format.
// It also runs the attachment processor to transform inline base64 data URLs
// into braintrust_attachment references, mirroring what the Braintrust span
// processor does in production.
func convertExportedSpans(exporter *oteltest.Exporter) []map[string]any {
	otelSpans := exporter.Flush()

	// Create an attachment processor with a no-op uploader so that base64
	// data URLs are replaced with braintrust_attachment references without
	// actually uploading anything.
	ap := attachmentprocessor.NewProcessor(&attachmentprocessor.NoopUploader{}, nil)

	var result []map[string]any
	for _, span := range otelSpans {
		// Extract all string attributes into a map.
		attrs := make(map[string]string)
		hasBraintrustAttr := false
		for _, a := range span.Stub.Attributes {
			if a.Value.Type().String() == "STRING" {
				key := string(a.Key)
				attrs[key] = a.Value.AsString()
				if !hasBraintrustAttr && len(key) > 11 && key[:11] == "braintrust." {
					hasBraintrustAttr = true
				}
			}
		}

		// Only include spans that have braintrust attributes.
		if !hasBraintrustAttr {
			continue
		}

		// Process attachments in input and output JSON, converting inline
		// base64 data to braintrust_attachment references.
		for _, key := range []string{"braintrust.input_json", "braintrust.output_json"} {
			if v, ok := attrs[key]; ok {
				attrs[key] = ap.ProcessAndUpload(v)
			}
		}

		brainstoreSpan := spanFromOTel(span.Name(), attrs)
		result = append(result, brainstoreSpan)
	}

	return result
}

// newBTXHTTPClient creates an HTTP client with VCR support using custom cassette paths.
// Cassettes are stored at testdata/cassettes/<provider>/<spec_name>.yaml.
func newBTXHTTPClient(t *testing.T, spec LlmSpanSpec) *http.Client {
	t.Helper()

	mode := vcr.GetVCRMode()
	if mode == vcr.ModeOff {
		return &http.Client{Timeout: 120 * time.Second}
	}

	// Build cassette path: testdata/cassettes/<provider>/<name>
	// (go-vcr appends .yaml automatically)
	cassettePath := filepath.Join("testdata", "cassettes", spec.Provider, spec.Name)

	r, err := vcr.NewVCRRecorder(t, cassettePath)
	require.NoError(t, err, "failed to create VCR recorder for %s", spec.DisplayName)

	t.Cleanup(func() {
		if err := r.Stop(); err != nil {
			t.Errorf("failed to stop VCR recorder: %v", err)
		}
	})

	return &http.Client{
		Transport: r,
		Timeout:   30 * time.Second,
	}
}

// btxProjectName returns the Braintrust project name for live/record mode.
func btxProjectName() string {
	if name := os.Getenv("BRAINTRUST_PROJECT"); name != "" {
		return name
	}
	if name := os.Getenv("BRAINTRUST_DEFAULT_PROJECT_NAME"); name != "" {
		return name
	}
	return "go-unit-test"
}

// btxProjectID returns the Braintrust project ID for live mode BTQL queries.
// It checks ID env vars first, then falls back to resolving the project name
// to an ID via the API.
func btxProjectID(t *testing.T) string {
	t.Helper()
	if id := os.Getenv("BRAINTRUST_PROJECT_ID"); id != "" {
		return id
	}
	if id := os.Getenv("BRAINTRUST_DEFAULT_PROJECT_ID"); id != "" {
		return id
	}
	id, err := resolveProjectID(btxProjectName())
	require.NoError(t, err, "failed to resolve project %q to ID", btxProjectName())
	return id
}
