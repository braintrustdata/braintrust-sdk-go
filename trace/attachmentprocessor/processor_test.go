package attachmentprocessor

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 1×1 red PNG pixel, valid base64.
const base64PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8DwHwAFBQIAX8jx0gAAAABJRU5ErkJggg=="

// Fake base64 content standing in for a PDF document.
const base64PDF = "JVBERi0xLjQKMSAwIG9iago="

// formatTestCase is a single parameterised test entry. Every Format in
// Formats MUST have at least one test case. The TestAllFormatsHaveTestCases
// test below enforces this.
type formatTestCase struct {
	name       string
	format     string // must match Format.Name
	inputJSON  string
	assertions func(t *testing.T, root any)
}

var formatTestCases = []formatTestCase{
	// ── OpenAI ──────────────────────────────────────────────────────
	{
		name:   "openai-image",
		format: "openai",
		inputJSON: `[{"role":"user","content":[{"type":"text","text":"describe this"},` +
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}}]}]`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			arr := root.([]any)
			content := arr[0].(map[string]any)["content"].([]any)
			imageURL := content[1].(map[string]any)["image_url"].(map[string]any)
			url := imageURL["url"]
			assertAttachmentRef(t, url, "image/png")
		},
	},
	// ── Bedrock image ───────────────────────────────────────────────
	{
		name:   "bedrock-image",
		format: "bedrock",
		inputJSON: `[{"role":"user","content":[{"type":"text","text":"describe this"},` +
			`{"type":"image","image":{"format":"png","source":{"bytes":"` + base64PNG + `"}}}]}]`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			arr := root.([]any)
			content := arr[0].(map[string]any)["content"].([]any)
			image := content[1].(map[string]any)["image"].(map[string]any)
			assert.Equal(t, "png", image["format"])
			source := image["source"].(map[string]any)
			assertAttachmentRef(t, source["bytes"], "image/png")
		},
	},
	// ── Bedrock document ────────────────────────────────────────────
	{
		name:   "bedrock-document",
		format: "bedrock",
		inputJSON: `[{"role":"user","content":[{"type":"text","text":"summarize this"},` +
			`{"type":"document","document":{"format":"pdf","name":"report","source":{"bytes":"` + base64PDF + `"}}}]}]`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			arr := root.([]any)
			content := arr[0].(map[string]any)["content"].([]any)
			doc := content[1].(map[string]any)["document"].(map[string]any)
			assert.Equal(t, "pdf", doc["format"])
			assert.Equal(t, "report", doc["name"])
			source := doc["source"].(map[string]any)
			assertAttachmentRef(t, source["bytes"], "application/pdf")
		},
	},
	// ── Bedrock audio (ambiguous mp4 → audio/mp4, not video/mp4) ──
	{
		name:   "bedrock-audio",
		format: "bedrock",
		inputJSON: `[{"role":"user","content":[{"type":"text","text":"transcribe this"},` +
			`{"type":"audio","audio":{"format":"mp4","source":{"bytes":"` + base64PDF + `"}}}]}]`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			arr := root.([]any)
			content := arr[0].(map[string]any)["content"].([]any)
			audio := content[1].(map[string]any)["audio"].(map[string]any)
			assert.Equal(t, "mp4", audio["format"])
			source := audio["source"].(map[string]any)
			assertAttachmentRef(t, source["bytes"], "audio/mp4")
		},
	},
	// ── Anthropic image ─────────────────────────────────────────────
	{
		name:   "anthropic-image",
		format: "anthropic",
		inputJSON: `[{"role":"user","content":[{"type":"text","text":"describe this"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64PNG + `"}}]}]`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			arr := root.([]any)
			content := arr[0].(map[string]any)["content"].([]any)
			source := content[1].(map[string]any)["source"]
			assertAttachmentRef(t, source, "image/png")
		},
	},
	// ── Anthropic document ──────────────────────────────────────────
	{
		name:   "anthropic-document",
		format: "anthropic",
		inputJSON: `[{"role":"user","content":[{"type":"text","text":"summarize this"},` +
			`{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + base64PDF + `"}}]}]`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			arr := root.([]any)
			content := arr[0].(map[string]any)["content"].([]any)
			source := content[1].(map[string]any)["source"]
			assertAttachmentRef(t, source, "application/pdf")
		},
	},
	// ── Gemini image ────────────────────────────────────────────────
	{
		name:   "gemini-image",
		format: "gemini",
		inputJSON: `{"contents":[{"role":"user","parts":[{"text":"describe this"},` +
			`{"inlineData":{"mimeType":"image/png","data":"` + base64PNG + `"}}]}]}`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			obj := root.(map[string]any)
			contents := obj["contents"].([]any)
			parts := contents[0].(map[string]any)["parts"].([]any)
			part := parts[1].(map[string]any)
			assert.Nil(t, part["inlineData"], "inlineData should be removed")
			imageURL := part["image_url"].(map[string]any)
			assertAttachmentRef(t, imageURL["url"], "image/png")
		},
	},
	// ── Gemini document (non-image → file.file_data) ────────────────
	{
		name:   "gemini-document",
		format: "gemini",
		inputJSON: `{"contents":[{"role":"user","parts":[{"text":"summarize this"},` +
			`{"inlineData":{"mimeType":"application/pdf","data":"` + base64PDF + `"}}]}]}`,
		assertions: func(t *testing.T, root any) {
			t.Helper()
			obj := root.(map[string]any)
			contents := obj["contents"].([]any)
			parts := contents[0].(map[string]any)["parts"].([]any)
			part := parts[1].(map[string]any)
			assert.Nil(t, part["inlineData"], "inlineData should be removed")
			assert.Nil(t, part["image_url"], "non-image should not use image_url")
			file := part["file"].(map[string]any)
			assertAttachmentRef(t, file["file_data"], "application/pdf")
		},
	},
}

