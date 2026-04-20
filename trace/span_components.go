// Package trace provides span component encoding/decoding for distributed tracing.
// The format is compatible with the Braintrust JS/Python SDK SpanComponents V4.
package trace

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

const encodingVersionV4 = 4

// Field IDs in the V4 binary format (must match JS/Python).
const (
	fieldObjectID   = 1
	fieldRowID      = 2
	fieldSpanID     = 3 // 8-byte hex
	fieldRootSpanID = 4 // 16-byte hex
)

// SpanObjectTypeV3 matches the Braintrust backend object types.
type SpanObjectTypeV3 byte

const (
	SpanObjectTypeExperiment     SpanObjectTypeV3 = 1
	SpanObjectTypeProjectLogs    SpanObjectTypeV3 = 2
	SpanObjectTypePlaygroundLogs SpanObjectTypeV3 = 3
)

// SpanComponents holds the decoded span identifiers from an exported span.
// It is compatible with the Braintrust SpanComponents V4 format.
type SpanComponents struct {
	ObjectType SpanObjectTypeV3
	ObjectID   string

	// Span identifiers (all set when exporting a specific span).
	RowID      string
	SpanID     string // 8-byte hex (16 chars)
	RootSpanID string // 16-byte hex (32 chars), same as OTel trace ID
}

