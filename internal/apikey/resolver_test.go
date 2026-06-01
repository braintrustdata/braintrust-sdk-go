package apikey

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBraintrustAPIKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		key   string
		found bool
	}{
		{
			name:  "api key string",
			input: `{"BRAINTRUST_API_KEY":"test-key"}`,
			key:   "test-key",
			found: true,
		},
		{
			name:  "trims whitespace",
			input: `{"BRAINTRUST_API_KEY":"  test-key  "}`,
			key:   "test-key",
			found: true,
		},
		{
			name: "ignores other keys",
			input: `{
				"OTHER_SECRET": "ignored",
				"BRAINTRUST_API_KEY": "test-key"
			}`,
			key:   "test-key",
			found: true,
		},
		{
			name:  "empty value is not found",
			input: `{"BRAINTRUST_API_KEY":""}`,
			found: false,
		},
		{
			name:  "whitespace value is not found",
			input: `{"BRAINTRUST_API_KEY":"   "}`,
			found: false,
		},
		{
			name:  "missing key is not found",
			input: `{"OTHER_SECRET":"ignored"}`,
			found: false,
		},
		{
			name:  "non-string key is not found",
			input: `{"BRAINTRUST_API_KEY":123}`,
			found: false,
		},
		{
			name:  "malformed json is not found",
			input: `{"BRAINTRUST_API_KEY":`,
			found: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key, found := parseBraintrustAPIKey([]byte(tt.input))

			assert.Equal(t, tt.found, found)
			assert.Equal(t, tt.key, key)
		})
	}
}

func TestLookupBraintrustConfigAPIKeyNearestWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	grandchild := filepath.Join(child, "grandchild")
	require.NoError(t, os.MkdirAll(grandchild, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"root-key"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(child, braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"child-key"}`), 0o600))

	key, found := lookupBraintrustConfigAPIKeyFromDir(grandchild)

	assert.True(t, found)
	assert.Equal(t, "child-key", key)
}

func TestLookupBraintrustConfigAPIKeyNearestFileIsBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"root-key"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(child, braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"   "}`), 0o600))

	key, found := lookupBraintrustConfigAPIKeyFromDir(child)

	assert.False(t, found)
	assert.Empty(t, key)
}

func TestLookupBraintrustConfigAPIKeyUnreadableNearestFileIsBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.MkdirAll(filepath.Join(child, braintrustConfigFilename), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"root-key"}`), 0o600))

	key, found := lookupBraintrustConfigAPIKeyFromDir(child)

	assert.False(t, found)
	assert.Empty(t, key)
}

func TestLookupBraintrustConfigAPIKeyDepthCap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	current := root
	for i := 0; i < 65; i++ {
		current = filepath.Join(current, fmt.Sprintf("d%d", i))
	}
	require.NoError(t, os.MkdirAll(current, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(root, braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"too-far"}`), 0o600))
	key, found := lookupBraintrustConfigAPIKeyFromDir(current)
	assert.False(t, found)
	assert.Empty(t, key)

	require.NoError(t, os.WriteFile(filepath.Join(root, "d0", braintrustConfigFilename), []byte(`{"BRAINTRUST_API_KEY":"in-range"}`), 0o600))
	key, found = lookupBraintrustConfigAPIKeyFromDir(current)
	assert.True(t, found)
	assert.Equal(t, "in-range", key)
}
