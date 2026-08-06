package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func TestHealthEndpoint(t *testing.T) {
	srv := New(WithNoAuth())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "ok", body["status"])
}

func TestListEndpoint_Empty(t *testing.T) {
	srv := New(WithNoAuth())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/list")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Empty(t, body)
}

func TestListEndpoint_WithEvaluators(t *testing.T) {
	srv := New(WithNoAuth())

	task := eval.T(func(ctx context.Context, input string) (string, error) {
		return input, nil
	})
	scorer := eval.NewScorer("exact_match", func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
		if r.Output == r.Expected {
			return eval.S(1.0), nil
		}
		return eval.S(0.0), nil
	})

	RegisterEval(srv, &eval.Eval[string, string]{
		Name:    "my-eval",
		Task:    task,
		Scorers: []eval.Scorer[string, string]{scorer},
	}, RegisterEvalOpts{})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/list")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body, "my-eval")

	var info evalInfo
	require.NoError(t, json.Unmarshal(body["my-eval"], &info))
	require.Len(t, info.Scores, 1)
	assert.Equal(t, "exact_match", info.Scores[0].Name)
}

func TestListEndpoint_WithParameters(t *testing.T) {
	srv := New(WithNoAuth())

	task := eval.T(func(ctx context.Context, input string) (string, error) {
		return input, nil
	})
	scorer := eval.NewScorer("score", func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
		return eval.S(1.0), nil
	})

	RegisterEval(srv, &eval.Eval[string, string]{
		Name:    "param-eval",
		Task:    task,
		Scorers: []eval.Scorer[string, string]{scorer},
	}, RegisterEvalOpts{
		Parameters: &Parameters{
			Schema: map[string]ParameterDef{
				"model": {
					Type:        "string",
					Default:     "gpt-4",
					Description: "Model to use",
				},
			},
		},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/list")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	var body map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	var info struct {
		Parameters *parametersMeta `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(body["param-eval"], &info))
	require.NotNil(t, info.Parameters)
	assert.Equal(t, "braintrust.staticParameters", info.Parameters.Type)
	assert.Contains(t, info.Parameters.Schema, "model")

	// Verify wire format: each parameter must have type "data" and nested schema.
	modelParam := info.Parameters.Schema["model"]
	assert.Equal(t, "data", modelParam.Type)
	assert.Equal(t, "string", modelParam.Schema.Type)
	assert.Equal(t, "gpt-4", modelParam.Default)
	assert.Equal(t, "Model to use", modelParam.Description)
}

func TestListEndpoint_ModelParameter(t *testing.T) {
	srv := New(WithNoAuth())

	task := eval.T(func(ctx context.Context, input string) (string, error) {
		return input, nil
	})
	scorer := eval.NewScorer("score", func(ctx context.Context, r eval.TaskResult[string, string]) (eval.Scores, error) {
		return eval.S(1.0), nil
	})

	RegisterEval(srv, &eval.Eval[string, string]{
		Name:    "model-eval",
		Task:    task,
		Scorers: []eval.Scorer[string, string]{scorer},
	}, RegisterEvalOpts{
		Parameters: &Parameters{
			Schema: map[string]ParameterDef{
				"model": {Type: "model", Default: "gpt-4o", Description: "Model picker"},
			},
		},
	})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/list")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	var body map[string]json.RawMessage
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))

	var info struct {
		Parameters *parametersMeta `json:"parameters"`
	}
	require.NoError(t, json.Unmarshal(body["model-eval"], &info))
	require.NotNil(t, info.Parameters)

	// A model parameter uses the union's "model" shape: top-level type "model",
	// no nested schema object.
	modelParam := info.Parameters.Schema["model"]
	assert.Equal(t, "model", modelParam.Type)
	assert.Nil(t, modelParam.Schema, "model params must not carry a nested schema")
	assert.Equal(t, "gpt-4o", modelParam.Default)
	assert.Equal(t, "Model picker", modelParam.Description)
}

func TestEvalEndpoint_MissingName(t *testing.T) {
	srv := New(WithNoAuth())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/eval", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestEvalEndpoint_NotFound(t *testing.T) {
	srv := New(WithNoAuth())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := `{"name":"nonexistent","data":{"data":[]}}`
	resp, err := http.Post(ts.URL+"/eval", "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestEvalEndpoint_InvalidJSON(t *testing.T) {
	srv := New(WithNoAuth())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/eval", "application/json", strings.NewReader(`not json`))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestServerNew_Defaults(t *testing.T) {
	srv := New()
	assert.Equal(t, "localhost:8300", srv.addr)
	assert.Equal(t, "https://www.braintrust.dev", srv.appURL)
	assert.False(t, srv.noAuth)
	assert.NotNil(t, srv.evaluators)
	assert.NotNil(t, srv.authCache)
}

func TestServerNew_Options(t *testing.T) {
	srv := New(
		WithAddress(":9000"),
		WithAppURL("https://custom.example.com"),
		WithNoAuth(),
	)
	assert.Equal(t, ":9000", srv.addr)
	assert.Equal(t, "https://custom.example.com", srv.appURL)
	assert.True(t, srv.noAuth)
}

func TestServerShutdown_NilServer(t *testing.T) {
	srv := New()
	err := srv.Shutdown(context.Background())
	assert.NoError(t, err)
}

func TestListEndpoint_POST(t *testing.T) {
	srv := New(WithNoAuth())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/list", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
