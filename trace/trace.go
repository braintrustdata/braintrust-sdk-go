// Package trace provides distributed tracing functionality for Braintrust experiments.
//
// This package is built on OpenTelemetry and integrates with the Braintrust Client
// for session-based authentication.
//
// To enable tracing, create a TracerProvider and Braintrust client:
//
//	tp := trace.NewTracerProvider()
//	defer tp.Shutdown(context.Background())
//
//	bt, err := braintrust.New(tp,
//	    braintrust.WithAPIKey(os.Getenv("BRAINTRUST_API_KEY")),
//	    braintrust.WithProject("my-project"),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// Once the client is created, create spans using OpenTelemetry:
//
//	tracer := bt.Tracer("my-app")
//	ctx, span := tracer.Start(ctx, "my-operation")
//	span.SetAttributes(attribute.String("user.id", "123"))
//	span.End()
package trace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/attachmentprocessor"
)

// Config holds configuration for Braintrust tracing
type Config struct {
	// Default parent for spans
	DefaultProjectID   string
	DefaultProjectName string

	// Span filtering
	FilterAISpans          bool
	EnableBuiltinAdkTraces bool // if false (default), drop spans from Google ADK (gcp.vertex.agent) to avoid duplicates
	SpanFilterFuncs        []SpanFilterFunc

	// Attachment processing
	AutoConvertAIAttachments bool // scan spans for base64 attachments and upload them

	// Debug
	EnableTraceConsoleLog bool

	// Test override: provide custom exporter (e.g., memory exporter for tests)
	Exporter sdktrace.SpanExporter

	// Test override: custom attachment uploader (e.g., noop for tests)
	AttachmentUploader attachmentprocessor.Uploader

	// Logger
	Logger logger.Logger

	Environment *SpanOriginEnvironment
}

// SpanOriginEnvironment describes where spans are produced for span-origin provenance.
type SpanOriginEnvironment struct {
	Type string
	Name string
}

// SpanFilterFunc decides which spans to send to Braintrust.
// Return >0 to keep, <0 to drop, 0 to not influence.
type SpanFilterFunc func(span sdktrace.ReadOnlySpan) int

// GetSpanProcessor creates a Braintrust span processor.
func GetSpanProcessor(session *auth.Session, cfg Config) (sdktrace.SpanProcessor, error) {
	log := cfg.Logger
	if log == nil {
		log = logger.NewDefaultLogger()
	}

	// Get API credentials - always available immediately
	apiInfo := session.APIInfo()

	var exporter sdktrace.SpanExporter
	var err error

	// Use provided exporter (for tests) or create HTTP OTLP exporter
	if cfg.Exporter != nil {
		exporter = cfg.Exporter
		log.Debug("using provided exporter")
	} else if apiInfo.APIKey != "" {
		otelOpts, err := getHTTPOtelOpts(apiInfo.APIURL, apiInfo.APIKey)
		if err != nil {
			return nil, err
		}

		exporter, err = otlptrace.New(
			context.Background(),
			otlptracehttp.NewClient(otelOpts...),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
		}
		log.Debug("created OTLP HTTP exporter", "endpoint", apiInfo.APIURL)
	} else {
		exporter = newAPIKeyResolvingExporter(session, log)
		log.Debug("created lazy OTLP HTTP exporter", "endpoint", apiInfo.APIURL)
	}

	// Wrap in batch processor
	batchProcessor := sdktrace.NewBatchSpanProcessor(exporter)

	// Get default parent from config
	parent := getParent(cfg)
	log.Debug("using default parent", "parent", parent.String())

	// Build filter list
	var filters, rootFilters []SpanFilterFunc
	filters = append(filters, cfg.SpanFilterFuncs...)
	if !cfg.EnableBuiltinAdkTraces {
		filters = append(filters, adkSpanFilterFunc)
		rootFilters = append(rootFilters, adkSpanFilterFunc)
		log.Debug("ADK span filtering enabled")
	}
	if cfg.FilterAISpans {
		filters = append(filters, aiSpanFilterFunc)
		log.Debug("AI span filtering enabled")
	}

	// Set up attachment processor if enabled.
	var ap *attachmentprocessor.Processor
	var uploader attachmentprocessor.Uploader
	if cfg.AutoConvertAIAttachments {
		if cfg.AttachmentUploader != nil {
			uploader = cfg.AttachmentUploader
		} else {
			uploader = attachmentprocessor.NewS3Uploader(attachmentprocessor.UploaderConfig{
				APIURL:   apiInfo.APIURL,
				APIKey:   apiInfo.APIKey,
				LoginURL: session.AppPublicURL(),
				Logger:   log,
			})
		}
		ap = attachmentprocessor.NewProcessor(uploader, log)
		log.Debug("attachment processing enabled")
	}

	// Wrap with Braintrust span processor (adds parent labels, filtering, etc.)
	// The processor will get endpoints and org name from session dynamically
	btProcessor, err := newSpanProcessor(
		batchProcessor,
		parent,
		filters,
		rootFilters,
		session,
		log,
		ap,
		uploader,
		resolveEnvironment(cfg.Environment),
	)
	if err != nil {
		return nil, err
	}

	return btProcessor, nil
}

