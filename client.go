package braintrust

import (
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/google/uuid"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	internalattachment "github.com/braintrustdata/braintrust-sdk-go/internal/attachment"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
	bttraceattachment "github.com/braintrustdata/braintrust-sdk-go/trace/attachment"
)

// Client is the main Braintrust SDK client
type Client struct {
	config         *config.Config
	logger         logger.Logger
	session        *auth.Session
	tracerProvider *trace.TracerProvider
	uploader       *internalattachment.LazyUploader
}

// New creates a new Braintrust client.
//
// It will add a Braintrust exporter to the given tracer provider..
//
// Configuration is loaded from environment variables first, then
// explicit options are applied (options take precedence).
//
// Login happens asynchronously in the background by default.
//
// Example:
//
//	tp := trace.NewTracerProvider()
//	bt, err := braintrust.New(tp,
//	    braintrust.WithAPIKey("your-api-key"),
//	    braintrust.WithProject("my-project"),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer tp.Shutdown(context.Background())
func New(tp *trace.TracerProvider, opts ...Option) (*Client, error) {
	// Build config from environment variables
	cfg := config.FromEnv()

	// Apply user options (override env vars)
	for _, opt := range opts {
		opt(cfg)
	}

	// Validate configuration before proceeding
	if err := cfg.IsValid(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Setup default logger if none provided
	log := cfg.Logger
	if log == nil {
		log = logger.NewDefaultLogger()
	}

	client := &Client{
		config: cfg,
		logger: log,
	}

	log.Debug("initializing braintrust client",
		"project", cfg.DefaultProjectName,
		"org", cfg.OrgName,
		"api_url", cfg.APIURL,
		"blocking_login", cfg.BlockingLogin)

	// Create auth session - starts async login immediately
	session, err := auth.NewSession(context.Background(), auth.Options{
		AppURL:       cfg.AppURL,
		AppPublicURL: cfg.AppURL,
		APIURL:       cfg.APIURL,
		APIKey:       cfg.APIKey,
		OrgName:      cfg.OrgName,
		Logger:       log,
	})
	if err != nil {
		log.Error("failed to create auth session", "error", err)
		return nil, fmt.Errorf("failed to create auth session: %w", err)
	}

	client.session = session
	client.tracerProvider = tp
	client.uploader = internalattachment.NewLazyUploader(session, log)

	// Setup tracing with provided TracerProvider
	if err := client.setupTracing(); err != nil {
		_ = client.uploader.Shutdown(context.Background())
		session.Close()
		log.Error("failed to setup tracing", "error", err)
		return nil, fmt.Errorf("failed to setup tracing: %w", err)
	}
	log.Debug("tracing setup complete")

	// If blocking login requested, wait for it
	if cfg.BlockingLogin {
		log.Debug("waiting for login to complete")
		err := session.Login(context.Background())
		if err != nil {
			_ = client.uploader.Shutdown(context.Background())
			session.Close()
			log.Error("blocking login failed", "error", err)
			return nil, fmt.Errorf("login failed: %w", err)
		}
		log.Debug("blocking login complete")
	}

	return client, nil
}

// setupTracing initializes OpenTelemetry tracing
func (c *Client) setupTracing() error {
	// Build trace config from client config
	traceConfig := bttrace.Config{
		DefaultProjectID:       c.config.DefaultProjectID,
		DefaultProjectName:     c.config.DefaultProjectName,
		FilterAISpans:          c.config.FilterAISpans,
		EnableBuiltinAdkTraces: c.config.EnableBuiltinAdkTraces,
		SpanFilterFuncs:        convertSpanFilters(c.config.SpanFilterFuncs),
		EnableTraceConsoleLog:  c.config.EnableTraceConsoleLog,
		Exporter:               c.config.Exporter,
		Logger:                 c.logger,
		AttachmentUploader:     c.uploader,
	}

	// Add Braintrust span processor to the provided TracerProvider
	c.logger.Debug("enabling braintrust tracing on provider")
	if err := bttrace.AddSpanProcessor(c.tracerProvider, c.session, traceConfig); err != nil {
		c.logger.Error("failed to setup tracing", "error", err)
		return fmt.Errorf("failed to setup tracing: %w", err)
	}

	return nil
}

// convertSpanFilters converts config.SpanFilterFunc to trace.SpanFilterFunc
func convertSpanFilters(funcs []config.SpanFilterFunc) []bttrace.SpanFilterFunc {
	result := make([]bttrace.SpanFilterFunc, len(funcs))
	for i, f := range funcs {
		result[i] = bttrace.SpanFilterFunc(f)
	}
	return result
}

// String returns a string representation of the client
func (c *Client) String() string {
	// Get org name from auth session if available
	org := c.session.OrgInfo()
	orgName := org.Name
	if orgName == "" {
		orgName = c.config.OrgName
	}

	orgInfo := orgName
	if org.ID != "" {
		orgInfo = fmt.Sprintf("%s (ID: %s)", orgName, org.ID)
	} else if orgName == "" {
		orgInfo = "<not logged in>"
	}

	return fmt.Sprintf(`Braintrust Client:
  Organization: %s
  Project: %s
  API URL: %s
  App URL: %s`,
		orgInfo,
		c.config.DefaultProjectName,
		c.config.APIURL,
		c.config.AppURL,
	)
}

// TracerProvider returns the OpenTelemetry TracerProvider used by this client.
// This can be used to create tracers or access the provider for advanced use cases.
func (c *Client) TracerProvider() *trace.TracerProvider {
	return c.tracerProvider
}

// Tracer returns an OpenTelemetry Tracer with the given name.
// This is a convenience method equivalent to calling TracerProvider().Tracer(name, opts...).
//
// Example:
//
//	tracer := client.Tracer("my-app")
//	ctx, span := tracer.Start(ctx, "my-operation")
//	defer span.End()
func (c *Client) Tracer(name string, opts ...oteltrace.TracerOption) oteltrace.Tracer {
	return c.tracerProvider.Tracer(name, opts...)
}

// SetJSONAttachment serializes data as JSON, sets a Braintrust attachment reference on span,
// and enqueues the attachment upload on this client.
//
// To attach JSON already encoded as bytes, pass json.RawMessage(data). Passing a
// raw []byte directly will marshal it as a base64 JSON string.
func (c *Client) SetJSONAttachment(ctx context.Context, span oteltrace.Span, key string, data any, opts ...bttraceattachment.JSONAttachmentOption) (*bttraceattachment.AttachmentReference, error) {
	options := jsonAttachmentOptions("data.json", opts...)

	var b []byte
	var err error
	if options.Pretty {
		b, err = json.MarshalIndent(data, "", "  ")
	} else {
		b, err = json.Marshal(data)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to marshal JSON attachment: %w", err)
	}

	return c.setJSONBytes(ctx, span, key, b, options.Filename)
}

func jsonAttachmentOptions(defaultFilename string, opts ...bttraceattachment.JSONAttachmentOption) bttraceattachment.JSONAttachmentOptions {
	options := bttraceattachment.JSONAttachmentOptions{Filename: defaultFilename}
	for _, opt := range opts {
		opt(&options)
	}
	if options.Filename == "" {
		options.Filename = defaultFilename
	}
	return options
}

func (c *Client) setJSONBytes(ctx context.Context, span oteltrace.Span, key string, data []byte, filename string) (*bttraceattachment.AttachmentReference, error) {
	ref := bttraceattachment.AttachmentReference{
		Type:        "braintrust_attachment",
		Filename:    filename,
		ContentType: bttraceattachment.JSON,
		Key:         uuid.NewString(),
	}

	refJSON, err := json.Marshal(ref)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal attachment reference: %w", err)
	}

	if err := c.uploader.Enqueue(ctx, internalattachment.Upload{Reference: ref, Data: data}); err != nil {
		return nil, err
	}

	span.SetAttributes(attribute.String(key, string(refJSON)))
	return &ref, nil
}

