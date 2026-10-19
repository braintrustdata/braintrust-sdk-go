package evalrunner

import (
	"context"
	"fmt"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/config"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// session holds the authenticated Braintrust context for this process.
//
// Under bt there is no auth code to write: bt validates the browser's token
// before spawning us and passes it through as BRAINTRUST_API_KEY, so we simply
// read the environment. The token belongs to the person who clicked Run, which
// is what attributes the resulting experiment to them.
type session struct {
	session *auth.Session
	api     *api.API
}

// newSession logs in and builds an API client.
func newSession(ctx context.Context, apiKey, appURL, apiURL, orgName string, log logger.Logger) (*session, error) {
	httpClient := https.NewClient(apiKey, appURL, log)
	authSession, err := auth.NewSession(ctx, auth.Options{
		APIKey:       apiKey,
		AppURL:       appURL,
		AppPublicURL: appURL,
		APIURL:       apiURL,
		OrgName:      orgName,
		Logger:       log,
		Client:       httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	if err := authSession.Login(ctx); err != nil {
		authSession.Close()
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	apiInfo := authSession.APIInfo()
	apiClient := api.NewClient(apiInfo.APIKey, api.WithAPIURL(apiInfo.APIURL), api.WithLogger(log))

	return &session{session: authSession, api: apiClient}, nil
}

// newSessionFromEnv builds a session from the environment bt gave us.
//
// bt sets BRAINTRUST_API_KEY to the caller's own token and BRAINTRUST_APP_URL /
// BRAINTRUST_API_URL / BRAINTRUST_ORG_NAME alongside it. Outside bt these come
// from the user's own environment, so a standalone run works the same way.
func newSessionFromEnv(ctx context.Context, log logger.Logger) (*session, error) {
	cfg := config.FromEnv()
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("BRAINTRUST_API_KEY is required to run evals")
	}

	appURL := cfg.AppURL
	if appURL == "" {
		appURL = defaultAppURL
	}

	return newSession(ctx, cfg.APIKey, appURL, cfg.APIURL, cfg.OrgName, log)
}

// authSession and apiClient tolerate a nil session, which is what a
// --no-send-logs run has.
func (s *session) authSession() *auth.Session {
	if s == nil {
		return nil
	}
	return s.session
}

func (s *session) apiClient() *api.API {
	if s == nil {
		return nil
	}
	return s.api
}

func (s *session) Close() {
	if s != nil && s.session != nil {
		s.session.Close()
	}
}
