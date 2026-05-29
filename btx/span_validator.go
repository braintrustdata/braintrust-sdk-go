package btx

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// validateSpans compares actual spans against expected spans from the spec.
// It collects all errors and returns them together for easier debugging.
func validateSpans(actualSpans []map[string]any, spec LlmSpanSpec) error {
	// Filter to LLM spans only.
	var llmSpans []map[string]any
	for _, span := range actualSpans {
		sa, ok := span["span_attributes"].(map[string]any)
		if !ok {
			continue
		}
		if sa["type"] == "llm" {
			llmSpans = append(llmSpans, span)
		}
	}

	// Sort by exec_counter for deterministic ordering.
	sort.SliceStable(llmSpans, func(i, j int) bool {
		return execCounter(llmSpans[i]) < execCounter(llmSpans[j])
	})

	expected := spec.ExpectedBrainstoreSpans

	if len(llmSpans) < len(expected) {
		return fmt.Errorf("%s: expected at least %d LLM spans, got %d",
			spec.DisplayName, len(expected), len(llmSpans))
	}

	var allErrors []string

	for i, exp := range expected {
		actual := llmSpans[i]
		var errors []string
		validateValue(actual, exp, fmt.Sprintf("span[%d]", i), &errors)

		if len(errors) > 0 {
			spanJSON, _ := json.MarshalIndent(actual, "", "  ")
			header := fmt.Sprintf("--- Span %d ---", i)
			if name := spanName(actual); name != "" {
				header = fmt.Sprintf("--- Span %d (%s) ---", i, name)
			}
			allErrors = append(allErrors, header)
			allErrors = append(allErrors, errors...)
			allErrors = append(allErrors, fmt.Sprintf("\nFull span JSON:\n%s", string(spanJSON)))
		}
	}

	if len(allErrors) > 0 {
		return fmt.Errorf("%s: span validation failed:\n\n%s",
			spec.DisplayName, strings.Join(allErrors, "\n"))
	}

	return nil
}

// validateValue recursively validates an actual value against an expected value.
func validateValue(actual, expected any, path string, errors *[]string) {
	switch exp := expected.(type) {
	case OrMatcher:
		validateOrMatcher(actual, exp, path, errors)

	case FnMatcher:
		validateFnMatcher(actual, exp, path, errors)

	case StartsWithMatcher:
		actualStr, ok := actual.(string)
		if !ok {
			*errors = append(*errors, fmt.Sprintf("%s: expected string for starts_with, got %T (%v)", path, actual, actual))
			return
		}
		if !strings.HasPrefix(actualStr, exp.Prefix) {
			*errors = append(*errors, fmt.Sprintf("%s: expected to start with %q, got %q", path, exp.Prefix, actualStr))
		}

	case nil:
		// nil expected = don't care, always passes.

	case map[string]any:
		actualMap, ok := actual.(map[string]any)
		if !ok {
			// Reverse single-item list vs object: actual is [dict], expected is dict.
			if actualList, isList := actual.([]any); isList && len(actualList) == 1 {
				if innerMap, isMap := actualList[0].(map[string]any); isMap {
					validateValue(innerMap, expected, path, errors)
					return
				}
			}
			*errors = append(*errors, fmt.Sprintf("%s: expected map, got %T (%v)", path, actual, actual))
			return
		}
		for key, expVal := range exp {
			actualVal, exists := actualMap[key]
			if !exists {
				// If expected is undefined_or_null, missing key is acceptable.
				if fn, ok := expVal.(FnMatcher); ok && fn.Expr == "undefined_or_null" {
					continue
				}
				*errors = append(*errors, fmt.Sprintf("%s.%s: key missing in actual", path, key))
				continue
			}
			validateValue(actualVal, expVal, path+"."+key, errors)
		}

	case []any:
		// Single-item list vs object special case.
		if len(exp) == 1 {
			if _, isMap := exp[0].(map[string]any); isMap {
				if actualMap, isActualMap := actual.(map[string]any); isActualMap {
					validateValue(actualMap, exp[0], path, errors)
					return
				}
			}
		}

		actualList, ok := actual.([]any)
		if !ok {
			*errors = append(*errors, fmt.Sprintf("%s: expected list, got %T (%v)", path, actual, actual))
			return
		}
		if len(actualList) < len(exp) {
			*errors = append(*errors, fmt.Sprintf("%s: expected at least %d elements, got %d", path, len(exp), len(actualList)))
			return
		}
		for i, expItem := range exp {
			validateValue(actualList[i], expItem, fmt.Sprintf("%s[%d]", path, i), errors)
		}

	default:
		// Scalar comparison.
		if !scalarEqual(actual, expected) {
			*errors = append(*errors, fmt.Sprintf("%s: expected=%v (%T), actual=%v (%T)", path, expected, expected, actual, actual))
		}
	}
}

