package braintrust

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
	bttrace "github.com/braintrustdata/braintrust-sdk-go/trace"
)

// Client is the main Braintrust SDK client
type Client struct {
	config         *config.Config
	logger         logger.Logger
	session        *auth.Session
	tracerProvider *trace.TracerProvider
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
		AppURL:         cfg.AppURL,
		AppPublicURL:   cfg.AppURL,
		APIURL:         cfg.APIURL,
		APIKey:         cfg.APIKey,
		APIKeyResolver: cfg.APIKeyResolver,
		OrgName:        cfg.OrgName,
		Logger:         log,
	})
	if err != nil {
		log.Error("failed to create auth session", "error", err)
		return nil, fmt.Errorf("failed to create auth session: %w", err)
	}

	client.session = session
	client.tracerProvider = tp

	// Setup tracing with provided TracerProvider
	if err := client.setupTracing(); err != nil {
		log.Error("failed to setup tracing", "error", err)
		return nil, fmt.Errorf("failed to setup tracing: %w", err)
	}
	log.Debug("tracing setup complete")

	// If blocking login requested, wait for it
	if cfg.BlockingLogin {
		log.Debug("waiting for login to complete")
		err := session.Login(context.Background())
		if err != nil {
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
		DefaultProjectID:         c.config.DefaultProjectID,
		DefaultProjectName:       c.config.DefaultProjectName,
		FilterAISpans:            c.config.FilterAISpans,
		EnableBuiltinAdkTraces:   c.config.EnableBuiltinAdkTraces,
		SpanFilterFuncs:          convertSpanFilters(c.config.SpanFilterFuncs),
		EnableTraceConsoleLog:    c.config.EnableTraceConsoleLog,
		AutoConvertAIAttachments: c.config.AutoConvertAIAttachments,
		Exporter:                 c.config.Exporter,
		Logger:                   c.logger,
	}
	if c.config.Environment != nil {
		traceConfig.Environment = &bttrace.SpanOriginEnvironment{
			Type: c.config.Environment.Type,
			Name: c.config.Environment.Name,
		}
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

// NewEval creates a runnable [eval.Eval] by combining a client with an eval definition.
//
// Example:
//
//	client, _ := braintrust.New(tp)
//	e := braintrust.NewEval(client, &eval.Eval[string, string]{
//	    Name:    "classify",
//	    Task:    task,
//	    Scorers: scorers,
//	})
//	result, _ := e.Run(ctx, eval.RunOpts[string, string]{Dataset: dataset})
func NewEval[I, R any](client *Client, e *eval.Eval[I, R]) *eval.Eval[I, R] {
	evaluator := eval.NewEvaluator[I, R](client.session, client.tracerProvider, client.API(), client.config.DefaultProjectName)
	return eval.NewEval(evaluator, e)
}

// NewEvaluator creates a new evaluator for running evaluations with the same
// input and output types.
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
	_, _ = c.session.ResolveAPIKey(context.Background())

	// Get API credentials from session (prefers logged-in info, falls back to config)
	apiInfo := c.session.APIInfo()

	return api.NewClient(
		apiInfo.APIKey,
		api.WithAPIURL(apiInfo.APIURL),
		api.WithLogger(c.logger),
	)
}

// LoadPrompt loads a prompt saved in Braintrust and returns it ready to render.
//
//	p, err := client.LoadPrompt(ctx, prompt.LoadOpts{Slug: "summarizer"})
//	if err != nil {
//		return err
//	}
//	built, err := p.Build(map[string]any{"input": article})
//
// Opts.Project defaults to the client's project. The prompt is fetched every
// time; nothing is cached.
func (c *Client) LoadPrompt(ctx context.Context, opts prompt.LoadOpts) (*prompt.Prompt, error) {
	if opts.Project == "" && opts.ProjectID == "" {
		opts.Project = c.config.DefaultProjectName
	}
	return prompt.Load(ctx, c.API(), opts)
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
