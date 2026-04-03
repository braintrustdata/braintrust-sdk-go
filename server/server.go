// Package server provides a remote eval HTTP server for the Braintrust SDK.
//
// The server exposes locally-registered evaluators over HTTP, allowing the
// Braintrust UI to trigger evaluations against code running on your infrastructure.
// Results are streamed back via Server-Sent Events (SSE).
//
// Example:
//
//	classify := &eval.Eval[string, string]{
//	    Name:    "classify",
//	    Task:    eval.T(classifyTask),
//	    Scorers: []eval.Scorer[string, string]{scorer},
//	}
//
//	srv := server.New(server.WithAddress(":8300"))
//	server.RegisterEval(srv, classify, server.RegisterEvalOpts{})
//	log.Fatal(srv.Start())
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

const (
	defaultAddr         = "localhost:8300"
	defaultAppURL       = "https://www.braintrust.dev"
	defaultAuthCacheMax = 64

	// defaultReadHeaderTimeout limits how long the server waits for request
	// headers after accepting a connection. Protects against slowloris attacks.
	// Does not affect eval duration — only the initial request setup.
	defaultReadHeaderTimeout = 10 * time.Second

	// defaultIdleTimeout limits how long idle keep-alive connections stay open
	// between requests. Does not affect active SSE streams.
	defaultIdleTimeout = 120 * time.Second
)

// Server is an HTTP server that exposes registered evaluators to the Braintrust UI.
type Server struct {
	evalsMu    sync.RWMutex
	evaluators map[string]registeredEval

	serverMu   sync.Mutex
	httpServer *http.Server

	logger         logger.Logger
	tracerProvider *sdktrace.TracerProvider // optional, user-provided
	addr           string
	appURL         string
	noAuth         bool
	authCache      *authCache
	defaultAuth    *authResult // used in no-auth mode, built from env config
}

// Option configures the server.
type Option func(*Server)

// WithAddress sets the listen address (default "localhost:8300").
func WithAddress(addr string) Option {
	return func(s *Server) {
		s.addr = addr
	}
}

// WithLogger sets a custom logger.
func WithLogger(l logger.Logger) Option {
	return func(s *Server) {
		s.logger = l
	}
}

// WithAppURL sets the Braintrust app URL for auth validation (default "https://www.braintrust.dev").
func WithAppURL(url string) Option {
	return func(s *Server) {
		s.appURL = url
	}
}

// WithTracerProvider sets a custom OpenTelemetry TracerProvider for the server.
// When provided, all eval spans flow through this provider, so user-instrumented
// code (LLM clients, custom spans, etc.) appears in the same trace as eval spans.
// When nil (the default), a per-request TracerProvider is created internally.
func WithTracerProvider(tp *sdktrace.TracerProvider) Option {
	return func(s *Server) {
		s.tracerProvider = tp
	}
}

// WithNoAuth disables authentication. Use only for local development.
func WithNoAuth() Option {
	return func(s *Server) {
		s.noAuth = true
	}
}

// New creates a new eval server.
func New(opts ...Option) *Server {
	s := &Server{
		evaluators: make(map[string]registeredEval),
		addr:       defaultAddr,
		appURL:     defaultAppURL,
		logger:     logger.NewDefaultLogger(),
	}

	for _, opt := range opts {
		opt(s)
	}

	s.authCache = newAuthCache(s.appURL, defaultAuthCacheMax, s.logger)

	// In no-auth mode, build a default auth result from environment config
	if s.noAuth {
		if err := s.initDefaultAuth(); err != nil {
			s.logger.Warn("no-auth mode: could not create default auth from env", "error", err)
		}
	}

	return s
}

