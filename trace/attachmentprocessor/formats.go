package attachmentprocessor

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// minBase64Len is the minimum length of a base64 string to consider it a
// real attachment (avoids false positives on short strings).
const minBase64Len = 20

// dataURIPrefix matches "data:<mime>;base64,".
const dataURIPrefix = `data:([\w/\-.+]+);base64,`

// base64Str matches a base64 string of at least minBase64Len characters.
const base64Str = `([A-Za-z0-9+/=]{20,})`

var dataURIPattern = regexp.MustCompile(dataURIPrefix + base64Str)

// heuristic patterns for the fast-path check against raw JSON strings.
var (
	// Matches a quoted data URI (OpenAI format).
	dataURIHeuristic = `"` + dataURIPrefix + base64Str + `"`
	// Matches "bytes" or "data" keys with a base64 value (Bedrock/Anthropic/Gemini).
	byteValueHeuristic = `"(?:bytes|data)"\s*:\s*"` + base64Str + `"`
)

// Format describes how to detect and replace a provider-specific base64
// attachment structure. Each vendor is a self-contained unit; adding a new
// provider is a matter of appending an entry to Formats.
type Format struct {
	// Name is a human-readable label for logging and test-coverage tracking.
	Name string
	// HeuristicFragment is a regex fragment for the combined fast-path
	// heuristic. Duplicate fragments across formats are de-duplicated.
	HeuristicFragment string
	// Match returns true if this format should handle the given JSON node.
	// parentKey is the key of this node within its parent (empty for root/array items).
	Match func(parentKey string, node any) bool
	// Replace builds the replacement value. It receives the matched node and
	// an Uploader to enqueue the binary data. It returns the replacement value
	// and the decoded binary data, or nil if replacement should be skipped.
	Replace func(node any, upload UploadFunc) (replacement any, ok bool)
}

// UploadFunc enqueues an attachment for background upload. Returns false if
// the uploader has shut down or the queue is full.
type UploadFunc func(ref Reference, data []byte) bool

// Formats lists all supported vendor attachment formats, checked in order
// during tree traversal.
var Formats = []Format{
	openAIFormat,
	bedrockFormat,
	anthropicFormat,
	geminiFormat,
}

// BuildHeuristic compiles a single regex from all format heuristic fragments.
func BuildHeuristic(formats []Format) *regexp.Regexp {
	seen := make(map[string]bool)
	var fragments []string
	for _, f := range formats {
		if !seen[f.HeuristicFragment] {
			seen[f.HeuristicFragment] = true
			fragments = append(fragments, f.HeuristicFragment)
		}
	}
	return regexp.MustCompile(strings.Join(fragments, "|"))
}

// ── OpenAI ─────────────────────────────────────────────────────────

// openAIFormat handles data URIs in text node values.
// e.g. image_url.url = "data:image/png;base64,..."
var openAIFormat = Format{
	Name:              "openai",
	HeuristicFragment: dataURIHeuristic,
	Match: func(_ string, node any) bool {
		s, ok := node.(string)
		if !ok {
			return false
		}
		return isEntirelyDataURI(s) && dataURIPattern.MatchString(s)
	},
	Replace: func(node any, upload UploadFunc) (any, bool) {
		s, ok := node.(string)
		if !ok {
			return nil, false
		}
		m := dataURIPattern.FindStringSubmatch(s)
		if m == nil {
			return nil, false
		}
		contentType := m[1]
		b64Data := m[2]
		return uploadAndCreateRef(contentType, b64Data, upload)
	},
}

// isEntirelyDataURI returns true when the trimmed value is entirely a data URI
// — no quotes, backslashes, or spaces mixed in.
func isEntirelyDataURI(value string) bool {
	t := strings.TrimSpace(value)
	return strings.HasPrefix(t, "data:") &&
		!strings.Contains(t, "\"") &&
		!strings.Contains(t, "\\") &&
		!strings.Contains(t, " ")
}

// ── Bedrock Converse ───────────────────────────────────────────────

