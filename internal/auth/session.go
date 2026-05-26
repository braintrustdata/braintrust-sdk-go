package auth

import (
	"context"
	"fmt"
	"sync"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// APIInfo contains API credentials and endpoint information.
type APIInfo struct {
	APIKey string
	APIURL string
}

// Org contains organization information.
type Org struct {
	ID   string
	Name string
}

// Session manages authentication and login state.
type Session struct {
	mu     sync.RWMutex
	result *loginResult // Server response data
	err    error
	done   chan struct{}
	logger logger.Logger
	ctx    context.Context
	cancel context.CancelFunc
	opts   Options // Store original options for access before login completes
	apiKey string
}

// NewSession creates a session and starts login with retry in the background.
// Returns an error if required fields (APIKey, AppURL) are missing.
// The context is used for the background login goroutine.
// If opts.Logger is nil, a noop logger is used.
// If opts.Client is nil, a default client will be created.
func NewSession(ctx context.Context, opts Options) (*Session, error) {
	if opts.APIKey == "" && opts.APIKeyResolver == nil {
		return nil, fmt.Errorf("API key is required")
	}
	if opts.AppURL == "" {
		return nil, fmt.Errorf("app URL is required")
	}

	// Use discard logger if none provided
	log := opts.Logger
	if log == nil {
		log = logger.Discard()
	}

	// Create default client if none provided
	if opts.Client == nil && opts.APIKey != "" {
		opts.Client = https.NewClient(opts.APIKey, opts.AppURL, log)
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &Session{
		logger: log,
		done:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		opts:   opts,
		apiKey: opts.APIKey,
	}
	go s.loginWithRetry(opts)
	return s, nil
}

// Close cancels the background login goroutine.
func (s *Session) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

// OrgName returns the organization name if available.
// Returns empty string if login hasn't completed yet.
func (s *Session) OrgName() string {
	org := s.OrgInfo()
	return org.Name
}

// OrgInfo returns the organization ID and name from the server response.
// Returns empty strings if login hasn't completed yet.
func (s *Session) OrgInfo() Org {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.result != nil {
		return Org{
			ID:   s.result.OrgID,
			Name: s.result.OrgName,
		}
	}

	return Org{}
}

// APIInfo returns the API key and API URL.
// API key comes from config, API URL comes from server response (or default).
// Always available - falls back to config/defaults if login not complete.
func (s *Session) APIInfo() APIInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	apiKey := s.apiKey
	apiURL := s.opts.APIURL

	if s.result != nil {
		apiURL = s.result.APIURL
	}

	if apiURL == "" {
		apiURL = "https://api.braintrust.dev"
	}

	return APIInfo{
		APIKey: apiKey,
		APIURL: apiURL,
	}
}

// ResolveAPIKey returns the configured API key or waits for the lazy resolver.
func (s *Session) ResolveAPIKey(ctx context.Context) (string, bool) {
	s.mu.RLock()
	if s.apiKey != "" {
		apiKey := s.apiKey
		s.mu.RUnlock()
		return apiKey, true
	}
	resolver := s.opts.APIKeyResolver
	s.mu.RUnlock()

	if resolver == nil {
		return "", false
	}

	apiKey, ok := resolver.APIKey(ctx)
	if !ok || apiKey == "" {
		return "", false
	}

	s.mu.Lock()
	if s.apiKey == "" {
		s.apiKey = apiKey
		s.opts.APIKey = apiKey
	}
	apiKey = s.apiKey
	s.mu.Unlock()
	return apiKey, true
}

// AppPublicURL returns the public app URL from config.
// Always available.
func (s *Session) AppPublicURL() string {
	return s.opts.AppPublicURL
}

// Login blocks until login completes or context is cancelled.
// Returns error if login failed.
func (s *Session) Login(ctx context.Context) error {
	select {
	case <-s.done:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Session) loginWithRetry(opts Options) {
	defer close(s.done)

	s.logger.Debug("starting login with retry")

	apiKey, ok := s.ResolveAPIKey(s.ctx)
	if !ok {
		err := fmt.Errorf("API key is required")
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		s.logger.Warn("login failed", "error", err)
		return
	}
	opts.APIKey = apiKey
	if opts.Client == nil {
		opts.Client = https.NewClient(apiKey, opts.AppURL, s.logger)
	}

	// Use loginUntilSuccess which retries on network/5xx errors
	result, err := loginUntilSuccess(s.ctx, opts.Client, opts.OrgName)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.err = err
		s.logger.Warn("login failed", "error", err)
		return
	}

	s.result = result
	s.logger.Debug("login successful",
		"org_name", s.result.OrgName,
		"org_id", s.result.OrgID)
}

// NewTestSession creates a static test session with hardcoded data.
// This is for use in test packages outside of internal/auth to avoid import cycles.
// This session does not make any network calls or start goroutines.
func NewTestSession(apiKey, orgID, orgName, apiURL, appURL, appPublicURL string, log logger.Logger) *Session {
	done := make(chan struct{})
	close(done)
	return &Session{
		result: &loginResult{
			OrgID:    orgID,
			OrgName:  orgName,
			APIURL:   apiURL,
			ProxyURL: apiURL, // Use same as APIURL for test
		},
		err:    nil,
		done:   done,
		logger: log,
		opts: Options{
			APIKey:       apiKey,
			AppURL:       appURL,
			AppPublicURL: appPublicURL,
			APIURL:       apiURL,
		},
		apiKey: apiKey,
	}
}