// AddSpanProcessor creates and registers a Braintrust span processor.
func AddSpanProcessor(tp *sdktrace.TracerProvider, session *auth.Session, cfg Config) error {
	log := cfg.Logger
	if log == nil {
		log = logger.NewDefaultLogger()
	}

	processor, err := GetSpanProcessor(session, cfg)
	if err != nil {
		return err
	}

	tp.RegisterSpanProcessor(processor)
	log.Debug("registered Braintrust span processor")

	// Add console log processor if requested
	if cfg.EnableTraceConsoleLog {
		consoleExporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err == nil {
			tp.RegisterSpanProcessor(sdktrace.NewBatchSpanProcessor(consoleExporter))
			log.Debug("registered console trace exporter")
		} else {
			log.Warn("failed to create console exporter", "error", err)
		}
	}

	return nil
}

type apiKeyResolvingExporter struct {
	session *auth.Session
	logger  logger.Logger

	mu       sync.Mutex
	exporter sdktrace.SpanExporter
	shutdown bool
}

func newAPIKeyResolvingExporter(session *auth.Session, log logger.Logger) sdktrace.SpanExporter {
	return &apiKeyResolvingExporter{session: session, logger: log}
}

func (e *apiKeyResolvingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	exporter, err := e.getExporter(ctx)
	if err != nil {
		return err
	}
	return exporter.ExportSpans(ctx, spans)
}

func (e *apiKeyResolvingExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	exporter := e.exporter
	e.shutdown = true
	e.mu.Unlock()

	if exporter == nil {
		return nil
	}
	return exporter.Shutdown(ctx)
}

func (e *apiKeyResolvingExporter) getExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	e.mu.Lock()
	if e.exporter != nil {
		exporter := e.exporter
		e.mu.Unlock()
		return exporter, nil
	}
	if e.shutdown {
		e.mu.Unlock()
		return nil, fmt.Errorf("OTLP exporter is shut down")
	}
	e.mu.Unlock()

	apiKey, ok := e.session.ResolveAPIKey(ctx)
	if !ok {
		return nil, fmt.Errorf("API key is required")
	}

	apiInfo := e.session.APIInfo()
	otelOpts, err := getHTTPOtelOpts(apiInfo.APIURL, apiKey)
	if err != nil {
		return nil, err
	}

	exporter, err := otlptrace.New(ctx, otlptracehttp.NewClient(otelOpts...))
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP exporter: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.exporter != nil {
		if shutdownErr := exporter.Shutdown(ctx); shutdownErr != nil {
			e.logger.Warn("failed to shut down duplicate OTLP exporter", "error", shutdownErr)
		}
		return e.exporter, nil
	}
	if e.shutdown {
		if shutdownErr := exporter.Shutdown(ctx); shutdownErr != nil {
			e.logger.Warn("failed to shut down unused OTLP exporter", "error", shutdownErr)
		}
		return nil, fmt.Errorf("OTLP exporter is shut down")
	}

	e.exporter = exporter
	e.logger.Debug("initialized OTLP HTTP exporter", "endpoint", apiInfo.APIURL)
	return e.exporter, nil
}