// validateOrMatcher validates that actual matches at least one alternative.
func validateOrMatcher(actual any, matcher OrMatcher, path string, errors *[]string) {
	var allSubErrors [][]string

	for _, alt := range matcher.Alternatives {
		var subErrors []string
		validateValue(actual, alt, path, &subErrors)
		if len(subErrors) == 0 {
			return // One alternative matched.
		}
		allSubErrors = append(allSubErrors, subErrors)
	}

	// None matched — report all alternatives' errors.
	*errors = append(*errors, fmt.Sprintf("%s: none of %d alternatives matched:", path, len(matcher.Alternatives)))
	for i, subErrors := range allSubErrors {
		*errors = append(*errors, fmt.Sprintf("  Alternative %d:", i))
		for _, e := range subErrors {
			*errors = append(*errors, "    "+e)
		}
	}
}

// validateFnMatcher validates using a named predicate or lambda expression.
func validateFnMatcher(actual any, matcher FnMatcher, path string, errors *[]string) {
	switch matcher.Expr {
	case "is_non_negative_number":
		if !isNumber(actual) {
			*errors = append(*errors, fmt.Sprintf("%s: expected number for is_non_negative_number, got %T (%v)", path, actual, actual))
			return
		}
		if toFloat64(actual) < 0 {
			*errors = append(*errors, fmt.Sprintf("%s: expected non-negative number, got %v", path, actual))
		}

	case "is_positive_number":
		if !isNumber(actual) {
			*errors = append(*errors, fmt.Sprintf("%s: expected number for is_positive_number, got %T (%v)", path, actual, actual))
			return
		}
		if toFloat64(actual) <= 0 {
			*errors = append(*errors, fmt.Sprintf("%s: expected positive number, got %v", path, actual))
		}

	case "is_non_empty_string":
		str, ok := actual.(string)
		if !ok || str == "" {
			*errors = append(*errors, fmt.Sprintf("%s: expected non-empty string, got %T (%v)", path, actual, actual))
		}

	case "is_reasoning_message":
		validateReasoningMessage(actual, path, errors)

	case "undefined_or_null":
		if actual != nil {
			*errors = append(*errors, fmt.Sprintf("%s: expected null/undefined, got %T (%v)", path, actual, actual))
		}

	default:
		// Lambda expressions or unknown predicates.
		// In Go we can't evaluate Python lambdas, so treat as "non-null and non-empty".
		if actual == nil {
			*errors = append(*errors, fmt.Sprintf("%s: expected non-null for fn %q, got nil", path, matcher.Expr))
			return
		}
		if str, ok := actual.(string); ok && str == "" {
			*errors = append(*errors, fmt.Sprintf("%s: expected non-empty for fn %q, got empty string", path, matcher.Expr))
		}
	}
}

// validateReasoningMessage validates that the value is a list of {type: "summary_text", text: <non-empty>} dicts,
// or an empty list.
func validateReasoningMessage(actual any, path string, errors *[]string) {
	list, ok := actual.([]any)
	if !ok {
		*errors = append(*errors, fmt.Sprintf("%s: expected list for is_reasoning_message, got %T", path, actual))
		return
	}
	// Empty list is valid.
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			*errors = append(*errors, fmt.Sprintf("%s[%d]: expected map in reasoning message, got %T", path, i, item))
			continue
		}
		if m["type"] != "summary_text" {
			*errors = append(*errors, fmt.Sprintf("%s[%d].type: expected 'summary_text', got %v", path, i, m["type"]))
		}
		text, ok := m["text"].(string)
		if !ok || text == "" {
			*errors = append(*errors, fmt.Sprintf("%s[%d].text: expected non-empty string, got %v", path, i, m["text"]))
		}
	}
}

// scalarEqual compares two scalar values with numeric tolerance.
func scalarEqual(actual, expected any) bool {
	// Handle numeric comparisons with epsilon.
	if isNumber(actual) && isNumber(expected) {
		return math.Abs(toFloat64(actual)-toFloat64(expected)) < 1e-9
	}

	return fmt.Sprintf("%v", actual) == fmt.Sprintf("%v", expected)
}

// isNumber returns true if the value is a numeric type.
func isNumber(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	}
	return false
}

// toFloat64 converts a numeric value to float64.
func toFloat64(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int8:
		return float64(n)
	case int16:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint8:
		return float64(n)
	case uint16:
		return float64(n)
	case uint32:
		return float64(n)
	case uint64:
		return float64(n)
	case float32:
		return float64(n)
	case float64:
		return n
	default:
		return 0
	}
}

// execCounter extracts span_attributes.exec_counter as a float64 for sorting.
func execCounter(span map[string]any) float64 {
	sa, ok := span["span_attributes"].(map[string]any)
	if !ok {
		return 0
	}
	if ec, ok := sa["exec_counter"]; ok {
		return toFloat64(ec)
	}
	return 0
}

// spanName extracts span_attributes.name from a span.
func spanName(span map[string]any) string {
	sa, ok := span["span_attributes"].(map[string]any)
	if !ok {
		return ""
	}
	if name, ok := sa["name"].(string); ok {
		return name
	}
	return ""
}
