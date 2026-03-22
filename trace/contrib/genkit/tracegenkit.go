// Package genkit provides OpenTelemetry tracing middleware for Firebase Genkit.
//
// First, set up tracing with braintrust.New():
//
//	tp := trace.NewTracerProvider()
//	defer tp.Shutdown(context.Background())
//	otel.SetTracerProvider(tp)
//
//	bt, err := braintrust.New(tp,
//		braintrust.WithProject("my-project"),
//	)
//	if err != nil {
//		log.Fatal(err)
//	}
//
// Then wrap Genkit entrypoints with this package:
//
//	resp, err := tracegenkit.Generate(ctx, g,
//		ai.WithPrompt("Hello!"),
//	)
//
// NewMiddleware is still available when you need lower-level manual control.
package genkit

import (
	"context"
	"encoding/json"
	"iter"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
	"unsafe"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	genkitpkg "github.com/firebase/genkit/go/genkit"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/trace/internal"
)

type contextKey string

const (
	llmSpanStateKey     contextKey = "braintrust.genkit.llm_span_state"
	generateMetadataKey contextKey = "braintrust.genkit.generate_metadata"
)

type llmSpanState struct {
	mu    sync.Mutex
	depth int
}

type generateMetadata struct {
	model    string
	provider string
}

// Option configures the Genkit tracing middleware.
type Option func(*middlewareConfig)

// WithTracerProvider sets a custom TracerProvider for the middleware.
// If not provided, the global otel.GetTracerProvider() is used.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(cfg *middlewareConfig) {
		cfg.tracer = tp.Tracer("braintrust")
	}
}

// WithLogger sets a custom logger for the middleware.
// If not provided, logging is disabled.
func WithLogger(log logger.Logger) Option {
	return func(cfg *middlewareConfig) {
		if log != nil {
			cfg.logger = log
		}
	}
}

type middlewareConfig struct {
	logger logger.Logger
	tracer trace.Tracer
}

// NewMiddleware creates a new Genkit ModelMiddleware that traces model calls with
// Braintrust-compatible OpenTelemetry spans.
func NewMiddleware(opts ...Option) ai.ModelMiddleware {
	cfg := &middlewareConfig{
		logger: logger.Discard(),
		tracer: otel.GetTracerProvider().Tracer("braintrust"),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next ai.ModelFunc) ai.ModelFunc {
		return func(ctx context.Context, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			return traceGenerate(ctx, cfg, next, req, cb)
		}
	}
}

// Generate wraps genkit.Generate with Braintrust tracing.
func Generate(ctx context.Context, g *genkitpkg.Genkit, opts ...ai.GenerateOption) (*ai.ModelResponse, error) {
	ctx = withGenerateMetadata(ctx, g, opts)
	return genkitpkg.Generate(ctx, g, tracedGenerateOptions(opts)...)
}

// GenerateText wraps genkit.GenerateText with Braintrust tracing.
func GenerateText(ctx context.Context, g *genkitpkg.Genkit, opts ...ai.GenerateOption) (string, error) {
	ctx = withGenerateMetadata(ctx, g, opts)
	return genkitpkg.GenerateText(ctx, g, tracedGenerateOptions(opts)...)
}

// GenerateStream wraps genkit.GenerateStream with Braintrust tracing.
func GenerateStream(ctx context.Context, g *genkitpkg.Genkit, opts ...ai.GenerateOption) iter.Seq2[*ai.ModelStreamValue, error] {
	ctx = withGenerateMetadata(ctx, g, opts)
	return genkitpkg.GenerateStream(ctx, g, tracedGenerateOptions(opts)...)
}

