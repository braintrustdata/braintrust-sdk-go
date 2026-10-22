package datasets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func TestCompatibilityInsertPreservesArbitraryJSON(t *testing.T) {
	client := New(https.NewClient("unused", "https://example.invalid", nil))
	stop := errors.New("inspect generated request before network I/O")
	// Inspect the real generated request before sending it. This tests wire
	// serialization, not a synthetic API response.
	origin := Origin{ObjectType: "dataset", ObjectID: "dataset-123", ID: "event-456", Created: "2024-01-15T10:30:00Z", XactID: "xact-789"}
	client.client.RequestEditors = []openapi.RequestEditorFn{func(_ context.Context, req *http.Request) error {
		body, err := req.GetBody()
		require.NoError(t, err)
		defer func() { _ = body.Close() }()
		encoded, err := io.ReadAll(body)
		require.NoError(t, err)
		var document struct {
			Events []struct {
				Input    json.RawMessage `json:"input"`
				Expected json.RawMessage `json:"expected"`
				IsMerge  *bool           `json:"_is_merge"`
				Origin   *Origin         `json:"origin"`
			} `json:"events"`
		}
		require.NoError(t, json.Unmarshal(encoded, &document))
		require.Len(t, document.Events, 1)
		// Decode without float64 conversion so precision loss is observable.
		var input map[string]any
		decoder := json.NewDecoder(bytes.NewReader(document.Events[0].Input))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&input))
		require.Equal(t, json.Number("9007199254740993"), input["number"])
		require.Equal(t, []any{true, nil}, input["future_field"])
		require.Equal(t, "null", string(document.Events[0].Expected))
		require.NotNil(t, document.Events[0].IsMerge)
		require.False(t, *document.Events[0].IsMerge)
		require.Equal(t, &origin, document.Events[0].Origin)
		return stop
	}}
	merge := false
	err := client.InsertEvents(context.Background(), "opaque-dataset-id", []Event{{
		Input:    json.RawMessage(`{"number":9007199254740993,"future_field":[true,null]}`),
		Expected: json.RawMessage(`null`),
		IsMerge:  &merge,
		Origin:   &origin,
	}})
	require.ErrorIs(t, err, stop)
}
