package eval

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test types for conversion tests
type TestStruct struct {
	Answer string `json:"answer"`
}

type TestComplexStruct struct {
	Name  string `json:"name"`
	Age   int    `json:"age"`
	Email string `json:"email"`
}

type CustomString string
type CustomInt int
type CustomFloat float64

// TestConvertToType_DirectTypeMatch tests direct type assertion
func TestConvertToType_DirectTypeMatch(t *testing.T) {
	t.Run("string to string", func(t *testing.T) {
		result, err := convertToType[string]("hello")
		require.NoError(t, err)
		assert.Equal(t, "hello", result)
	})

	t.Run("int to int", func(t *testing.T) {
		result, err := convertToType[int](42)
		require.NoError(t, err)
		assert.Equal(t, 42, result)
	})

	t.Run("bool to bool", func(t *testing.T) {
		result, err := convertToType[bool](true)
		require.NoError(t, err)
		assert.Equal(t, true, result)
	})

	t.Run("struct to struct", func(t *testing.T) {
		input := TestStruct{Answer: "test"}
		result, err := convertToType[TestStruct](input)
		require.NoError(t, err)
		assert.Equal(t, input, result)
	})
}

// TestConvertToType_StringToString tests string type conversions
func TestConvertToType_StringToString(t *testing.T) {
	t.Run("plain string to string", func(t *testing.T) {
		result, err := convertToType[string]("plain text")
		require.NoError(t, err)
		assert.Equal(t, "plain text", result)
	})

	t.Run("custom string type", func(t *testing.T) {
		result, err := convertToType[CustomString]("custom")
		require.NoError(t, err)
		assert.Equal(t, CustomString("custom"), result)
	})

	t.Run("number string to string", func(t *testing.T) {
		result, err := convertToType[string]("42")
		require.NoError(t, err)
		assert.Equal(t, "42", result)
	})
}

// TestConvertToType_JSONStringToStruct tests parsing JSON strings into structs
func TestConvertToType_JSONStringToStruct(t *testing.T) {
	t.Run("simple JSON to struct", func(t *testing.T) {
		jsonStr := `{"answer":"test answer"}`
		result, err := convertToType[TestStruct](jsonStr)
		require.NoError(t, err)
		assert.Equal(t, "test answer", result.Answer)
	})

	t.Run("complex JSON to struct", func(t *testing.T) {
		jsonStr := `{"name":"Alice","age":30,"email":"alice@example.com"}`
		result, err := convertToType[TestComplexStruct](jsonStr)
		require.NoError(t, err)
		assert.Equal(t, "Alice", result.Name)
		assert.Equal(t, 30, result.Age)
		assert.Equal(t, "alice@example.com", result.Email)
	})

	t.Run("invalid JSON to struct should fail", func(t *testing.T) {
		jsonStr := `{invalid json}`
		_, err := convertToType[TestStruct](jsonStr)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal JSON string")
	})

	t.Run("JSON with extra fields", func(t *testing.T) {
		jsonStr := `{"answer":"test","extra":"ignored"}`
		result, err := convertToType[TestStruct](jsonStr)
		require.NoError(t, err)
		assert.Equal(t, "test", result.Answer)
	})

	t.Run("JSON with missing fields uses zero values", func(t *testing.T) {
		jsonStr := `{"name":"Bob"}`
		result, err := convertToType[TestComplexStruct](jsonStr)
		require.NoError(t, err)
		assert.Equal(t, "Bob", result.Name)
		assert.Equal(t, 0, result.Age)
		assert.Equal(t, "", result.Email)
	})
}

// TestConvertToType_MapToStruct tests converting map[string]any to structs
func TestConvertToType_MapToStruct(t *testing.T) {
	t.Run("simple map to struct", func(t *testing.T) {
		input := map[string]any{"answer": "test answer"}
		result, err := convertToType[TestStruct](input)
		require.NoError(t, err)
		assert.Equal(t, "test answer", result.Answer)
	})

	t.Run("complex map to struct", func(t *testing.T) {
		input := map[string]any{
			"name":  "Alice",
			"age":   30,
			"email": "alice@example.com",
		}
		result, err := convertToType[TestComplexStruct](input)
		require.NoError(t, err)
		assert.Equal(t, "Alice", result.Name)
		assert.Equal(t, 30, result.Age)
		assert.Equal(t, "alice@example.com", result.Email)
	})

	t.Run("map with extra fields", func(t *testing.T) {
		input := map[string]any{
			"answer": "test",
			"extra":  "ignored",
		}
		result, err := convertToType[TestStruct](input)
		require.NoError(t, err)
		assert.Equal(t, "test", result.Answer)
	})

	t.Run("map with wrong types should handle gracefully", func(t *testing.T) {
		input := map[string]any{
			"name": "Alice",
			"age":  "not a number", // wrong type
		}
		result, err := convertToType[TestComplexStruct](input)
		// Should fail because age can't be converted from string
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal")
		_ = result
	})
}