// ParentOtelAttrKey is the OpenTelemetry attribute key used to associate spans with Braintrust parents.
// This enables spans to be grouped under specific projects or experiments in the Braintrust platform.
// Parents are formatted as "project_id:{uuid}" or "experiment_id:{uuid}".
const ParentOtelAttrKey = "braintrust.parent"

// Internal attribute keys for Braintrust span metadata.
const (
	orgAttrKey         = "braintrust.org"
	appURLAttrKey      = "braintrust.app_url"
	contextJSONAttrKey = "braintrust.context_json"
)

type contextKey string

// a context key that cannot possibly collide with any other keys
var parentContextKey contextKey = ParentOtelAttrKey

// SetParent will add a parent to the given context. Any span started with that context will
// be marked with that parent, and sent to the given project or experiment in Braintrust.
//
// The parent is stored in both context values (for same-process access) and W3C baggage
// (for distributed tracing across process boundaries).
//
// Example:
//
//	ctx = trace.SetParent(ctx, trace.Parent{Type: trace.ParentTypeProjectName, ID: "my-project"})
//	_, span := tracer.Start(ctx, "database-query")
func SetParent(ctx context.Context, parent Parent) context.Context {
	// Store parent in context value (for same-process access)
	ctx = context.WithValue(ctx, parentContextKey, parent)

	// Also store in baggage for distributed tracing across process boundaries.
	// Baggage propagates automatically through W3C headers, while context values don't.
	member, err := baggage.NewMember(ParentOtelAttrKey, parent.String())
	if err != nil {
		// Log warning but continue - context value will still work for same-process
		return ctx
	}

	// Merge with existing baggage if any
	existingBag := baggage.FromContext(ctx)
	bag, err := existingBag.SetMember(member)
	if err != nil {
		// Log warning but continue - context value will still work for same-process
		return ctx
	}

	return baggage.ContextWithBaggage(ctx, bag)
}

// GetParent returns the parent from the context and a boolean indicating if it was set.
// It first checks the context value (for same-process access), then falls back to
// baggage (for distributed tracing across process boundaries).
func GetParent(ctx context.Context) (bool, Parent) {
	// First, try to get from context value (fast path for same-process)
	if parent, ok := ctx.Value(parentContextKey).(Parent); ok {
		return true, parent
	}

	// Fall back to baggage (for distributed tracing)
	bag := baggage.FromContext(ctx)
	if parentStr := bag.Member(ParentOtelAttrKey).Value(); parentStr != "" {
		parent, err := parseParent(parentStr)
		if err != nil {
			// Invalid parent format in baggage, return not found
			return false, Parent{}
		}
		return true, parent
	}

	return false, Parent{}
}

// ParentType represents the different places spans can be sent to
// in Braintrust - projects, experiments, etc.
type ParentType string

const (
	// ParentTypeProjectName is the type of parent that represents a project by name.
	ParentTypeProjectName ParentType = "project_name"
	// ParentTypeProjectID is the type of parent that represents a project by ID.
	ParentTypeProjectID ParentType = "project_id"
	// ParentTypeExperimentID is the type of parent that represents an experiment by ID.
	ParentTypeExperimentID ParentType = "experiment_id"
)

// IsValid returns true if the ParentType is a valid type.
func (p ParentType) IsValid() bool {
	return p == ParentTypeProjectName || p == ParentTypeProjectID || p == ParentTypeExperimentID
}

// Parent represents where data goes in Braintrust - a project, an experiment, etc.
type Parent struct {
	Type ParentType
	ID   string
}

// Attr returns the OTel attribute for this parent.
func (p Parent) Attr() attribute.KeyValue {
	return attribute.String(ParentOtelAttrKey, p.String())
}

func (p Parent) String() string {
	return fmt.Sprintf("%s:%s", p.Type, p.ID)
}