func traceGenerate(ctx context.Context, cfg *middlewareConfig, next ai.ModelFunc, req *ai.ModelRequest, cb ai.ModelStreamCallback) (*ai.ModelResponse, error) {
	ctx, state := withLLMSpanState(ctx)
	if !state.enter() {
		defer state.leave()
		return next(ctx, req, cb)
	}
	defer state.leave()

	ctx, span := cfg.tracer.Start(ctx, "genkit.generate", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	setJSONAttr(cfg, span, "braintrust.span_attributes", map[string]string{"type": "llm"})
	if input := cleanupForInput(req); input != nil {
		setJSONAttr(cfg, span, "braintrust.input_json", input)
	}

	metadata := requestMetadata(ctx, req)

	var ttft time.Duration
	wrappedCB := cb
	if cb != nil {
		var once sync.Once
		startTime := time.Now()
		wrappedCB = func(ctx context.Context, chunk *ai.ModelResponseChunk) error {
			once.Do(func() {
				ttft = time.Since(startTime)
			})
			return cb(ctx, chunk)
		}
	}

	resp, err := next(ctx, req, wrappedCB)
	if err != nil {
		setJSONAttr(cfg, span, "braintrust.metadata", metadata)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	if output := cleanupForOutput(resp); output != nil {
		setJSONAttr(cfg, span, "braintrust.output_json", output)
	}

	if metrics := extractMetrics(resp, ttft); len(metrics) > 0 {
		setJSONAttr(cfg, span, "braintrust.metrics", metrics)
	}

	for key, value := range responseMetadata(resp) {
		metadata[key] = value
	}
	setJSONAttr(cfg, span, "braintrust.metadata", metadata)

	return resp, nil
}

func setJSONAttr(cfg *middlewareConfig, span trace.Span, key string, value any) {
	if err := internal.SetJSONAttr(span, key, value); err != nil {
		cfg.logger.Debug("Failed to set JSON span attribute", "key", key, "error", err)
	}
}

// cleanupForInput creates a clean input representation from the ModelRequest.
func cleanupForInput(req *ai.ModelRequest) any {
	if req == nil {
		return nil
	}

	input := map[string]any{}
	if len(req.Messages) > 0 {
		if messages := normalizeJSON(req.Messages); messages != nil {
			input["messages"] = messages
		}
	}
	if len(req.Docs) > 0 {
		if docs := normalizeJSON(req.Docs); docs != nil {
			input["docs"] = docs
		}
	}
	if len(req.Tools) > 0 {
		if tools := normalizeJSON(req.Tools); tools != nil {
			input["tools"] = tools
		}
	}
	if req.ToolChoice != "" {
		input["toolChoice"] = req.ToolChoice
	}
	if req.Output != nil {
		if output := normalizeJSON(req.Output); output != nil {
			input["output"] = output
		}
	}
	if config := normalizedConfig(req.Config); len(config) > 0 {
		input["config"] = config
	}

	return cleanupJSON(input)
}

// cleanupForOutput creates a clean output representation from the ModelResponse.
func cleanupForOutput(resp *ai.ModelResponse) any {
	if resp == nil {
		return nil
	}

	output := map[string]any{}
	if resp.Message != nil {
		if message := normalizeJSON(resp.Message); message != nil {
			output["message"] = message
		}
	}
	if resp.FinishReason != "" {
		output["finishReason"] = resp.FinishReason
	}
	if resp.FinishMessage != "" {
		output["finishMessage"] = resp.FinishMessage
	}
	if resp.Operation != nil {
		if operation := normalizeJSON(resp.Operation); operation != nil {
			output["operation"] = operation
		}
	}

	return cleanupJSON(output)
}

// extractMetrics extracts token usage metrics from the response.
func extractMetrics(resp *ai.ModelResponse, ttft time.Duration) map[string]float64 {
	metrics := make(map[string]float64)

	if resp != nil && resp.Usage != nil {
		usage := resp.Usage
		if usage.InputTokens > 0 {
			metrics["prompt_tokens"] = float64(usage.InputTokens)
		}
		if usage.OutputTokens > 0 {
			metrics["completion_tokens"] = float64(usage.OutputTokens)
		}
		if usage.TotalTokens > 0 {
			metrics["tokens"] = float64(usage.TotalTokens)
		}
		if usage.CachedContentTokens > 0 {
			metrics["prompt_cached_tokens"] = float64(usage.CachedContentTokens)
		}
		if usage.ThoughtsTokens > 0 {
			metrics["completion_reasoning_tokens"] = float64(usage.ThoughtsTokens)
		}
		for key, value := range usage.Custom {
			if value == 0 {
				continue
			}
			metricKey := snakeCase(key)
			if _, exists := metrics[metricKey]; !exists {
				metrics[metricKey] = value
			}
		}
		if _, exists := metrics["tokens"]; !exists {
			metrics["tokens"] = metrics["prompt_tokens"] + metrics["completion_tokens"]
			if metrics["tokens"] == 0 {
				delete(metrics, "tokens")
			}
		}
	}

	if ttft > 0 {
		metrics["time_to_first_token"] = ttft.Seconds()
	}

	return metrics
}

func requestMetadata(ctx context.Context, req *ai.ModelRequest) map[string]any {
	metadata := metadataFromContext(ctx)
	if req == nil {
		return metadata
	}

	if system := systemPrompt(req.Messages); system != "" {
		metadata["system"] = system
	}
	if req.ToolChoice != "" {
		metadata["tool_choice"] = req.ToolChoice
	} else if len(req.Tools) > 0 {
		metadata["tool_choice"] = ai.ToolChoiceAuto
	}
	if len(req.Tools) > 0 {
		if tools := normalizeJSON(req.Tools); tools != nil {
			metadata["tools"] = tools
		}
	}
	if req.Output != nil {
		if req.Output.Format != "" {
			metadata["response_format"] = req.Output.Format
		}
		if req.Output.ContentType != "" {
			metadata["content_type"] = req.Output.ContentType
		}
		if req.Output.Schema != nil {
			metadata["output_schema"] = req.Output.Schema
		}
		if req.Output.Constrained {
			metadata["output_constrained"] = true
		}
	}
	for key, value := range configMetadata(req.Config) {
		metadata[key] = value
	}

	return metadata
}

func responseMetadata(resp *ai.ModelResponse) map[string]any {
	metadata := map[string]any{}
	if resp == nil {
		return metadata
	}

	if resp.LatencyMs > 0 {
		metadata["latency_ms"] = resp.LatencyMs
	}

	custom := normalizeJSON(resp.Custom)
	customMap, ok := custom.(map[string]any)
	if !ok {
		return metadata
	}

	if model, ok := stringValue(customMap["model"]); ok {
		metadata["model"] = model
		if provider, ok := providerFromModel(model); ok {
			metadata["provider"] = provider
		}
	}
	if provider, ok := stringValue(customMap["provider"]); ok {
		metadata["provider"] = provider
	}
	if id, ok := stringValue(customMap["id"]); ok {
		metadata["id"] = id
	}
	if fingerprint, ok := stringValue(customMap["systemFingerprint"]); ok {
		metadata["system_fingerprint"] = fingerprint
	}

	return metadata
}

func tracedGenerateOptions(opts []ai.GenerateOption) []ai.GenerateOption {
	middleware := NewMiddleware()
	cloned := append([]ai.GenerateOption(nil), opts...)
	for i, opt := range cloned {
		if merged, ok := appendMiddleware(opt, middleware); ok {
			cloned[i] = merged
			return cloned
		}
	}
	return append(cloned, ai.WithMiddleware(middleware))
}

func appendMiddleware(opt ai.GenerateOption, middleware ai.ModelMiddleware) (ai.GenerateOption, bool) {
	cloned := cloneOption(opt)
	field := findField(cloned, "Middleware")
	if !field.IsValid() || field.Kind() != reflect.Slice {
		return nil, false
	}

	current, ok := field.Interface().([]ai.ModelMiddleware)
	if !ok {
		return nil, false
	}

	field.Set(reflect.ValueOf(append(current, middleware)))
	updated, ok := cloned.Interface().(ai.GenerateOption)
	return updated, ok
}

func withGenerateMetadata(ctx context.Context, g *genkitpkg.Genkit, opts []ai.GenerateOption) context.Context {
	metadata := resolveGenerateMetadata(g, opts)
	if metadata.model == "" && metadata.provider == "" {
		return ctx
	}
	return context.WithValue(ctx, generateMetadataKey, metadata)
}

func resolveGenerateMetadata(g *genkitpkg.Genkit, opts []ai.GenerateOption) generateMetadata {
	model := modelNameFromOptions(opts)
	if model == "" {
		model = defaultModelName(g)
	}
	if model == "" {
		return generateMetadata{}
	}

	provider, normalizedModel := normalizeModelRef(model)
	metadata := generateMetadata{model: normalizedModel}
	if provider != "" {
		metadata.provider = provider
	}
	return metadata
}

func modelNameFromOptions(opts []ai.GenerateOption) string {
	for _, opt := range opts {
		field := findField(reflect.ValueOf(opt), "Model")
		if !field.IsValid() || field.IsZero() {
			continue
		}
		model, ok := field.Interface().(ai.ModelArg)
		if ok && model != nil && model.Name() != "" {
			return model.Name()
		}
	}
	return ""
}

func defaultModelName(g *genkitpkg.Genkit) string {
	if g == nil {
		return ""
	}

	registry := findField(reflect.ValueOf(g), "reg")
	if !registry.IsValid() || registry.IsNil() {
		return ""
	}

	lookup := registry.MethodByName("LookupValue")
	if !lookup.IsValid() {
		return ""
	}

	results := lookup.Call([]reflect.Value{reflect.ValueOf(api.DefaultModelKey)})
	if len(results) != 1 {
		return ""
	}

	model, _ := results[0].Interface().(string)
	return model
}

func normalizeModelRef(model string) (string, string) {
	provider, rest, found := strings.Cut(model, "/")
	if !found || provider == "" || rest == "" {
		if normalizedProvider, ok := providerFromModel(model); ok {
			return normalizedProvider, model
		}
		return "", model
	}

	switch provider {
	case "google", "googleai", "gemini":
		provider = "gemini"
	}
	return provider, rest
}

func metadataFromContext(ctx context.Context) map[string]any {
	metadata, _ := ctx.Value(generateMetadataKey).(generateMetadata)
	result := map[string]any{}
	if metadata.provider != "" {
		result["provider"] = metadata.provider
	}
	if metadata.model != "" {
		result["model"] = metadata.model
	}
	return result
}

func withLLMSpanState(ctx context.Context) (context.Context, *llmSpanState) {
	if state, ok := ctx.Value(llmSpanStateKey).(*llmSpanState); ok && state != nil {
		return ctx, state
	}

	state := &llmSpanState{}
	return context.WithValue(ctx, llmSpanStateKey, state), state
}

func (s *llmSpanState) enter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	shouldTrace := s.depth == 0
	s.depth++
	return shouldTrace
}

func (s *llmSpanState) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.depth > 0 {
		s.depth--
	}
}