func TestAttachmentFormatReplacesBase64WithRef(t *testing.T) {
	for _, tc := range formatTestCases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify heuristic matches.
			heuristic := BuildHeuristic(Formats)
			assert.True(t, heuristic.MatchString(tc.inputJSON),
				"BASE64_HEURISTIC should match test data for %s", tc.name)

			// Run processor.
			p := NewProcessor(&NoopUploader{}, nil)
			result := p.ProcessAndUpload(tc.inputJSON)
			require.NotEqual(t, tc.inputJSON, result,
				"base64 data should have been replaced for %s", tc.name)

			var root any
			require.NoError(t, json.Unmarshal([]byte(result), &root))
			tc.assertions(t, root)
		})
	}
}

// TestAllFormatsHaveTestCases ensures that adding a new format without test
// data causes a test failure (as required by the spec).
func TestAllFormatsHaveTestCases(t *testing.T) {
	covered := make(map[string]bool)
	for _, tc := range formatTestCases {
		covered[tc.format] = true
	}
	for _, f := range Formats {
		assert.True(t, covered[f.Name],
			"format %q has no test cases in formatTestCases — add at least one", f.Name)
	}
}

// ── Negative cases ─────────────────────────────────────────────────

func TestNonDataURIInputIsUnchanged(t *testing.T) {
	inputJSON := `[{"role":"user","content":"Hello, how are you?"}]`
	p := NewProcessor(&NoopUploader{}, nil)
	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result)
}

func TestPartialDataURIInTextIsNotReplaced(t *testing.T) {
	inputJSON := `[{"role":"user","content":"Check this: data:image/png;base64,` + base64PNG + ` please"}]`
	p := NewProcessor(&NoopUploader{}, nil)
	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result)
}

func TestShortBase64IsNotReplaced(t *testing.T) {
	// Short base64 string (< 20 chars) should not trigger replacement.
	inputJSON := `[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"abc123"}}]}]`
	p := NewProcessor(&NoopUploader{}, nil)
	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result)
}

func TestMalformedJSONDoesNotKillProcessor(t *testing.T) {
	uploader := &NoopUploader{}
	p := NewProcessor(uploader, nil)

	// First call: malformed JSON that passes the heuristic but fails to parse.
	badJSON := `{"data":"` + base64PNG + `" INVALID`
	result := p.ProcessAndUpload(badJSON)
	assert.Equal(t, badJSON, result, "should return original on parse error")
	assert.False(t, uploader.IsShutdown(), "uploader should NOT be shut down by a JSON parse error")

	// Second call: valid JSON with an attachment — should still work.
	goodJSON := `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}}]}]`
	result = p.ProcessAndUpload(goodJSON)
	assert.NotEqual(t, goodJSON, result, "subsequent valid spans should still be processed")
	assert.Contains(t, result, "braintrust_attachment")
}