// NewParent creates a new parent with the given type and ID.
func NewParent(t ParentType, id string) Parent {
	return Parent{Type: t, ID: id}
}

func parseParent(s string) (Parent, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return Parent{}, fmt.Errorf("invalid parent format: %s", s)
	}
	pt := ParentType(parts[0])
	if !pt.IsValid() {
		return Parent{}, fmt.Errorf("invalid parent type: %s", parts[0])
	}

	return Parent{Type: pt, ID: parts[1]}, nil
}

// otelAttrs contains the attributes that are added to all spans in our processor.
type otelAttrs struct {
	parent attribute.KeyValue

	mu sync.RWMutex

	orgName string
	appURL  string

	attrs []attribute.KeyValue
}

func newOtelAttrs(parent Parent, orgName string, appURL string) *otelAttrs {
	oa := &otelAttrs{
		parent:  parent.Attr(),
		orgName: orgName,
		appURL:  appURL,
	}
	oa.makeAttrs()
	return oa
}

// Get returns the attributes that should be set on the span. The parent is selectively
// applied to spans with no parent, so it's separate.
func (o *otelAttrs) Get() (parent attribute.KeyValue, always []attribute.KeyValue) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.parent, o.attrs
}

func (o *otelAttrs) SetOrgName(orgName string) {
	o.mu.Lock()
	o.orgName = orgName
	o.mu.Unlock()

	o.makeAttrs()
}

func (o *otelAttrs) makeAttrs() {
	var attrs []attribute.KeyValue
	if o.orgName != "" {
		attrs = append(attrs, attribute.String(orgAttrKey, o.orgName))
	}
	if o.appURL != "" {
		attrs = append(attrs, attribute.String(appURLAttrKey, o.appURL))
	}

	o.mu.Lock()
	o.attrs = attrs
	o.mu.Unlock()
}

// OTel attribute keys for Braintrust span input/output.
const (
	inputJSONAttrKey  = attribute.Key("braintrust.input_json")
	outputJSONAttrKey = attribute.Key("braintrust.output_json")
)

type spanProcessor struct {
	wrapped             sdktrace.SpanProcessor
	filters             []SpanFilterFunc
	rootFilters         []SpanFilterFunc
	otelAttrs           *otelAttrs
	session             *auth.Session // Session provides endpoints and org name
	logger              logger.Logger
	attachmentProcessor *attachmentprocessor.Processor // nil when attachment processing is disabled
	attachmentUploader  attachmentprocessor.Uploader   // nil when attachment processing is disabled
	environment         *SpanOriginEnvironment
}

// newSpanProcessor creates a new span processor that wraps another processor and adds parent labeling.
func newSpanProcessor(
	proc sdktrace.SpanProcessor,
	defaultParent Parent,
	filters []SpanFilterFunc,
	rootFilters []SpanFilterFunc,
	session *auth.Session,
	log logger.Logger,
	ap *attachmentprocessor.Processor,
	uploader attachmentprocessor.Uploader,
	environment *SpanOriginEnvironment,
) (*spanProcessor, error) {
	// Get app URL from session
	appURL := session.AppPublicURL()

	// Initialize with empty org name - will be looked up dynamically from session
	attrs := newOtelAttrs(defaultParent, "", appURL)

	sp := &spanProcessor{
		wrapped:             proc,
		filters:             filters,
		rootFilters:         rootFilters,
		otelAttrs:           attrs,
		session:             session,
		logger:              log,
		attachmentProcessor: ap,
		attachmentUploader:  uploader,
		environment:         environment,
	}

	return sp, nil
}