// Per-block-type format-to-MIME mappings for the AWS Bedrock Converse API.
var converseBlockTypeFormats = map[string]map[string]string{
	"image": {
		"gif":  "image/gif",
		"jpeg": "image/jpeg",
		"png":  "image/png",
		"webp": "image/webp",
	},
	"video": {
		"flv":      "video/x-flv",
		"mkv":      "video/x-matroska",
		"mov":      "video/quicktime",
		"mp4":      "video/mp4",
		"mpeg":     "video/mpeg",
		"mpg":      "video/mpeg",
		"three_gp": "video/3gpp",
		"webm":     "video/webm",
		"wmv":      "video/x-ms-wmv",
	},
	"audio": {
		"aac":   "audio/aac",
		"flac":  "audio/flac",
		"m4a":   "audio/mp4",
		"mka":   "audio/x-matroska",
		"mkv":   "audio/x-matroska",
		"mp3":   "audio/mpeg",
		"mp4":   "audio/mp4",
		"mpeg":  "audio/mpeg",
		"mpga":  "audio/mpeg",
		"ogg":   "audio/ogg",
		"opus":  "audio/opus",
		"pcm":   "audio/pcm",
		"wav":   "audio/wav",
		"webm":  "audio/webm",
		"x-aac": "audio/aac",
	},
	"document": {
		"csv":  "text/csv",
		"doc":  "application/msword",
		"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"html": "text/html",
		"md":   "text/markdown",
		"pdf":  "application/pdf",
		"txt":  "text/plain",
		"xls":  "application/vnd.ms-excel",
		"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	},
}

type converseBlock struct {
	blockTypeKey string
	inner        map[string]any
	formatMap    map[string]string
}

func getConverseBlock(obj map[string]any) *converseBlock {
	for blockKey, fmtMap := range converseBlockTypeFormats {
		inner, ok := obj[blockKey]
		if !ok {
			continue
		}
		innerMap, ok := inner.(map[string]any)
		if !ok {
			continue
		}
		fmtVal, ok := innerMap["format"]
		if !ok {
			continue
		}
		fmtStr, ok := fmtVal.(string)
		if !ok {
			continue
		}
		if _, found := fmtMap[strings.ToLower(fmtStr)]; !found {
			continue
		}
		src, ok := innerMap["source"]
		if !ok {
			continue
		}
		srcMap, ok := src.(map[string]any)
		if !ok {
			continue
		}
		bytesVal, ok := srcMap["bytes"]
		if !ok {
			continue
		}
		bytesStr, ok := bytesVal.(string)
		if !ok || len(bytesStr) < minBase64Len {
			continue
		}
		return &converseBlock{
			blockTypeKey: blockKey,
			inner:        innerMap,
			formatMap:    fmtMap,
		}
	}
	return nil
}

var bedrockFormat = Format{
	Name:              "bedrock",
	HeuristicFragment: byteValueHeuristic,
	Match: func(_ string, node any) bool {
		obj, ok := node.(map[string]any)
		if !ok {
			return false
		}
		return getConverseBlock(obj) != nil
	},
	Replace: func(node any, upload UploadFunc) (any, bool) {
		obj, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		block := getConverseBlock(obj)
		if block == nil {
			return nil, false
		}

		fmtStr, ok := block.inner["format"].(string)
		if !ok {
			return nil, false
		}
		contentType, ok := block.formatMap[strings.ToLower(fmtStr)]
		if !ok {
			return nil, false
		}

		srcMap, ok := block.inner["source"].(map[string]any)
		if !ok {
			return nil, false
		}
		b64Data, ok := srcMap["bytes"].(string)
		if !ok {
			return nil, false
		}
		refVal, ok := uploadAndCreateRef(contentType, b64Data, upload)
		if !ok {
			return nil, false
		}

		// Rebuild: copy all fields, replace source.bytes in the matched block.
		result := make(map[string]any, len(obj))
		for k, v := range obj {
			if k == block.blockTypeKey {
				newInner := make(map[string]any, len(block.inner))
				for ik, iv := range block.inner {
					if ik == "source" {
						origSource, ok := iv.(map[string]any)
						if !ok {
							newInner[ik] = iv
							continue
						}
						newSource := make(map[string]any, len(origSource))
						for sk, sv := range origSource {
							if sk == "bytes" {
								newSource[sk] = refVal
							} else {
								newSource[sk] = sv
							}
						}
						newInner[ik] = newSource
					} else {
						newInner[ik] = iv
					}
				}
				result[k] = newInner
			} else {
				result[k] = v
			}
		}
		return result, true
	},
}

// ── Anthropic ──────────────────────────────────────────────────────

// anthropicFormat handles {"type":"base64","media_type":"image/png","data":"<b64>"}.
// The entire source object is replaced with the attachment reference.
var anthropicFormat = Format{
	Name:              "anthropic",
	HeuristicFragment: byteValueHeuristic,
	Match: func(_ string, node any) bool {
		obj, ok := node.(map[string]any)
		if !ok {
			return false
		}
		typ, _ := obj["type"].(string)
		if typ != "base64" {
			return false
		}
		mediaType, ok := obj["media_type"].(string)
		if !ok || mediaType == "" {
			return false
		}
		data, ok := obj["data"].(string)
		return ok && len(data) >= minBase64Len
	},
	Replace: func(node any, upload UploadFunc) (any, bool) {
		obj, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		contentType, ok := obj["media_type"].(string)
		if !ok {
			return nil, false
		}
		b64Data, ok := obj["data"].(string)
		if !ok {
			return nil, false
		}
		return uploadAndCreateRef(contentType, b64Data, upload)
	},
}

// ── Gemini ─────────────────────────────────────────────────────────

// geminiFormat handles {"inlineData": {"mimeType":"image/png","data":"<b64>"}}.
// Images → image_url: {url: ref}; non-images → file: {file_data: ref}.
var geminiFormat = Format{
	Name:              "gemini",
	HeuristicFragment: byteValueHeuristic,
	Match: func(_ string, node any) bool {
		obj, ok := node.(map[string]any)
		if !ok {
			return false
		}
		inlineData, ok := obj["inlineData"]
		if !ok {
			return false
		}
		idMap, ok := inlineData.(map[string]any)
		if !ok {
			return false
		}
		mimeType, ok := idMap["mimeType"].(string)
		if !ok || mimeType == "" {
			return false
		}
		data, ok := idMap["data"].(string)
		return ok && len(data) >= minBase64Len
	},
	Replace: func(node any, upload UploadFunc) (any, bool) {
		obj, ok := node.(map[string]any)
		if !ok {
			return nil, false
		}
		idMap, ok := obj["inlineData"].(map[string]any)
		if !ok {
			return nil, false
		}
		contentType, ok := idMap["mimeType"].(string)
		if !ok {
			return nil, false
		}
		b64Data, ok := idMap["data"].(string)
		if !ok {
			return nil, false
		}

		refVal, ok := uploadAndCreateRef(contentType, b64Data, upload)
		if !ok {
			return nil, false
		}

		isImage := strings.HasPrefix(contentType, "image/")

		// Rebuild: swap inlineData for the appropriate wrapper.
		result := make(map[string]any, len(obj))
		for k, v := range obj {
			if k == "inlineData" {
				if isImage {
					result["image_url"] = map[string]any{"url": refVal}
				} else {
					result["file"] = map[string]any{"file_data": refVal}
				}
			} else {
				result[k] = v
			}
		}
		return result, true
	},
}

// ── Shared helpers ─────────────────────────────────────────────────

// uploadAndCreateRef decodes base64 data, creates a reference, and enqueues
// the upload. Returns the reference as a map (for JSON embedding) and true,
// or nil, false on failure.
func uploadAndCreateRef(contentType, b64Data string, upload UploadFunc) (any, bool) {
	data, err := base64.StdEncoding.DecodeString(b64Data)
	if err != nil {
		return nil, false
	}
	ref := NewReference(contentType)
	if !upload(ref, data) {
		return nil, false
	}
	// Return as map[string]any so it embeds naturally in the JSON tree.
	return map[string]any{
		"type":         ref.Type,
		"content_type": ref.ContentType,
		"filename":     ref.Filename,
		"key":          ref.Key,
	}, true
}