// initDefaultAuth creates a default auth result from environment variables.
// This is used in no-auth mode so the server can run evaluations locally.
func (s *Server) initDefaultAuth() error {
	cfg := config.FromEnv()
	if cfg.APIKey == "" {
		return fmt.Errorf("BRAINTRUST_API_KEY is required for no-auth mode")
	}

	appURL := cfg.AppURL
	if appURL == "" {
		appURL = s.appURL
	}

	result, err := newAuthResult(context.Background(), cfg.APIKey, appURL, cfg.APIURL, cfg.OrgName, s.logger)
	if err != nil {
		return err
	}

	s.defaultAuth = result
	return nil
}

// Handler returns the server's http.Handler for embedding in a custom server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleHealth)
	mux.HandleFunc("GET /list", s.handleList)
	mux.HandleFunc("POST /list", s.handleList)
	mux.HandleFunc("POST /eval", s.handleEval)

	// Apply middleware: CORS → Auth → Router
	handler := s.authMiddleware(mux)
	handler = corsMiddleware(handler)
	return handler
}

// Start starts the HTTP server and blocks until it is shut down.
func (s *Server) Start() error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}

	s.serverMu.Lock()
	s.httpServer = srv
	s.serverMu.Unlock()

	s.logger.Info("eval server listening", "addr", s.addr, "no_auth", s.noAuth)
	return srv.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.serverMu.Lock()
	srv := s.httpServer
	s.serverMu.Unlock()

	// Drain active requests before closing the default session,
	// so in-flight evals using it can complete.
	var err error
	if srv != nil {
		err = srv.Shutdown(ctx)
	}

	if s.defaultAuth != nil {
		s.defaultAuth.session.Close()
	}

	return err
}

// handleHealth responds to GET / with a health check.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleList responds to GET/POST /list with registered evaluators.
func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	s.evalsMu.RLock()
	defer s.evalsMu.RUnlock()

	resp := make(listResponse, len(s.evaluators))
	for name, e := range s.evaluators {
		info := evalInfo{
			Scores: make([]scoreInfo, 0),
		}
		for _, sn := range e.scorerNames() {
			info.Scores = append(info.Scores, scoreInfo{Name: sn})
		}
		if params := e.parameters(); params != nil {
			wireSchema := make(map[string]wireParameterDef, len(params.Schema))
			for k, v := range params.Schema {
				wireSchema[k] = wireParameterDef{
					Type:        "data",
					Schema:      schemaField{Type: v.Type},
					Default:     v.Default,
					Description: v.Description,
				}
			}
			info.Parameters = &parametersMeta{
				Type:   "braintrust.staticParameters",
				Schema: wireSchema,
				Source: nil,
			}
		}
		resp[name] = info
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.Error("failed to encode list response", "error", err)
	}
}

// maxRequestBodyBytes limits the size of eval request bodies (10MB).
const maxRequestBodyBytes = 10 * 1024 * 1024

// handleEval handles POST /eval by running an evaluation and streaming SSE.
func (s *Server) handleEval(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var req EvalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, `{"error":"name is required"}`, http.StatusBadRequest)
		return
	}

	s.evalsMu.RLock()
	evaluator, ok := s.evaluators[req.Name]
	s.evalsMu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":"evaluator %q not found"}`, req.Name), http.StatusNotFound)
		return
	}

	// Get auth from context, or use default auth in no-auth mode
	ar := authFromContext(r.Context())
	if ar == nil && s.noAuth {
		ar = s.defaultAuth
	}
	if ar == nil {
		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
		return
	}

	// Create SSE writer
	sse, err := newSSEWriter(w)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	cfg := &evalRunConfig{
		req:            &req,
		auth:           ar,
		sse:            sse,
		noAuth:         s.noAuth,
		tracerProvider: s.tracerProvider,
	}

	// Run the evaluation
	if err := evaluator.run(r.Context(), cfg); err != nil {
		s.logger.Error("eval failed", "evaluator", req.Name, "error", err)
		// Evict cached session on auth errors so the next request gets a fresh login
		if isAuthError(err) && !s.noAuth {
			token := extractToken(r)
			orgName := extractOrgName(r)
			s.authCache.evict(token, orgName)
		}
		_ = sse.writeError(err)
	}

	_ = sse.writeDone()
}