func TestEmptyInputReturnsEmpty(t *testing.T) {
	p := NewProcessor(&NoopUploader{}, nil)
	assert.Equal(t, "", p.ProcessAndUpload(""))
}

func TestHeuristicSkipsPlainText(t *testing.T) {
	// JSON with no base64 patterns at all.
	inputJSON := `{"messages":[{"role":"user","content":"just text"}]}`
	p := NewProcessor(&NoopUploader{}, nil)
	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result)
}

func TestUploaderShutdownSkipsProcessing(t *testing.T) {
	uploader := &NoopUploader{}
	uploader.Shutdown()
	p := NewProcessor(uploader, nil)
	inputJSON := `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}}]}]`
	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result)
}

func TestUploaderRejectionReturnsOriginal(t *testing.T) {
	// An uploader that rejects all enqueue calls.
	uploader := &rejectingUploader{}
	p := NewProcessor(uploader, nil)
	inputJSON := `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}}]}]`
	result := p.ProcessAndUpload(inputJSON)
	// Should return original since the upload was rejected.
	assert.Equal(t, inputJSON, result)
}

func TestPartialEnqueueFailureReturnsOriginal(t *testing.T) {
	// Uploader that succeeds N times then rejects. With 2 attachments in the
	// payload and a limit of 1, the second enqueue fails mid-walk.
	uploader := &limitedUploader{remaining: 1}
	p := NewProcessor(uploader, nil)

	// Two OpenAI-format images in one message.
	inputJSON := `[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}}` +
		`]}]`

	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result,
		"should return original JSON unchanged when an enqueue fails mid-walk")
}

func TestAlwaysRejectingUploaderReturnsOriginal(t *testing.T) {
	uploader := &rejectingUploader{}
	p := NewProcessor(uploader, nil)

	inputJSON := `[{"role":"user","content":[` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + base64PNG + `"}}` +
		`]}]`

	result := p.ProcessAndUpload(inputJSON)
	assert.Equal(t, inputJSON, result,
		"should return original JSON unchanged when uploader rejects all enqueues")
}

// ── isEntirelyDataURI ──────────────────────────────────────────────

func TestIsEntirelyDataURI(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"data:image/png;base64,abc123", true},
		{" data:image/png;base64,abc123 ", true},
		{`Check this: data:image/png;base64,abc123 please`, false}, // space
		{`"data:image/png;base64,abc123"`, false},                  // quotes
		{`data:image/png;base64,abc\n123`, false},                  // backslash
		{"not-a-data-uri", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, isEntirelyDataURI(tt.input), "input: %q", tt.input)
	}
}

// ── Content type to extension ──────────────────────────────────────

func TestContentTypeToExtension(t *testing.T) {
	tests := []struct {
		contentType string
		expected    string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"application/pdf", ".pdf"},
		{"video/mp4", ".mp4"},
		{"audio/mpeg", ".mp3"},
		{"application/octet-stream", ".octet"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.expected, contentTypeToExtension(tt.contentType),
			"contentType: %q", tt.contentType)
	}
}

// ── Helpers ─────────────────────────────────────────────────────────

func assertAttachmentRef(t *testing.T, node any, expectedContentType string) {
	t.Helper()
	require.NotNil(t, node, "attachment ref node should not be nil")
	ref, ok := node.(map[string]any)
	require.True(t, ok, "attachment ref should be a map, got %T", node)
	assert.Equal(t, "braintrust_attachment", ref["type"])
	assert.Equal(t, expectedContentType, ref["content_type"])
	assert.NotEmpty(t, ref["filename"])
	assert.NotEmpty(t, ref["key"])
}

// rejectingUploader is an uploader that rejects all enqueue calls.
type rejectingUploader struct{ NoopUploader }

func (u *rejectingUploader) Enqueue(_ Reference, _ []byte) bool { return false }

// limitedUploader accepts the first N enqueue calls, then rejects.
type limitedUploader struct {
	NoopUploader
	remaining int
}

func (u *limitedUploader) Enqueue(_ Reference, _ []byte) bool {
	if u.remaining <= 0 {
		return false
	}
	u.remaining--
	return true
}
