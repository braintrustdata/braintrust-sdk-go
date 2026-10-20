package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const normalizationFixture = `{
  "openapi":"3.0.3",
  "paths":{},
  "components":{
    "schemas":{
      "ProjectScoreType":{"type":"string","enum":["slider","categorical"]},
      "NullableEnum":{"type":"string","enum":["scorer",null],"default":"scorer"},
      "NullableUnion":{"anyOf":[{"type":"string"},{"type":"null"}]},
      "Payload":{"type":"object","properties":{"count":{"type":"integer","default":9007199254740993}},"example":{"enum":[null],"anyOf":[{"type":"null"}]}}
    },
    "parameters":{
      "ProjectScoreType":{
        "name":"score_type","in":"query","required":false,
        "schema":{"anyOf":[{"$ref":"#/components/schemas/ProjectScoreType"},{"type":"array","items":{"allOf":[{"$ref":"#/components/schemas/ProjectScoreType"},{"title":"project_score_type"}]}}]}
      }
    }
  }
}`

func decodeNormalizationDocument(t *testing.T, input []byte) map[string]any {
	t.Helper()
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&result))
	return result
}

// Resolve local references to compare meaning rather than extraction layout.
func resolveNormalizationSchema(value any, schemas map[string]any) any {
	switch value := value.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			return resolveNormalizationSchema(schemas[strings.TrimPrefix(ref, "#/components/schemas/")], schemas)
		}
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[key] = resolveNormalizationSchema(child, schemas)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, child := range value {
			result[i] = resolveNormalizationSchema(child, schemas)
		}
		return result
	default:
		return value
	}
}

func TestNormalizePreservesSchemaMeaning(t *testing.T) {
	original := decodeNormalizationDocument(t, []byte(normalizationFixture))
	output, err := normalizeSpec([]byte(normalizationFixture))
	require.NoError(t, err)
	result := decodeNormalizationDocument(t, output)
	before := original["components"].(map[string]any)
	after := result["components"].(map[string]any)
	schemas := after["schemas"].(map[string]any)
	parameter := after["parameters"].(map[string]any)["ProjectScoreType"].(map[string]any)
	originalParameter := before["parameters"].(map[string]any)["ProjectScoreType"].(map[string]any)

	// Extraction retains both query forms, without renaming the wire parameter.
	require.Equal(t,
		resolveNormalizationSchema(originalParameter["schema"], before["schemas"].(map[string]any)),
		resolveNormalizationSchema(parameter["schema"], schemas))
	require.Equal(t, map[string]any{"$ref": "#/components/schemas/ProjectScoreTypeFilter"}, parameter["schema"])
	require.Equal(t, "score_type", parameter["name"])
	require.Equal(t, "query", parameter["in"])
	require.Equal(t, "ProjectScoreTypeParameter", parameter["x-go-name"])
	require.Equal(t, before["schemas"].(map[string]any)["ProjectScoreType"], schemas["ProjectScoreType"])

	enum := schemas["NullableEnum"].(map[string]any)
	require.Equal(t, []any{"scorer"}, enum["enum"])
	require.Equal(t, true, enum["nullable"])
	require.Equal(t, "scorer", enum["default"])
	union := schemas["NullableUnion"].(map[string]any)
	require.Equal(t, true, union["nullable"])
	require.Equal(t, before["schemas"].(map[string]any)["NullableUnion"].(map[string]any)["anyOf"], union["anyOf"])

	// Arbitrary example data and integers beyond float64 precision stay intact.
	require.Equal(t, before["schemas"].(map[string]any)["Payload"], schemas["Payload"])
}

func TestNormalizeRejectsChangedInput(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"conflicting component", func(doc map[string]any) {
			doc["components"].(map[string]any)["schemas"].(map[string]any)["ProjectScoreTypeFilter"] = map[string]any{"type": "boolean"}
		}},
		{"changed parameter", func(doc map[string]any) {
			doc["components"].(map[string]any)["parameters"].(map[string]any)["ProjectScoreType"].(map[string]any)["name"] = "other"
		}},
		{"changed union", func(doc map[string]any) {
			doc["components"].(map[string]any)["parameters"].(map[string]any)["ProjectScoreType"].(map[string]any)["schema"] = map[string]any{"type": "string"}
		}},
		{"null-only enum", func(doc map[string]any) {
			doc["components"].(map[string]any)["schemas"].(map[string]any)["NullableEnum"].(map[string]any)["enum"] = []any{nil}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			doc := decodeNormalizationDocument(t, []byte(normalizationFixture))
			change.edit(doc)
			input, err := json.Marshal(doc)
			require.NoError(t, err)
			_, err = normalizeSpec(input)
			require.Error(t, err)
		})
	}
}