// NewEvaluator creates a new evaluator for running multiple evaluations with the same
// input and output types.
//
// Example:
//
//	client, _ := braintrust.New(tp)
//
//	// Create an evaluator for string → string evaluations
//	evaluator := braintrust.NewEvaluator[string, string](client)
//
//	// Run multiple evaluations
//	result1, _ := evaluator.Run(ctx, eval.Opts[string, string]{
//	    Experiment: "test-1",
//	    Dataset:    dataset1,
//	    Task:       task1,
//	    Scorers:    scorers,
//	})
//
//	result2, _ := evaluator.Run(ctx, eval.Opts[string, string]{
//	    Experiment: "test-2",
//	    Dataset:    dataset2,
//	    Task:       task2,
//	    Scorers:    scorers,
//	})
func NewEvaluator[I, R any](client *Client) *eval.Evaluator[I, R] {
	return eval.NewEvaluator[I, R](client.session, client.tracerProvider, client.API(), client.config.DefaultProjectName)
}

// API returns an API client for making direct calls to the Braintrust API.
// This provides low-level access to projects, datasets, experiments, and other resources.
//
// Example:
//
//	client, _ := braintrust.New(tp, braintrust.WithAPIKey("your-key"))
//
//	// Create a dataset
//	apiClient := client.API()
//	project, _ := apiClient.Projects().Create(ctx, "my-project")
//	dataset, _ := apiClient.Datasets().Create(ctx, api.DatasetRequest{
//	    ProjectID:   project.ID,
//	    Name:        "my-dataset",
//	    Description: "My test dataset",
//	})
func (c *Client) API() *api.API {
	// Get API credentials from session (prefers logged-in info, falls back to config)
	apiInfo := c.session.APIInfo()

	return api.NewClient(
		apiInfo.APIKey,
		api.WithAPIURL(apiInfo.APIURL),
		api.WithLogger(c.logger),
	)
}

// Permalink returns a URL to the span in the Braintrust UI.
// If the permalink cannot be generated, it returns an empty string and logs a warning.
//
// Example:
//
//	client, _ := braintrust.New(tp, braintrust.WithAPIKey("your-key"))
//	tracer := client.Tracer("my-app")
//	ctx, span := tracer.Start(ctx, "my-operation")
//	defer span.End()
//
//	// Get the permalink
//	link := client.Permalink(span)
//	fmt.Println("View trace:", link)
func (c *Client) Permalink(span oteltrace.Span) string {
	link, err := bttrace.Permalink(span)
	if err != nil {
		c.logger.Warn("could not generate permalink", "error", err)
		return ""
	}
	return link
}