func configMetadata(config any) map[string]any {
	metadata := map[string]any{}
	if config == nil {
		return metadata
	}

	if provider, ok := providerFromConfig(config); ok {
		metadata["provider"] = provider
	}

	configMap := normalizedConfig(config)
	if len(configMap) == 0 {
		return metadata
	}

	if model, ok := firstString(configMap, "model", "model_name", "modelName"); ok {
		metadata["model"] = model
		if provider, ok := providerFromModel(model); ok {
			metadata["provider"] = provider
		}
	}
	if temperature, ok := firstFloat(configMap, "temperature", "Temperature"); ok {
		metadata["temperature"] = temperature
	}
	if topP, ok := firstFloat(configMap, "top_p", "topP", "TopP"); ok {
		metadata["top_p"] = topP
	}
	if topK, ok := firstInt(configMap, "top_k", "topK", "TopK"); ok {
		metadata["top_k"] = topK
	}
	if maxOutputTokens, ok := firstInt(configMap, "max_output_tokens", "maxOutputTokens", "max_completion_tokens", "maxCompletionTokens", "max_tokens", "maxTokens"); ok {
		metadata["max_output_tokens"] = maxOutputTokens
	}
	if stopSequences, ok := firstValue(configMap, "stop_sequences", "stopSequences", "stop", "Stop"); ok {
		metadata["stop_sequences"] = stopSequences
	}
	if responseFormat, ok := firstValue(configMap, "response_format", "responseFormat"); ok {
		metadata["response_format"] = responseFormat
	}
	if version, ok := firstString(configMap, "version", "Version"); ok {
		metadata["version"] = version
	}

	return metadata
}

