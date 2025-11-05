// Package auth provides authentication functionality for the Braintrust SDK.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

const (
	// TestAPIKey is a special API key used for testing
	TestAPIKey = "___TEST_API_KEY___"
	// DefaultAppURL is the default Braintrust app URL
	DefaultAppURL = "https://www.braintrust.dev"
)

// loginError wraps an error with HTTP status code information
type loginError struct {
	err        error
	statusCode int
}

func (e *loginError) Error() string {
	return e.err.Error()
}

func (e *loginError) Unwrap() error {
	return e.err
}

// Note: Caching is now handled by auth.Session per-client, not globally

// Options contains options for the Login function
type Options struct {
	// AppURL is the URL of the Braintrust app
	AppURL string
	// AppPublicURL is the public URL of the Braintrust app
	AppPublicURL string
	// APIURL is the URL of the Braintrust API
	APIURL string
	// APIKey is the API key to use
	APIKey string
	// OrgName is the name of a specific organization to connect to (optional)
	OrgName string
	// Logger is the logger to use (optional, defaults to noop logger)
	Logger logger.Logger
}

// Info holds authentication information
type Info struct {
	LoginToken   string
	OrgID        string
	OrgName      string
	APIKey       string
	APIURL       string
	ProxyURL     string
	AppURL       string
	AppPublicURL string
	LoggedIn     bool
}

// OrgInfo represents organization information from the login response
type OrgInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	APIURL   string `json:"api_url"`
	ProxyURL string `json:"proxy_url"`
}

// loginResponse represents the response from the login API
type loginResponse struct {
	OrgInfo []OrgInfo `json:"org_info"`
}

// makeLoginRequest makes an HTTP request to the login API and returns the parsed response.
func makeLoginRequest(ctx context.Context, appURL, apiKey string) (*loginResponse, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", appURL+"/api/apikey/login", nil)
	if err != nil {
		return nil, fmt.Errorf("error creating login request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making login request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		maskedKey := maskAPIKey(apiKey)
		return nil, &loginError{
			err:        fmt.Errorf("invalid API key %s: [%d]", maskedKey, resp.StatusCode),
			statusCode: resp.StatusCode,
		}
	}

	var loginResp loginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return nil, fmt.Errorf("error decoding login response: %w", err)
	}

	return &loginResp, nil
}

// login authenticates with the Braintrust API and returns login information.
// It implements the same logic as the Python SDK's login() function.
// Note: Caching is now handled by auth.Session, not by this function.
func login(ctx context.Context, apiKey, appURL, appPublicURL, orgName string, log logger.Logger) (*Info, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is required")
	}
	if appURL == "" {
		return nil, fmt.Errorf("app URL is required")
	}

	// Use discard logger if none provided
	if log == nil {
		log = logger.Discard()
	}

	if appPublicURL == "" {
		appPublicURL = appURL
	}

	log.Debug("Login: attempting login", "api_key", maskAPIKey(apiKey), "org", orgName, "app_url", appURL)

	result := &Info{
		AppURL:       appURL,
		AppPublicURL: appPublicURL,
		LoginToken:   apiKey,
		APIKey:       apiKey,
	}

	// Handle test API key
	if apiKey == TestAPIKey {
		testOrgInfo := []OrgInfo{
			{
				ID:       "test-org-id",
				Name:     getOrgNameOrDefault(orgName, "test-org-name"),
				APIURL:   "https://api.braintrust.ai",
				ProxyURL: "https://proxy.braintrust.ai",
			},
		}
		if err := selectOrg(result, testOrgInfo, orgName); err != nil {
			return nil, err
		}
		result.LoggedIn = true
		return result, nil
	}

	// Make API request to login
	loginResp, err := makeLoginRequest(ctx, appURL, apiKey)
	if err != nil {
		return nil, err
	}

	// Check and select organization
	if err := selectOrg(result, loginResp.OrgInfo, orgName); err != nil {
		return nil, err
	}

	result.LoggedIn = true
	log.Debug("Login: successfully logged in", "org_name", result.OrgName, "org_id", result.OrgID)
	return result, nil
}

// selectOrg selects the appropriate organization from the org_info list
func selectOrg(result *Info, orgInfo []OrgInfo, orgName string) error {
	if len(orgInfo) == 0 {
		return fmt.Errorf("this user is not part of any organizations")
	}

	// Find the matching org
	for _, org := range orgInfo {
		if orgName == "" || org.Name == orgName {
			result.OrgID = org.ID
			result.OrgName = org.Name
			result.APIURL = org.APIURL
			result.ProxyURL = org.ProxyURL
			return nil
		}
	}

	// If we get here, the org was not found
	orgNames := make([]string, len(orgInfo))
	for i, org := range orgInfo {
		orgNames[i] = org.Name
	}
	return fmt.Errorf("organization %q not found. Must be one of: %s", orgName, strings.Join(orgNames, ", "))
}

// getOrgNameOrDefault returns orgName if not empty, otherwise returns defaultName
func getOrgNameOrDefault(orgName, defaultName string) string {
	if orgName != "" {
		return orgName
	}
	return defaultName
}

// maskAPIKey masks an API key for safe display
func maskAPIKey(apiKey string) string {
	if len(apiKey) <= 6 {
		return "<redacted>"
	}
	return apiKey[:3] + "..." + apiKey[len(apiKey)-3:]
}

// Logout and GetState are deprecated - use auth.Session instead

// isRetryableError determines if an error should trigger a retry.
// Returns true for 5xx errors and network errors, false for 4xx errors and validation errors.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check if it's a loginError with status code
	var le *loginError
	if errors.As(err, &le) {
		// Retry on 5xx errors (server errors)
		if le.statusCode >= 500 && le.statusCode < 600 {
			return true
		}
		// Don't retry on 4xx errors (client errors)
		if le.statusCode >= 400 && le.statusCode < 500 {
			return false
		}
	}

	// Check for validation errors - don't retry
	if strings.Contains(err.Error(), "API key is required") ||
		strings.Contains(err.Error(), "app URL is required") ||
		strings.Contains(err.Error(), "organization") ||
		strings.Contains(err.Error(), "not part of any organizations") {
		return false
	}

	// Network errors, timeouts, and other errors - retry
	return true
}

// loginUntilSuccess attempts to login with exponential backoff retry.
// It retries indefinitely on 5xx errors and network errors, but returns immediately on 4xx errors.
// Backoff starts at 10ms and doubles each attempt, capped at 10 seconds.
// Returns early if context is cancelled.
func loginUntilSuccess(ctx context.Context, apiKey, appURL, appPublicURL, orgName string, log logger.Logger) (*Info, error) {
	// Use discard logger if none provided
	if log == nil {
		log = logger.Discard()
	}

	attempt := 0
	for {
		result, err := login(ctx, apiKey, appURL, appPublicURL, orgName, log)
		if err == nil {
			return result, nil
		}

		// Check if error is retryable
		if !isRetryableError(err) {
			log.Debug("loginUntilSuccess: non-retryable error", "error", err)
			return nil, err
		}

		// Calculate exponential backoff: 10ms * 2^attempt, capped at 10 seconds
		delay := time.Duration(10*math.Pow(2, float64(attempt))) * time.Millisecond
		maxDelay := 10 * time.Second
		if delay > maxDelay {
			delay = maxDelay
		}
		log.Debug("loginUntilSuccess: retrying after failure", "attempt", attempt+1, "error", err, "delay", delay)

		// Sleep with context cancellation
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
			// Continue to next attempt
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}

		attempt++
	}
}
