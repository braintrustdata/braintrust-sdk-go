package attachment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONAttachmentOptions(t *testing.T) {
	options := JSONAttachmentOptions{}
	WithFilename("input.json")(&options)
	WithPrettyJSON()(&options)

	require.Equal(t, "input.json", options.Filename)
	require.True(t, options.Pretty)
}