// TestConvertToType_NilInput tests handling of nil inputs
func TestConvertToType_NilInput(t *testing.T) {
	t.Run("nil to string", func(t *testing.T) {
		result, err := convertToType[string](nil)
		require.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("nil to int", func(t *testing.T) {
		result, err := convertToType[int](nil)
		require.NoError(t, err)
		assert.Equal(t, 0, result)
	})

	t.Run("nil to struct", func(t *testing.T) {
		result, err := convertToType[TestStruct](nil)
		require.NoError(t, err)
		assert.Equal(t, TestStruct{}, result)
	})
}

// TestConvertToType_EdgeCases tests edge cases
func TestConvertToType_EdgeCases(t *testing.T) {
	t.Run("empty string to struct should fail", func(t *testing.T) {
		_, err := convertToType[TestStruct]("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal JSON string")
	})

	t.Run("empty string to string", func(t *testing.T) {
		result, err := convertToType[string]("")
		require.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("empty map to struct", func(t *testing.T) {
		input := map[string]any{}
		result, err := convertToType[TestStruct](input)
		require.NoError(t, err)
		assert.Equal(t, TestStruct{}, result)
	})

	t.Run("nested struct from map", func(t *testing.T) {
		type Nested struct {
			Inner TestStruct `json:"inner"`
		}
		input := map[string]any{
			"inner": map[string]any{
				"answer": "nested answer",
			},
		}
		result, err := convertToType[Nested](input)
		require.NoError(t, err)
		assert.Equal(t, "nested answer", result.Inner.Answer)
	})

	t.Run("plain number string that looks like JSON number", func(t *testing.T) {
		// When the LLM returns just "4", it's a plain string "4"
		// But JSON parsing sees it as a number, which fails to unmarshal to a struct
		_, err := convertToType[TestStruct]("4")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal JSON string")
	})
}

// TestConvertToType_CustomPrimitiveTypes tests custom type aliases
func TestConvertToType_CustomPrimitiveTypes(t *testing.T) {
	t.Run("custom int from string JSON", func(t *testing.T) {
		result, err := convertToType[CustomInt]("42")
		require.NoError(t, err)
		assert.Equal(t, CustomInt(42), result)
	})

	t.Run("custom float from string JSON", func(t *testing.T) {
		result, err := convertToType[CustomFloat]("3.14")
		require.NoError(t, err)
		assert.InDelta(t, 3.14, float64(result), 0.01)
	})

	t.Run("custom string from plain string", func(t *testing.T) {
		result, err := convertToType[CustomString]("hello world")
		require.NoError(t, err)
		assert.Equal(t, CustomString("hello world"), result)
	})

	t.Run("int from string JSON", func(t *testing.T) {
		result, err := convertToType[int]("42")
		require.NoError(t, err)
		assert.Equal(t, 42, result)
	})

	t.Run("float from string JSON", func(t *testing.T) {
		result, err := convertToType[float64]("3.14159")
		require.NoError(t, err)
		assert.InDelta(t, 3.14159, result, 0.00001)
	})

	t.Run("int from number", func(t *testing.T) {
		result, err := convertToType[int](42)
		require.NoError(t, err)
		assert.Equal(t, 42, result)
	})

	t.Run("float from number", func(t *testing.T) {
		result, err := convertToType[float64](3.14)
		require.NoError(t, err)
		assert.Equal(t, 3.14, result)
	})
}

// TestConvertToType_Arrays tests array conversions
func TestConvertToType_Arrays(t *testing.T) {
	t.Run("string slice from JSON", func(t *testing.T) {
		jsonStr := `["a","b","c"]`
		result, err := convertToType[[]string](jsonStr)
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b", "c"}, result)
	})

	t.Run("int slice from JSON", func(t *testing.T) {
		jsonStr := `[1,2,3]`
		result, err := convertToType[[]int](jsonStr)
		require.NoError(t, err)
		assert.Equal(t, []int{1, 2, 3}, result)
	})

	t.Run("struct slice from JSON", func(t *testing.T) {
		jsonStr := `[{"answer":"a"},{"answer":"b"}]`
		result, err := convertToType[[]TestStruct](jsonStr)
		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, "a", result[0].Answer)
		assert.Equal(t, "b", result[1].Answer)
	})
}

// TestConvertToType_BooleanValues tests boolean conversions
func TestConvertToType_BooleanValues(t *testing.T) {
	t.Run("bool from JSON string true", func(t *testing.T) {
		result, err := convertToType[bool]("true")
		require.NoError(t, err)
		assert.Equal(t, true, result)
	})

	t.Run("bool from JSON string false", func(t *testing.T) {
		result, err := convertToType[bool]("false")
		require.NoError(t, err)
		assert.Equal(t, false, result)
	})

	t.Run("bool from bool", func(t *testing.T) {
		result, err := convertToType[bool](true)
		require.NoError(t, err)
		assert.Equal(t, true, result)
	})
}

// TestConvertToType_InterfaceValues tests any/interface{} handling
func TestConvertToType_InterfaceValues(t *testing.T) {
	t.Run("any from string", func(t *testing.T) {
		result, err := convertToType[any]("hello")
		require.NoError(t, err)
		assert.Equal(t, "hello", result)
	})

	t.Run("any from map", func(t *testing.T) {
		input := map[string]any{"key": "value"}
		result, err := convertToType[any](input)
		require.NoError(t, err)
		assert.Equal(t, input, result)
	})

	t.Run("any from number", func(t *testing.T) {
		result, err := convertToType[any](42)
		require.NoError(t, err)
		assert.Equal(t, 42, result)
	})
}