// OnStart is called when a span is started and assigns parent attributes.
// It assigns spans to projects or experiments based on context or default parent.
func (sp *spanProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	// Always check session for latest org name and appURL (non-blocking)
	// If login hasn't completed yet, OrgName() returns empty string
	orgName := sp.session.OrgName()
	if orgName != "" {
		sp.otelAttrs.SetOrgName(orgName)
	}

	// Update appURL in case it changed
	appURL := sp.session.AppPublicURL()
	sp.otelAttrs.appURL = appURL

	defaultParent, attrs := sp.otelAttrs.Get()

	// All otel spans need to have a parent attached (e.g. project_name:my-project).
	// If the span doesn't have one already attached, use the one from the context or our default.
	if !hasParent(span) {
		// if the context has a parent, use it.
		ok, parent := GetParent(ctx)
		if ok {
			setParentOnSpan(span, parent)
			sp.logger.Debug("setting parent from context", "parent", parent.String())
		} else {
			// otherwise use the default parent
			span.SetAttributes(defaultParent)
			sp.logger.Debug("setting default parent", "parent", defaultParent.Value.AsString())
		}
	}

	// Set any other additional attributes (org name, app URL, etc.)
	span.SetAttributes(attrs...)

	// Delegate to wrapped processor
	sp.wrapped.OnStart(ctx, span)
}

func spanOriginContextJSON(span sdktrace.ReadOnlySpan, environment *SpanOriginEnvironment) string {
	origin := map[string]any{
		"span_origin": map[string]any{
			"name":            "braintrust.sdk.go",
			"version":         sdkVersion(),
			"instrumentation": map[string]any{"name": "braintrust-go"},
		},
	}
	for _, attr := range span.Attributes() {
		if attr.Key != contextJSONAttrKey {
			continue
		}
		var existing map[string]any
		if err := json.Unmarshal([]byte(attr.Value.AsString()), &existing); err == nil {
			for key, value := range existing {
				if key == "span_origin" {
					continue
				}
				origin[key] = value
			}
			if existingOrigin, ok := existing["span_origin"].(map[string]any); ok {
				spanOrigin, ok := origin["span_origin"].(map[string]any)
				if ok {
					for key, value := range existingOrigin {
						spanOrigin[key] = value
					}
				}
			}
		}
		break
	}
	if environment != nil {
		spanOrigin, ok := origin["span_origin"].(map[string]any)
		if !ok {
			return "{}"
		}
		if _, exists := spanOrigin["environment"]; !exists {
			env := map[string]any{"type": environment.Type}
			if environment.Name != "" {
				env["name"] = environment.Name
			}
			spanOrigin["environment"] = env
		}
	}
	data, err := json.Marshal(origin)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func sdkVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.Main.Path == "github.com/braintrustdata/braintrust-sdk-go" && isReleaseVersion(info.Main.Version) {
			return info.Main.Version
		}
		for _, dep := range info.Deps {
			if dep.Path == "github.com/braintrustdata/braintrust-sdk-go" && isReleaseVersion(dep.Version) {
				return dep.Version
			}
		}
	}
	return "unknown"
}

func isReleaseVersion(version string) bool {
	return version != "" && version != "(devel)"
}

func resolveEnvironment(explicit *SpanOriginEnvironment) *SpanOriginEnvironment {
	if explicit != nil {
		return explicit
	}
	if typ := envValue("BRAINTRUST_ENVIRONMENT_TYPE"); typ != "" {
		return &SpanOriginEnvironment{Type: typ, Name: envValue("BRAINTRUST_ENVIRONMENT_NAME")}
	}
	for key, name := range map[string]string{
		"GITHUB_ACTIONS": "github_actions", "GITLAB_CI": "gitlab_ci", "CIRCLECI": "circleci",
		"BUILDKITE": "buildkite", "JENKINS_URL": "jenkins", "JENKINS_HOME": "jenkins",
		"TF_BUILD": "azure_pipelines", "TEAMCITY_VERSION": "teamcity", "TRAVIS": "travis",
		"BITBUCKET_BUILD_NUMBER": "bitbucket",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return &SpanOriginEnvironment{Type: "ci", Name: name}
		}
	}
	if os.Getenv("CI") != "" {
		return &SpanOriginEnvironment{Type: "ci", Name: "ci"}
	}
	if name := detectServerEnvironmentName(); name != "" {
		return &SpanOriginEnvironment{Type: "server", Name: name}
	}
	return nil
}