func normalizedConfig(config any) map[string]any {
	configMap, ok := normalizeJSON(config).(map[string]any)
	if !ok || len(configMap) == 0 {
		return nil
	}

	delete(configMap, "apiKey")
	delete(configMap, "api_key")
	delete(configMap, "APIKey")

	return configMap
}

func normalizeJSON(value any) any {
	if value == nil {
		return nil
	}

	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}

	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}

	return result
}

func systemPrompt(messages []*ai.Message) string {
	var parts []string
	for _, msg := range messages {
		if msg == nil || msg.Role != ai.RoleSystem {
			continue
		}
		for _, part := range msg.Content {
			if part == nil || part.Text == "" {
				continue
			}
			parts = append(parts, part.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func providerFromConfig(config any) (string, bool) {
	configType := reflect.TypeOf(config)
	if configType == nil {
		return "", false
	}
	for configType.Kind() == reflect.Ptr {
		configType = configType.Elem()
	}

	pkg := configType.PkgPath()
	switch {
	case strings.Contains(pkg, "openai-go"):
		return "openai", true
	case strings.Contains(pkg, "anthropic-sdk-go"):
		return "anthropic", true
	case strings.Contains(pkg, "google.golang.org/genai"):
		return "gemini", true
	default:
		return "", false
	}
}

func providerFromModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}

	prefix, _, found := strings.Cut(model, "/")
	if !found || prefix == "" {
		return "", false
	}

	switch prefix {
	case "google", "googleai", "gemini":
		return "gemini", true
	default:
		return prefix, true
	}
}

func firstValue(m map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := m[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	value, ok := firstValue(m, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
}

func stringValue(value any) (string, bool) {
	str, ok := value.(string)
	if !ok || str == "" {
		return "", false
	}
	return str, true
}

func firstFloat(m map[string]any, keys ...string) (float64, bool) {
	value, ok := firstValue(m, keys...)
	if !ok {
		return 0, false
	}
	return toFloat(value)
}

func firstInt(m map[string]any, keys ...string) (int, bool) {
	value, ok := firstValue(m, keys...)
	if !ok {
		return 0, false
	}
	floatVal, ok := toFloat(value)
	if !ok {
		return 0, false
	}
	return int(floatVal), true
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// cleanupJSON recursively removes keys with empty values from JSON structures.
// Empty values include: nil, empty strings, empty slices, and empty maps.
func cleanupJSON(value any) any {
	switch val := value.(type) {
	case map[string]any:
		result := make(map[string]any)
		for k, v := range val {
			cleaned := cleanupJSON(v)
			if !isEmpty(cleaned) {
				result[k] = cleaned
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	case []any:
		result := make([]any, 0, len(val))
		for _, item := range val {
			cleaned := cleanupJSON(item)
			if !isEmpty(cleaned) {
				result = append(result, cleaned)
			}
		}
		if len(result) == 0 {
			return nil
		}
		return result
	default:
		return val
	}
}

// isEmpty checks if a value is considered empty.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case string:
		return val == ""
	case []any:
		return len(val) == 0
	case map[string]any:
		return len(val) == 0
	default:
		return false
	}
}

func snakeCase(value string) string {
	if value == "" {
		return value
	}

	var b strings.Builder
	lastLowerOrDigit := false
	for i, r := range value {
		if unicode.IsUpper(r) {
			if i > 0 && lastLowerOrDigit {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
			lastLowerOrDigit = false
			continue
		}
		if r == '-' || r == ' ' {
			b.WriteByte('_')
			lastLowerOrDigit = false
			continue
		}
		b.WriteRune(r)
		lastLowerOrDigit = unicode.IsLower(r) || unicode.IsDigit(r)
	}
	return b.String()
}

func findField(value reflect.Value, name string) reflect.Value {
	value = exposeValue(value)
	if !value.IsValid() {
		return reflect.Value{}
	}

	for value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = exposeValue(value.Elem())
	}

	if value.Kind() != reflect.Struct {
		return reflect.Value{}
	}

	if field := value.FieldByName(name); field.IsValid() {
		return exposeValue(field)
	}

	for i := 0; i < value.NumField(); i++ {
		field := exposeValue(value.Field(i))
		switch field.Kind() {
		case reflect.Struct, reflect.Ptr:
			if nested := findField(field, name); nested.IsValid() {
				return nested
			}
		}
	}

	return reflect.Value{}
}

func exposeValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	if value.CanInterface() || !value.CanAddr() {
		return value
	}
	return reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()
}

func cloneOption(opt ai.GenerateOption) reflect.Value {
	value := exposeValue(reflect.ValueOf(opt))
	if !value.IsValid() {
		return reflect.Value{}
	}
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Elem().Type())
		cloned.Elem().Set(exposeValue(value.Elem()))
		return cloned
	}
	cloned := reflect.New(value.Type()).Elem()
	cloned.Set(value)
	return cloned
}
