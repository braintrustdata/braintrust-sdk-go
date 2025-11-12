package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFromEnv_Defaults(t *testing.T) {
	// Clear all env vars
	t.Setenv("BRAINTRUST_API_KEY", "")
	t.Setenv("BRAINTRUST_API_URL", "")
	t.Setenv("BRAINTRUST_APP_URL", "")
	t.Setenv("BRAINTRUST_ORG_NAME", "")
	t.Setenv("BRAINTRUST_DEFAULT_PROJECT_ID", "")
	t.Setenv("BRAINTRUST_DEFAULT_PROJECT", "")
	t.Setenv("BRAINTRUST_BLOCKING_LOGIN", "")
	t.Setenv("BRAINTRUST_OTEL_FILTER_AI_SPANS", "")

	cfg := FromEnv()

	assert.Equal(t, "", cfg.APIKey)
	assert.Equal(t, "https://api.braintrust.dev", cfg.APIURL)
	assert.Equal(t, "https://www.braintrust.dev", cfg.AppURL)
	assert.Equal(t, "", cfg.OrgName)
	assert.Equal(t, "", cfg.DefaultProjectID)
	assert.Equal(t, "default-go-project", cfg.DefaultProjectName)
	assert.False(t, cfg.BlockingLogin)
	assert.False(t, cfg.FilterAISpans)
}

func TestFromEnv_LoadsEnvironmentVariables(t *testing.T) {
	// Set all env vars
	t.Setenv("BRAINTRUST_API_KEY", "test-api-key")
	t.Setenv("BRAINTRUST_API_URL", "https://custom-api.example.com")
	t.Setenv("BRAINTRUST_APP_URL", "https://custom-app.example.com")
	t.Setenv("BRAINTRUST_ORG_NAME", "test-org")
	t.Setenv("BRAINTRUST_DEFAULT_PROJECT_ID", "proj-123")
	t.Setenv("BRAINTRUST_DEFAULT_PROJECT", "my-project")
	t.Setenv("BRAINTRUST_BLOCKING_LOGIN", "true")
	t.Setenv("BRAINTRUST_OTEL_FILTER_AI_SPANS", "true")

	cfg := FromEnv()

	assert.Equal(t, "test-api-key", cfg.APIKey)
	assert.Equal(t, "https://custom-api.example.com", cfg.APIURL)
	assert.Equal(t, "https://custom-app.example.com", cfg.AppURL)
	assert.Equal(t, "test-org", cfg.OrgName)
	assert.Equal(t, "proj-123", cfg.DefaultProjectID)
	assert.Equal(t, "my-project", cfg.DefaultProjectName)
	assert.True(t, cfg.BlockingLogin)
	assert.True(t, cfg.FilterAISpans)
}

func TestFromEnv_TrimsWhitespace(t *testing.T) {
	t.Setenv("BRAINTRUST_API_KEY", "  test-key-with-spaces  ")
	t.Setenv("BRAINTRUST_ORG_NAME", "\ttest-org\t")
	t.Setenv("BRAINTRUST_DEFAULT_PROJECT", " my-project ")

	cfg := FromEnv()

	assert.Equal(t, "test-key-with-spaces", cfg.APIKey)
	assert.Equal(t, "test-org", cfg.OrgName)
	assert.Equal(t, "my-project", cfg.DefaultProjectName)
}

func TestFromEnv_BooleanParsing(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected bool
	}{
		{"true lowercase", "true", true},
		{"TRUE uppercase", "TRUE", true},
		{"True mixed case", "True", true},
		{"false lowercase", "false", false},
		{"FALSE uppercase", "FALSE", false},
		{"empty string", "", false},
		{"random string", "yes", false},
		{"1", "1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BRAINTRUST_BLOCKING_LOGIN", tt.envValue)

			cfg := FromEnv()

			assert.Equal(t, tt.expected, cfg.BlockingLogin, "BlockingLogin should be %v for input %q", tt.expected, tt.envValue)
		})
	}
}

func TestConfig_IsValid(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantErr   bool
		errString string
	}{
		{
			name: "valid config",
			config: &Config{
				APIKey: "test-key",
				APIURL: "https://api.braintrust.dev",
				AppURL: "https://www.braintrust.dev",
			},
			wantErr: false,
		},
		{
			name: "missing API key",
			config: &Config{
				APIKey: "",
				APIURL: "https://api.braintrust.dev",
				AppURL: "https://www.braintrust.dev",
			},
			wantErr:   true,
			errString: "API key is required",
		},
		{
			name: "missing API URL",
			config: &Config{
				APIKey: "test-key",
				APIURL: "",
				AppURL: "https://www.braintrust.dev",
			},
			wantErr:   true,
			errString: "API URL is required",
		},
		{
			name: "missing App URL",
			config: &Config{
				APIKey: "test-key",
				APIURL: "https://api.braintrust.dev",
				AppURL: "",
			},
			wantErr:   true,
			errString: "app URL is required",
		},
		{
			name: "all fields missing",
			config: &Config{
				APIKey: "",
				APIURL: "",
				AppURL: "",
			},
			wantErr:   true,
			errString: "API key is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.IsValid()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errString)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
