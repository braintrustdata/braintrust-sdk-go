package https_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// Observe actual requests before replaying real recorded API responses.
// This does not synthesize a server response or bypass the configured transport.
type observedTransport struct {
	next    http.RoundTripper
	request *http.Request
	body    []byte
}

func (o *observedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	o.request = req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer func() { _ = body.Close() }()
		o.body, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}
	return o.next.RoundTrip(req)
}

// debugLog captures debug messages written by the SDK transport.
type debugLog struct{ entries []string }

func (l *debugLog) Debug(msg string, args ...any) {
	l.entries = append(l.entries, fmt.Sprint(msg, args))
}
func (l *debugLog) Info(string, ...any)  {}
func (l *debugLog) Warn(string, ...any)  {}
func (l *debugLog) Error(string, ...any) {}

// observedClient records or replays the test's cassette behind an observer.
func observedClient(t *testing.T, log logger.Logger) (*openapi.Client, *observedTransport, string) {
	t.Helper()
	recorded := vcr.GetHTTPSClient(t)
	observed := &observedTransport{next: recorded.Client().Transport}
	client := https.NewGenerated(https.NewWrappedClient(recorded.APIKey(), recorded.BaseURL(), &http.Client{Transport: observed}, log))
	return client, observed, recorded.APIKey()
}

func TestGeneratedTransportPreservesAuthAndJSON(t *testing.T) {
	log := &debugLog{}
	client, observed, apiKey := observedClient(t, log)
	response, err := client.PostProjectWithBody(context.Background(), "application/json", strings.NewReader(`{"name":"go-sdk-tests"}`))
	require.NoError(t, err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "Bearer "+apiKey, observed.request.Header.Get("Authorization"))
	require.Equal(t, "application/json", observed.request.Header.Get("Content-Type"))
	require.JSONEq(t, `{"name":"go-sdk-tests"}`, string(observed.body))
	require.Contains(t, log.entries, `http request body[body {"name":"go-sdk-tests"}]`)
}

func TestGeneratedTransportPreservesServerErrors(t *testing.T) {
	client, _, _ := observedClient(t, nil)
	response, err := client.GetProjectId(context.Background(), "non-existent-project-id-12345")
	if response != nil {
		_ = response.Body.Close()
	}
	var responseError *https.HTTPError
	require.ErrorAs(t, err, &responseError)
	require.Equal(t, http.StatusBadRequest, responseError.StatusCode)
	require.Contains(t, responseError.Body, `"validation": "uuid"`)
}

func TestGeneratedRequestsPreservePrefixAndExtraFilters(t *testing.T) {
	client := https.NewGenerated(https.NewClient("unused", "https://example.invalid/deployment/api", nil))
	stop := errors.New("inspect request without sending")
	response, err := client.GetDataset(context.Background(), &openapi.GetDatasetParams{
		ProjectId: https.String("UPPERCASE-OR-OPAQUE-ID"),
		Limit:     https.Positive(3),
	}, https.Query("id", "dataset-id"), https.Query("version", "v1 & v2"), func(_ context.Context, req *http.Request) error {
		require.Equal(t, "/deployment/api/v1/dataset", req.URL.Path)
		require.Equal(t, "UPPERCASE-OR-OPAQUE-ID", req.URL.Query().Get("project_id"))
		require.Equal(t, "3", req.URL.Query().Get("limit"))
		require.Equal(t, "dataset-id", req.URL.Query().Get("id"))
		require.Equal(t, "v1 & v2", req.URL.Query().Get("version"))
		return stop
	})
	if response != nil {
		_ = response.Body.Close()
	}
	require.ErrorIs(t, err, stop)
}
