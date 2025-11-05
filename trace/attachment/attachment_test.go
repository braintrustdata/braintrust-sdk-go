package attachment

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test the new reader-based Attachment API
func TestAttachment_NewAPI_FromBytes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create attachment from raw bytes
	data := []byte("test data")
	att := FromBytes(ImageJPEG, data)

	require.NotNil(att)

	// Test Base64URL() returns data URL
	url, err := att.Base64URL()
	require.NoError(err)
	assert.Contains(url, "data:image/jpeg;base64,")
	// "test data" in base64 is "dGVzdCBkYXRh"
	assert.Contains(url, "dGVzdCBkYXRh")
}

func TestAttachment_NewAPI_Base64Message(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create attachment from raw bytes
	data := []byte("test data")
	att := FromBytes(ImageJPEG, data)

	require.NotNil(att)

	// Test Base64Message() returns proper message format
	msg, err := att.Base64Message()
	require.NoError(err)
	assert.Equal("base64_attachment", msg["type"])
	assert.Contains(msg["content"], "data:image/jpeg;base64,")
	assert.Contains(msg["content"], "dGVzdCBkYXRh")
}

func TestAttachment_NewAPI_SingleUse(t *testing.T) {
	require := require.New(t)

	// Create attachment
	data := []byte("test data")
	att := FromBytes(ImageJPEG, data)

	// First call should succeed
	_, err := att.Base64URL()
	require.NoError(err)

	// Second call should fail (reader consumed)
	_, err = att.Base64URL()
	require.Error(err)
	assert.Contains(t, err.Error(), "already consumed")
}

func TestAttachment_FromBytes_EncodesCorrectly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create attachment from raw bytes
	data := []byte("test data")
	att := FromBytes(ImageJPEG, data)

	require.NotNil(att)

	// Get the message format
	msg, err := att.Base64Message()
	require.NoError(err)
	assert.Equal("base64_attachment", msg["type"])

	// Should contain data URL with base64 encoding
	assert.Contains(msg["content"], "data:image/jpeg;base64,")
	// "test data" in base64 is "dGVzdCBkYXRh"
	assert.Contains(msg["content"], "dGVzdCBkYXRh")
}

func TestAttachment_FromFile_ReadsJPEG(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create a temporary test file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.jpg")
	testData := []byte("fake jpeg data")
	err := os.WriteFile(testFile, testData, 0644)
	require.NoError(err)

	// Read attachment from file
	att, err := FromFile(ImageJPEG, testFile)
	require.NoError(err)
	require.NotNil(att)

	// Get the message format
	msg, err := att.Base64Message()
	require.NoError(err)
	assert.Equal("base64_attachment", msg["type"])
	assert.Contains(msg["content"], "data:image/jpeg;base64,")
	// "fake jpeg data" base64 encoded
	assert.Contains(msg["content"], "ZmFrZSBqcGVnIGRhdGE=")
}

func TestAttachment_FromFile_ErrorOnNonExistentFile(t *testing.T) {
	require := require.New(t)

	// Try to read non-existent file
	att, err := FromFile(ImagePNG, "/nonexistent/file.png")

	require.Error(err)
	assert.Nil(t, att)
	assert.Contains(t, err.Error(), "failed to read file")
}

func TestAttachment_FromReader_ReadsStream(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create a temporary file and open it as a reader
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.png")
	testData := []byte("fake png data")
	err := os.WriteFile(testFile, testData, 0644)
	require.NoError(err)

	file, err := os.Open(testFile)
	require.NoError(err)
	defer func() {
		_ = file.Close()
	}()

	// Read attachment from reader
	att := FromReader(ImagePNG, file)
	require.NotNil(att)

	// Get the message format
	msg, err := att.Base64Message()
	require.NoError(err)
	assert.Equal("base64_attachment", msg["type"])
	assert.Contains(msg["content"], "data:image/png;base64,")
	// "fake png data" base64 encoded
	assert.Contains(msg["content"], "ZmFrZSBwbmcgZGF0YQ==")
}

