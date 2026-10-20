package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// normalizeSpec adapts generator limitations without modifying the pinned input.
func normalizeSpec(input []byte) ([]byte, error) {
	var doc map[string]any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode specification: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("specification must contain one JSON document")
	}

	components := schemaObject(doc["components"])
	schemas := schemaObject(components["schemas"])
	parameters := schemaObject(components["parameters"])
	parameter := schemaObject(parameters["ProjectScoreType"])
	if schemas == nil || parameter == nil {
		return nil, fmt.Errorf("missing ProjectScoreType schema or parameter")
	}
	for _, name := range []string{"ProjectScoreTypeFilter", "ProjectScoreTypeList", "ProjectScoreTypeListItem"} {
		if _, exists := schemas[name]; exists {
			return nil, fmt.Errorf("%s already exists; review schema extraction", name)
		}
	}
	// Preserve the complete scalar-or-array schema. A named component ensures
	// oapi-codegen emits the inline union's helper types, unlike a parameter.
	var expected any
	if err := json.Unmarshal([]byte(`{"anyOf":[{"$ref":"#/components/schemas/ProjectScoreType"},{"type":"array","items":{"allOf":[{"$ref":"#/components/schemas/ProjectScoreType"},{"title":"project_score_type"}]}}]}`), &expected); err != nil {
		return nil, err
	}
	if parameter["name"] != "score_type" || parameter["in"] != "query" || parameter["required"] != false || !reflect.DeepEqual(parameter["schema"], expected) {
		return nil, fmt.Errorf("ProjectScoreType parameter shape changed; review schema extraction")
	}
	if schemaObject(schemas["ProjectScoreType"])["type"] != "string" {
		return nil, fmt.Errorf("ProjectScoreType scalar schema changed")
	}
	filter := schemaObject(parameter["schema"])
	branches, _ := filter["anyOf"].([]any)
	list := schemaObject(branches[1])
	// Array/item and union-helper names also collide when these stay inline.
	// Extract both unchanged schemas so every helper has a distinct component.
	schemas["ProjectScoreTypeListItem"] = list["items"]
	list["items"] = map[string]any{"$ref": "#/components/schemas/ProjectScoreTypeListItem"}
	schemas["ProjectScoreTypeList"] = list
	branches[1] = map[string]any{"$ref": "#/components/schemas/ProjectScoreTypeList"}
	schemas["ProjectScoreTypeFilter"] = filter
	parameter["schema"] = map[string]any{"$ref": "#/components/schemas/ProjectScoreTypeFilter"}
	parameter["x-go-name"] = "ProjectScoreTypeParameter"

	// Public API IDs and cursors are opaque strings. Preserve their spelling and
	// leave validation to the server instead of parsing/canonicalizing UUIDs.
	// Only scalar parameter schemas change Go representation; response models
	// and the upstream UUID wire-format annotations remain unchanged.
	for _, value := range parameters {
		parameterSchema := schemaObject(schemaObject(value)["schema"])
		if ref, ok := parameterSchema["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
			parameterSchema = schemaObject(schemas[strings.TrimPrefix(ref, "#/components/schemas/")])
		}
		if parameterSchema["type"] == "string" && parameterSchema["format"] == "uuid" {
			parameterSchema["x-go-type"] = "string"
		}
	}

	for name, value := range schemas {
		if err := normalizeSchema(schemaObject(value)); err != nil {
			return nil, fmt.Errorf("schema %s: %w", name, err)
		}
	}
	for _, section := range []string{"parameters", "headers", "requestBodies", "responses"} {
		for name, value := range schemaObject(components[section]) {
			if err := normalizeSchemaContainer(schemaObject(value)); err != nil {
				return nil, fmt.Errorf("%s/%s: %w", section, name, err)
			}
		}
	}
	for path, value := range schemaObject(doc["paths"]) {
		item := schemaObject(value)
		if err := normalizeOperation(item); err != nil {
			return nil, fmt.Errorf("path %s: %w", path, err)
		}
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			if err := normalizeOperation(schemaObject(item[method])); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
		}
	}
	return json.Marshal(doc)
}

func schemaObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// Only follow OpenAPI schema locations, never arbitrary example/default data.
func normalizeSchema(schema map[string]any) error {
	if schema == nil {
		return nil
	}
	// An unconstrained nullable value already means any JSON, including null.
	// Express it as true so generated map helpers use interface{}, not a
	// mismatched *interface{} field with interface{} getters/setters.
	if additional := schemaObject(schema["additionalProperties"]); len(additional) == 1 && additional["nullable"] == true {
		schema["additionalProperties"] = true
	}
	if values, ok := schema["enum"].([]any); ok {
		filtered := values[:0]
		for _, value := range values {
			if value == nil {
				schema["nullable"] = true
			} else {
				filtered = append(filtered, value)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("enum has no non-null values")
		}
		schema["enum"] = filtered
	}
	for _, composition := range []string{"allOf", "anyOf", "oneOf"} {
		branches, _ := schema[composition].([]any)
		for _, value := range branches {
			branch := schemaObject(value)
			if composition != "allOf" && branch["type"] == "null" {
				schema["nullable"] = true
			}
			if err := normalizeSchema(branch); err != nil {
				return err
			}
		}
	}
	for _, value := range schemaObject(schema["properties"]) {
		if err := normalizeSchema(schemaObject(value)); err != nil {
			return err
		}
	}
	for _, key := range []string{"items", "additionalProperties", "not"} {
		if err := normalizeSchema(schemaObject(schema[key])); err != nil {
			return err
		}
	}
	return nil
}

// Parameters, headers, request bodies, and responses share schema/content slots.
func normalizeSchemaContainer(container map[string]any) error {
	if err := normalizeSchema(schemaObject(container["schema"])); err != nil {
		return err
	}
	for _, value := range schemaObject(container["content"]) {
		if err := normalizeSchema(schemaObject(schemaObject(value)["schema"])); err != nil {
			return err
		}
	}
	for _, value := range schemaObject(container["headers"]) {
		if err := normalizeSchemaContainer(schemaObject(value)); err != nil {
			return err
		}
	}
	return nil
}

func normalizeOperation(operation map[string]any) error {
	parameters, _ := operation["parameters"].([]any)
	for _, value := range parameters {
		if err := normalizeSchemaContainer(schemaObject(value)); err != nil {
			return err
		}
	}
	if err := normalizeSchemaContainer(schemaObject(operation["requestBody"])); err != nil {
		return err
	}
	for _, value := range schemaObject(operation["responses"]) {
		if err := normalizeSchemaContainer(schemaObject(value)); err != nil {
			return err
		}
	}
	return nil
}