func detectServerEnvironmentName() string {
	for key, name := range map[string]string{"VERCEL": "vercel", "NETLIFY": "netlify"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return name
		}
	}
	if strings.TrimSpace(os.Getenv("ECS_CONTAINER_METADATA_URI")) != "" ||
		strings.TrimSpace(os.Getenv("ECS_CONTAINER_METADATA_URI_V4")) != "" {
		return "ecs"
	}
	if value := strings.TrimSpace(os.Getenv("AWS_EXECUTION_ENV")); strings.HasPrefix(value, "AWS_ECS_") {
		return "ecs"
	} else if strings.HasPrefix(value, "AWS_Lambda_") {
		return "aws_lambda"
	}
	if strings.TrimSpace(os.Getenv("AWS_LAMBDA_FUNCTION_NAME")) != "" {
		return "aws_lambda"
	}
	for key, name := range map[string]string{
		"K_SERVICE": "cloud_run", "FUNCTION_TARGET": "gcp_functions", "KUBERNETES_SERVICE_HOST": "kubernetes",
		"DYNO": "heroku", "FLY_APP_NAME": "fly", "RAILWAY_ENVIRONMENT": "railway", "RENDER_SERVICE_NAME": "render",
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return name
		}
	}
	return ""
}

func envValue(key string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return braintrustEnvFileValue(key)
}