func TestAttachment_JSONMarshal_CorrectFormat(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create an attachment
	data := []byte("test image")
	att := FromBytes(ImageJPEG, data)

	// Get the message format
	msg, err := att.Base64Message()
	require.NoError(err)

	// Marshal the message to JSON
	jsonData, err := json.Marshal(msg)
	require.NoError(err)

	// Parse the JSON back
	var result map[string]interface{}
	err = json.Unmarshal(jsonData, &result)
	require.NoError(err)

	// Verify structure matches Braintrust format
	assert.Equal("base64_attachment", result["type"])
	assert.Contains(result["content"], "data:image/jpeg;base64,")

	// Verify it can be used in a message structure
	att2 := FromBytes(ImageJPEG, data)
	msg2, err := att2.Base64Message()
	require.NoError(err)

	message := map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "What's in this image?",
			},
			msg2, // Include attachment message
		},
	}

	messageJSON, err := json.Marshal(message)
	require.NoError(err)

	// Verify the full message structure
	var parsedMessage map[string]interface{}
	err = json.Unmarshal(messageJSON, &parsedMessage)
	require.NoError(err)

	assert.Equal("user", parsedMessage["role"])
	content, ok := parsedMessage["content"].([]interface{})
	require.True(ok, "content should be an array")
	assert.Len(content, 2)

	// Check text part
	textPart, ok := content[0].(map[string]interface{})
	require.True(ok, "text part should be a map")
	assert.Equal("text", textPart["type"])

	// Check attachment part
	attPart, ok := content[1].(map[string]interface{})
	require.True(ok, "attachment part should be a map")
	assert.Equal("base64_attachment", attPart["type"])
	assert.Contains(attPart["content"], "data:image/jpeg;base64,")
}

func TestAttachment_FromURL_FetchesImage(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Create a mock HTTP server
	testData := []byte("image from url")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testData)
	}))
	defer server.Close()

	// Fetch attachment from URL
	att, err := FromURL(server.URL)
	require.NoError(err)
	require.NotNil(att)

	// Get the message format
	msg, err := att.Base64Message()
	require.NoError(err)
	assert.Equal("base64_attachment", msg["type"])
	assert.Contains(msg["content"], "data:image/png;base64,")
	// "image from url" base64 encoded
	assert.Contains(msg["content"], "aW1hZ2UgZnJvbSB1cmw=")
}

func TestAttachment_FromURL_ErrorOn404(t *testing.T) {
	require := require.New(t)

	// Create a mock HTTP server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Try to fetch from URL
	att, err := FromURL(server.URL)

	require.Error(err)
	assert.Nil(t, att)
	assert.Contains(t, err.Error(), "status 404")
}

func TestAttachment_FromURL_ErrorOnInvalidURL(t *testing.T) {
	require := require.New(t)

	// Try to fetch from invalid URL
	att, err := FromURL("http://this-domain-does-not-exist-12345.com/image.png")

	require.Error(err)
	assert.Nil(t, att)
	assert.Contains(t, err.Error(), "failed to fetch URL")
}

// Example demonstrates how to create an attachment from a file and embed it
// in a message for multimodal AI requests. The attachment is included alongside
// text content in the message structure used by OpenAI and Anthropic APIs.
func Example() {
	// Create an attachment from a local file
	attach, err := FromFile(ImageJPEG, "./photo.jpg")
	if err != nil {
		panic(err)
	}

	// Get the attachment in message format
	attachMsg, err := attach.Base64Message()
	if err != nil {
		panic(err)
	}

	// Embed the attachment in a message structure (OpenAI/Anthropic format)
	message := map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "What's in this image?",
			},
			attachMsg, // Attachment message in correct format
		},
	}

	// Convert to JSON to log to a span
	jsonData, _ := json.Marshal(message)
	fmt.Printf("Message with attachment: %s\n", jsonData)

	// In practice, you would log this to a span using:
	// span.SetAttributes(attribute.String("braintrust.input_json", string(jsonData)))
}

// Example_multipleAttachments shows how to include multiple attachments
// in a single message, which is useful for comparing images or providing
// multiple pieces of visual context.
func Example_multipleAttachments() {
	// Create attachments from different sources
	imageAttach, _ := FromFile(ImagePNG, "./chart.png")
	pdfAttach, _ := FromFile(PDF, "./report.pdf")

	// Get message format for each attachment
	imageMsg, _ := imageAttach.Base64Message()
	pdfMsg, _ := pdfAttach.Base64Message()

	// Build a message with text and multiple attachments
	message := map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": "Compare the data in this chart with the report.",
			},
			imageMsg,
			pdfMsg,
		},
	}

	_ = message // Log to span in your application
}
