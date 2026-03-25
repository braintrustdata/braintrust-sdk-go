// Package server provides a remote eval HTTP server for the Braintrust SDK.
//
// The server exposes locally-registered evaluators over HTTP, allowing the
// Braintrust UI to trigger evaluations against code running on your infrastructure.
// Results are streamed back via Server-Sent Events (SSE).
//
// Example:
//
//	srv := server.New(server.WithAddress(":8300"))
//
//	server.Register(srv, "classify",
//	    eval.T(classifyTask),
//	    []eval.Scorer[string, string]{scorer},
//	)
//
//	log.Fatal(srv.Start())
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

const (
	defaultAddr         = "localhost:8300"
	defaultAppURL       = "https://www.braintrust.dev"
	defaultAuthCacheMax = 64
)

// Server is an HTTP server that exposes registered evaluators to the Braintrust UI.
type Server struct {
	evaluators  map[string]registeredEval
	mu          sync.RWMutex
	serverMu    sync.Mutex // protects httpServer
	httpServer  *http.Server
	logger      logger.Logger
	addr        string
	appURL      string
	noAuth      bool
	authCache   *authCache
	defaultAuth *authResult // used in no-auth mode, built from env config
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

	httpClient := https.NewClient(cfg.APIKey, appURL, s.logger)
	session, err := auth.NewSession(context.Background(), auth.Options{
		APIKey:       cfg.APIKey,
		AppURL:       appURL,
		AppPublicURL: appURL,
		APIURL:       cfg.APIURL,
		OrgName:      cfg.OrgName,
		Logger:       s.logger,
		Client:       httpClient,
	})
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}

	// Block until login completes
	if err := session.Login(context.Background()); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	apiInfo := session.APIInfo()
	apiClient := api.NewClient(apiInfo.APIKey, api.WithAPIURL(apiInfo.APIURL), api.WithLogger(s.logger))

	s.defaultAuth = &authResult{session: session, api: apiClient}
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
		Addr:    s.addr,
		Handler: s.Handler(),
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

	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// handleHealth responds to GET / with a health check.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleList responds to GET/POST /list with registered evaluators.
func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	resp := make(listResponse, len(s.evaluators))
	for name, e := range s.evaluators {
		info := evalInfo{
			Scores: make([]scoreInfo, 0),
		}
		for _, sn := range e.scorerNames() {
			info.Scores = append(info.Scores, scoreInfo{Name: sn})
		}
		if params := e.parameters(); params != nil {
			info.Parameters = &parametersMeta{
				Type:   "braintrust.staticParameters",
				Schema: params.Schema,
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

	s.mu.RLock()
	evaluator, ok := s.evaluators[req.Name]
	s.mu.RUnlock()

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
		req:    &req,
		auth:   ar,
		sse:    sse,
		noAuth: s.noAuth,
	}

	// Run the evaluation
	if err := evaluator.run(r.Context(), cfg); err != nil {
		s.logger.Error("eval failed", "evaluator", req.Name, "error", err)
		_ = sse.writeError(err)
	}

	_ = sse.writeDone()
}
