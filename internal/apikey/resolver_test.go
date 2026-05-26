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
			name:  "plain assignment",
			input: "BRAINTRUST_API_KEY=test-key\n",
			key:   "test-key",
			found: true,
		},
		{
			name:  "export assignment",
			input: "export BRAINTRUST_API_KEY=test-key\n",
			key:   "test-key",
			found: true,
		},
		{
			name:  "quoted assignment with comment",
			input: "BRAINTRUST_API_KEY=\"test-key\" # comment\n",
			key:   "test-key",
			found: true,
		},
		{
			name:  "single quoted assignment",
			input: "BRAINTRUST_API_KEY='test-key'\n",
			key:   "test-key",
			found: true,
		},
		{
			name: "ignores other variables",
			input: `OTHER_SECRET=ignored
BRAINTRUST_API_KEY=test-key
`,
			key:   "test-key",
			found: true,
		},
		{
			name:  "keeps hash without preceding whitespace",
			input: "BRAINTRUST_API_KEY=test#key\n",
			key:   "test#key",
			found: true,
		},
		{
			name:  "empty value is not found",
			input: "BRAINTRUST_API_KEY=\n",
			found: false,
		},
		{
			name:  "whitespace value is not found",
			input: "BRAINTRUST_API_KEY=   \n",
			found: false,
		},
		{
			name:  "comments only are not found",
			input: "# BRAINTRUST_API_KEY=ignored\n",
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

func TestLookupEnvBraintrustAPIKeyNearestWins(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	grandchild := filepath.Join(child, "grandchild")
	require.NoError(t, os.MkdirAll(grandchild, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, envBraintrustFilename), []byte("BRAINTRUST_API_KEY=root-key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(child, envBraintrustFilename), []byte("BRAINTRUST_API_KEY=child-key\n"), 0o600))

	key, found := lookupEnvBraintrustAPIKeyFromDir(grandchild)

	assert.True(t, found)
	assert.Equal(t, "child-key", key)
}

func TestLookupEnvBraintrustAPIKeyNearestFileIsBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.MkdirAll(child, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, envBraintrustFilename), []byte("BRAINTRUST_API_KEY=root-key\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(child, envBraintrustFilename), []byte("BRAINTRUST_API_KEY=   \n"), 0o600))

	key, found := lookupEnvBraintrustAPIKeyFromDir(child)

	assert.False(t, found)
	assert.Empty(t, key)
}

func TestLookupEnvBraintrustAPIKeyUnreadableNearestFileIsBoundary(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	child := filepath.Join(root, "child")
	require.NoError(t, os.MkdirAll(filepath.Join(child, envBraintrustFilename), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, envBraintrustFilename), []byte("BRAINTRUST_API_KEY=root-key\n"), 0o600))

	key, found := lookupEnvBraintrustAPIKeyFromDir(child)

	assert.False(t, found)
	assert.Empty(t, key)
}

func TestLookupEnvBraintrustAPIKeyDepthCap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	current := root
	for i := 0; i < 65; i++ {
		current = filepath.Join(current, fmt.Sprintf("d%d", i))
	}
	require.NoError(t, os.MkdirAll(current, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(root, envBraintrustFilename), []byte("BRAINTRUST_API_KEY=too-far\n"), 0o600))
	key, found := lookupEnvBraintrustAPIKeyFromDir(current)
	assert.False(t, found)
	assert.Empty(t, key)

	require.NoError(t, os.WriteFile(filepath.Join(root, "d0", envBraintrustFilename), []byte("BRAINTRUST_API_KEY=in-range\n"), 0o600))
	key, found = lookupEnvBraintrustAPIKeyFromDir(current)
	assert.True(t, found)
	assert.Equal(t, "in-range", key)
}
