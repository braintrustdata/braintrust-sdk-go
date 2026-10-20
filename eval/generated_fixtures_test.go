package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixtures decode only the identity fields used to arrange and inspect tests.
// Requests still go directly through the generated operations and VCR transport.
type fixtureObject struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
}

type fixtureDatasetEvent struct {
	Input    any            `json:"input,omitempty"`
	Expected any            `json:"expected,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Tags     []string       `json:"tags,omitempty"`
}

func fixtureBody(t *testing.T, value any) io.Reader {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return bytes.NewReader(body)
}

func decodeFixtureResponse(response *http.Response, err error) (*fixtureObject, error) {
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	var value fixtureObject
	decodeErr := json.NewDecoder(response.Body).Decode(&value)
	return &value, errors.Join(decodeErr, response.Body.Close())
}

func decodeFixtureObjects(response *http.Response, err error) ([]fixtureObject, error) {
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	var body struct {
		Objects []fixtureObject `json:"objects"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&body)
	return body.Objects, errors.Join(decodeErr, response.Body.Close())
}

func closeFixtureResponse(response *http.Response, err error) error {
	if response != nil {
		closeErr := response.Body.Close()
		if err == nil {
			err = closeErr
		}
	}
	return err
}