// tryHexSpanID checks if s is 16 hex chars (8 bytes) and returns bytes or nil.
func tryHexSpanID(s string) []byte {
	if len(s) != 16 {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// tryHexTraceID checks if s is 32 hex chars (16 bytes) and returns bytes or nil.
func tryHexTraceID(s string) []byte {
	if len(s) != 32 {
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// EncodeV4 serializes the span components to a base64 string (SpanComponents V4 format).
// The result can be passed to other processes and used with ContextWithExportedSpan.
func EncodeV4(c SpanComponents) (string, error) {
	jsonObj := make(map[string]interface{})

	// Header: version + object_type
	buf := []byte{encodingVersionV4, byte(c.ObjectType)}

	var hexEntries [][]byte

	addHexField := func(val string, fieldID byte) {
		if fieldID == fieldSpanID {
			if b := tryHexSpanID(val); b != nil {
				hexEntries = append(hexEntries, append([]byte{fieldID}, b...))
				return
			}
		} else if fieldID == fieldRootSpanID {
			if b := tryHexTraceID(val); b != nil {
				hexEntries = append(hexEntries, append([]byte{fieldID}, b...))
				return
			}
		}
		// Non-hex or other field: put in JSON
		name := map[byte]string{fieldObjectID: "object_id", fieldRowID: "row_id", fieldSpanID: "span_id", fieldRootSpanID: "root_span_id"}[fieldID]
		jsonObj[name] = val
	}

	if c.ObjectID != "" {
		addHexField(c.ObjectID, fieldObjectID)
	}
	if c.RowID != "" {
		addHexField(c.RowID, fieldRowID)
	}
	if c.SpanID != "" {
		addHexField(c.SpanID, fieldSpanID)
	}
	if c.RootSpanID != "" {
		addHexField(c.RootSpanID, fieldRootSpanID)
	}

	if len(hexEntries) > 255 {
		return "", fmt.Errorf("too many hex entries")
	}

	buf = append(buf, byte(len(hexEntries)))
	for _, e := range hexEntries {
		buf = append(buf, e...)
	}

	if len(jsonObj) > 0 {
		jsonBytes, err := json.Marshal(jsonObj)
		if err != nil {
			return "", err
		}
		buf = append(buf, jsonBytes...)
	}

	return base64.StdEncoding.EncodeToString(buf), nil
}

// DecodeV4 parses a base64-encoded SpanComponents V4 string.
// Supports both V4 and older V3 payloads (decodes via V3 logic for version < 4).
func DecodeV4(s string) (SpanComponents, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return SpanComponents{}, fmt.Errorf("decode base64: %w", err)
	}
	if len(raw) < 3 {
		return SpanComponents{}, fmt.Errorf("span components too short")
	}

	version := raw[0]
	if version < encodingVersionV4 {
		return decodeV3Fallback(raw, s)
	}

	// V4 format
	out := SpanComponents{
		ObjectType: SpanObjectTypeV3(raw[1]),
	}
	numHex := raw[2]
	offset := 3

	for i := 0; i < int(numHex); i++ {
		if offset >= len(raw) {
			break
		}
		fieldID := raw[offset]
		offset++
		switch fieldID {
		case fieldSpanID:
			if offset+8 > len(raw) {
				return SpanComponents{}, fmt.Errorf("truncated span_id")
			}
			out.SpanID = hex.EncodeToString(raw[offset : offset+8])
			offset += 8
		case fieldRootSpanID:
			if offset+16 > len(raw) {
				return SpanComponents{}, fmt.Errorf("truncated root_span_id")
			}
			out.RootSpanID = hex.EncodeToString(raw[offset : offset+16])
			offset += 16
		case fieldObjectID, fieldRowID:
			if offset+16 > len(raw) {
				return SpanComponents{}, fmt.Errorf("truncated field %d", fieldID)
			}
			val := hex.EncodeToString(raw[offset : offset+16])
			offset += 16
			if fieldID == fieldObjectID {
				out.ObjectID = val
			} else {
				out.RowID = val
			}
		default:
			return SpanComponents{}, fmt.Errorf("unknown field id %d", fieldID)
		}
	}

	if offset < len(raw) {
		var jsonObj map[string]interface{}
		if err := json.Unmarshal(raw[offset:], &jsonObj); err != nil {
			return SpanComponents{}, fmt.Errorf("decode json: %w", err)
		}
		for k, v := range jsonObj {
			if s, ok := v.(string); ok {
				switch k {
				case "object_id":
					out.ObjectID = s
				case "row_id":
					out.RowID = s
				case "span_id":
					out.SpanID = s
				case "root_span_id":
					out.RootSpanID = s
				}
			}
		}
	}

	return out, nil
}

// uuidLike matches UUID format (with hyphens) or 32 hex chars.
var uuidLike = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$|^[0-9a-fA-F]{32}$`)

// ParentFromComponents returns a Parent that can be used with SetParent so spans
// started under ContextWithExportedSpan are sent to the same Braintrust project/experiment.
// For PROJECT_LOGS, object_id is treated as project name unless it looks like a UUID.
func ParentFromComponents(c SpanComponents) (Parent, error) {
	switch c.ObjectType {
	case SpanObjectTypeExperiment:
		return Parent{Type: ParentTypeExperimentID, ID: c.ObjectID}, nil
	case SpanObjectTypeProjectLogs, SpanObjectTypePlaygroundLogs:
		if uuidLike.MatchString(c.ObjectID) {
			return Parent{Type: ParentTypeProjectID, ID: c.ObjectID}, nil
		}
		return Parent{Type: ParentTypeProjectName, ID: c.ObjectID}, nil
	default:
		return Parent{}, fmt.Errorf("unsupported object type %d", c.ObjectType)
	}
}

// decodeV3Fallback decodes V3-style encoding (version < 4) for interoperability.
// V3 uses 16-byte UUIDs for all fields; we only support reading object_type and object_id from the UUID section,
// and span_id/root_span_id if present as UUIDs. For simplicity we parse the minimal required fields.
func decodeV3Fallback(raw []byte, _ string) (SpanComponents, error) {
	if len(raw) < 3 {
		return SpanComponents{}, fmt.Errorf("v3 span components too short")
	}
	out := SpanComponents{
		ObjectType: SpanObjectTypeV3(raw[1]),
	}
	numUUID := raw[2]
	offset := 3
	for i := 0; i < int(numUUID); i++ {
		if offset+17 > len(raw) {
			break
		}
		fieldID := raw[offset]
		uuidBytes := raw[offset+1 : offset+17]
		offset += 17
		// UUID bytes to hex string (32 chars)
		val := hex.EncodeToString(uuidBytes)
		switch fieldID {
		case fieldObjectID:
			out.ObjectID = val
		case fieldRowID:
			out.RowID = val
		case fieldSpanID:
			out.SpanID = val
		case fieldRootSpanID:
			out.RootSpanID = val
		}
	}
	if offset < len(raw) {
		var jsonObj map[string]interface{}
		_ = json.Unmarshal(raw[offset:], &jsonObj)
		if v, ok := jsonObj["object_id"].(string); ok && out.ObjectID == "" {
			out.ObjectID = v
		}
		if v, ok := jsonObj["row_id"].(string); ok {
			out.RowID = v
		}
		if v, ok := jsonObj["span_id"].(string); ok {
			out.SpanID = v
		}
		if v, ok := jsonObj["root_span_id"].(string); ok {
			out.RootSpanID = v
		}
	}
	return out, nil
}