func braintrustEnvFileValue(key string) string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for depth := 0; depth <= 64; depth++ {
		envPath := filepath.Join(dir, ".env.braintrust")
		if data, err := os.ReadFile(envPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				name, value, ok := strings.Cut(line, "=")
				if !ok || strings.TrimSpace(name) != key {
					continue
				}
				return strings.Trim(strings.TrimSpace(value), `"'`)
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// OnEnd is called when a span ends.
func (sp *spanProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	// Apply filters to determine if we should forward this span
	if !sp.shouldForwardSpan(span) {
		return
	}

	// Process attachments if enabled.
	if sp.attachmentProcessor != nil {
		span = sp.processAttachments(span)
	}
	span = sp.addSpanOrigin(span)

	sp.wrapped.OnEnd(span)
}

func (sp *spanProcessor) addSpanOrigin(span sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	overrides := make(map[attribute.Key]string)
	overrides[contextJSONAttrKey] = spanOriginContextJSON(span, sp.environment)
	return attachmentprocessor.NewTransformedSpan(span, overrides)
}

// processAttachments scans input_json and output_json for base64 attachments,
// uploads them, and returns a transformed span with replacement references.
func (sp *spanProcessor) processAttachments(span sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	var inputJSON, outputJSON string
	for _, a := range span.Attributes() {
		switch a.Key {
		case inputJSONAttrKey:
			inputJSON = a.Value.AsString()
		case outputJSONAttrKey:
			outputJSON = a.Value.AsString()
		}
	}

	newInputJSON := sp.attachmentProcessor.ProcessAndUpload(inputJSON)
	newOutputJSON := sp.attachmentProcessor.ProcessAndUpload(outputJSON)

	if newInputJSON == inputJSON && newOutputJSON == outputJSON {
		return span
	}

	overrides := make(map[attribute.Key]string)
	if newInputJSON != inputJSON {
		overrides[inputJSONAttrKey] = newInputJSON
	}
	if newOutputJSON != outputJSON {
		overrides[outputJSONAttrKey] = newOutputJSON
	}
	return attachmentprocessor.NewTransformedSpan(span, overrides)
}

// shouldForwardSpan applies filter functions to determine if a span should be forwarded.
// Only rootFilters are applied to root spans. Filter functions are applied in
// order, with the first filters having priority.
func (sp *spanProcessor) shouldForwardSpan(span sdktrace.ReadOnlySpan) bool {
	// Always keep root spans (spans with no parent)
	filters := sp.filters
	if !span.Parent().IsValid() {
		filters = sp.rootFilters
	}

	// If no filters, keep everything
	if len(filters) == 0 {
		return true
	}
	for _, filter := range filters {
		result := filter(span)
		switch {
		case result > 0:
			return true
		case result < 0:
			return false
		case result == 0:
			continue
		}
	}
	return true
}

// Shutdown shuts down the span processor. It is bounded by ctx's deadline
// (if any) so a stuck uploader cannot block process exit beyond the caller's
// budget.
func (sp *spanProcessor) Shutdown(ctx context.Context) error {
	// Shut down the span exporter first so all buffered spans (including
	// those with attachment references) are flushed to the collector.
	err := sp.wrapped.Shutdown(ctx)

	if sp.attachmentUploader == nil {
		return err
	}

	// Shutdown drains any remaining queued uploads and exits, bounded by
	// the uploader's internal ShutdownTimeout (default 120s). Run it in a
	// goroutine so we can give up early if ctx expires.
	done := make(chan struct{})
	go func() {
		sp.attachmentUploader.Shutdown()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		sp.logger.Warn("attachment uploader shutdown abandoned: context done", "error", ctx.Err())
	}
	return err
}

// ForceFlush forces a flush of the span processor.
func (sp *spanProcessor) ForceFlush(ctx context.Context) error {
	err := sp.wrapped.ForceFlush(ctx)
	if sp.attachmentUploader != nil {
		sp.attachmentUploader.ForceFlush(timeoutFromContext(ctx, 30*time.Second))
	}
	return err
}

// timeoutFromContext returns the time remaining until ctx's deadline, or
// fallback if ctx has no deadline.
func timeoutFromContext(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
		return 0
	}
	return fallback
}

var _ sdktrace.SpanProcessor = &spanProcessor{}

func setParentOnSpan(span sdktrace.ReadWriteSpan, parent Parent) {
	span.SetAttributes(parent.Attr())
}

// getParent determines the default parent from the config
func getParent(cfg Config) Parent {
	// Figure out our default parent (defaulting to a reasonable value so users can still
	// see data flowing with no default project set)
	parentType := ParentTypeProjectName
	parentID := "go-otel-default-project"
	switch {
	case cfg.DefaultProjectID != "":
		parentType = ParentTypeProjectID
		parentID = cfg.DefaultProjectID
	case cfg.DefaultProjectName != "":
		parentType = ParentTypeProjectName
		parentID = cfg.DefaultProjectName
	}

	return Parent{Type: parentType, ID: parentID}
}

// getHTTPOtelOpts parses the URL and creates OTLP HTTP options with proper security settings
func getHTTPOtelOpts(fullURL, apiKey string) ([]otlptracehttp.Option, error) {
	// split url and protocol
	parts := strings.Split(fullURL, "://")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid url: %s", fullURL)
	}
	protocol := parts[0]
	urlWithoutProtocol := parts[1]

	otelOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(urlWithoutProtocol),
		otlptracehttp.WithURLPath("/otel/v1/traces"),
		otlptracehttp.WithHeaders(map[string]string{
			"Authorization": "Bearer " + apiKey,
		}),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	}

	if protocol == "http" {
		otelOpts = append(otelOpts, otlptracehttp.WithInsecure())
	}

	return otelOpts, nil
}

func hasParent(span sdktrace.ReadWriteSpan) bool {
	for _, attr := range span.Attributes() {
		if attr.Key == ParentOtelAttrKey {
			return true
		}
	}
	return false
}

// adkTracerScopeName is the instrumentation scope name used by Google ADK (adk-go).
// Spans from this scope are dropped when EnableBuiltinAdkTraces is false to avoid
// duplicates with Braintrust or custom instrumentation.
// https://github.com/google/adk-go/blob/main/internal/telemetry/telemetry.go
const adkTracerScopeName = "gcp.vertex.agent"

// adkSpanFilterFunc is a SpanFilterFunc that drops spans from Google ADK's built-in
// telemetry (instrumentation scope "gcp.vertex.agent")
func adkSpanFilterFunc(span sdktrace.ReadOnlySpan) int {
	if span.InstrumentationScope().Name == adkTracerScopeName {
		return -1 // Drop ADK native spans by scope
	}
	return 0
}

var aiOtelPrefixes = []string{
	"gen_ai.",
	"braintrust.",
	"llm.",
	"ai.",
	"traceloop.",
}

