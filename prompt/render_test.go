package prompt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A prompt template is remote input: it comes from Braintrust, and under a
// playground run whoever edits the prompt chooses its text. A partial tag pulls
// content in from outside the template, so it is rejected outright rather than
// resolved -- upstream cbroglie/mustache resolved partials against $CWD, which
// turned "{{> /etc/passwd }}" into local file disclosure. Rejecting rather than
// ignoring means a prompt that uses partials is reported, not silently altered.
// See internal/mustache/VENDOR.md.
func TestBuild_PartialsAreRejected(t *testing.T) {
	for _, template := range []string{
		"{{>partial}}",
		"{{> /etc/passwd }}",
		"{{> ../../go.mod }}",
	} {
		p := FromData("p", Data{
			Prompt:  &Block{Type: BlockCompletion, Content: "[" + template + "]"},
			Options: &Options{Model: "m"},
		})

		_, err := p.Build(nil)
		require.Error(t, err, "partial %q must be rejected, not resolved", template)
		assert.Contains(t, err.Error(), "partial")
	}
}