// aiSpanFilterFunc is a SpanFilterFunc that keeps AI spans, drops non-AI spans.
// Root spans get no opinion (0) so they are kept when all filters return 0.
func aiSpanFilterFunc(span sdktrace.ReadOnlySpan) int {
	if !span.Parent().IsValid() {
		return 0 // No opinion for root spans; they are kept by default
	}
	// Check span name for AI prefixes
	spanName := span.Name()
	for _, prefix := range aiOtelPrefixes {
		if strings.HasPrefix(spanName, prefix) {
			return 1 // Keep AI spans
		}
	}

	// Check attributes for AI prefixes (exclude system attributes we automatically add)
	for _, attr := range span.Attributes() {
		attrKey := string(attr.Key)
		// Skip system attributes that we automatically add to all spans
		if attrKey == ParentOtelAttrKey ||
			attrKey == orgAttrKey ||
			attrKey == appURLAttrKey ||
			attrKey == contextJSONAttrKey {
			continue
		}
		for _, prefix := range aiOtelPrefixes {
			if strings.HasPrefix(attrKey, prefix) {
				return 1 // Keep AI spans
			}
		}
	}

	// Drop non-AI spans
	return -1
}

// Permalink returns a URL to the span in the Braintrust UI.
func Permalink(span oteltrace.Span) (string, error) {
	appURL, org, parent, err := getSpanURLData(span)
	if err != nil {
		return "", err
	}

	// Get span context for trace and span IDs
	spanContext := span.SpanContext()
	traceID := spanContext.TraceID().String()
	spanID := spanContext.SpanID().String()

	// Build permalink
	u, err := url.Parse(appURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse app URL: %w", err)
	}

	// Different URL formats based on parent type
	// Projects: {app_url}/app/{org}/p/{project}/logs?r={trace_id}&s={span_id}
	// Experiments: {app_url}/app/{org}/p/{project}/experiments/{experiment_id}?r={trace_id}&s={span_id}
	if parent.Type == ParentTypeExperimentID {
		// For experiments, parent.ID format is "project-name/experiment-id"
		parts := strings.SplitN(parent.ID, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("experiment parent ID must be in format 'project/experiment-id', got: %s", parent.ID)
		}
		projectName := parts[0]
		experimentID := parts[1]
		u = u.JoinPath("app", org, "p", projectName, "experiments", experimentID)
	} else {
		u = u.JoinPath("app", org, "p", parent.ID, "logs")
	}

	q := u.Query()
	q.Set("r", traceID)
	q.Set("s", spanID)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

func getSpanURLData(span oteltrace.Span) (url, org string, parent Parent, err error) {
	// Check if it's a noop span (not recording)
	if !span.IsRecording() {
		url = "https://www.braintrust.dev"
		org = "unknown"
		parent = Parent{Type: ParentTypeProjectName, ID: "noop-span"}
		return
	}

	// Try ReadWriteSpan first (for live spans)
	var spanAttrs []attribute.KeyValue
	if readWriteSpan, ok := span.(sdktrace.ReadWriteSpan); ok {
		spanAttrs = readWriteSpan.Attributes()
	} else if readOnlySpan, ok := span.(sdktrace.ReadOnlySpan); ok {
		// Try ReadOnlySpan (for ended spans)
		spanAttrs = readOnlySpan.Attributes()
	} else {
		err = fmt.Errorf("span does not support attribute reading")
		return
	}

	attrs := make(map[string]string)
	for _, attr := range spanAttrs {
		attrs[string(attr.Key)] = attr.Value.AsString()
	}

	keys := []string{appURLAttrKey, orgAttrKey, ParentOtelAttrKey}
	for _, key := range keys {
		if _, ok := attrs[key]; !ok {
			err = fmt.Errorf("span missing %s attribute", key)
			return
		}
	}

	parent, err = parseParent(attrs[ParentOtelAttrKey])
	if err != nil {
		return
	}

	url = attrs[appURLAttrKey]
	org = attrs[orgAttrKey]
	return
}
