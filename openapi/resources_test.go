package openapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// recordedClient records or replays testdata/cassettes/<test name>.
func recordedClient(t *testing.T) *openapi.Client {
	t.Helper()
	return https.NewGenerated(vcr.GetHTTPSClient(t))
}

// Check the raw operation response after its body has been decoded and closed.
func checkRecorded(t *testing.T, response *http.Response, err error) {
	t.Helper()
	require.NoError(t, err)
	require.NotNil(t, response)
	require.GreaterOrEqual(t, response.StatusCode, 200)
	require.Less(t, response.StatusCode, 300)
}

type recordedDecoder struct{ t *testing.T }

func (d recordedDecoder) object(response *http.Response, err error) recordedObject {
	d.t.Helper()
	require.NotNil(d.t, response)
	var value recordedObject
	decodeErr := json.NewDecoder(response.Body).Decode(&value)
	closeErr := response.Body.Close()
	checkRecorded(d.t, response, err)
	require.NoError(d.t, decodeErr)
	require.NoError(d.t, closeErr)
	return value
}

func (d recordedDecoder) objects(response *http.Response, err error) recordedObjects {
	d.t.Helper()
	require.NotNil(d.t, response)
	var value recordedObjects
	decodeErr := json.NewDecoder(response.Body).Decode(&value)
	closeErr := response.Body.Close()
	checkRecorded(d.t, response, err)
	require.NoError(d.t, decodeErr)
	require.NoError(d.t, closeErr)
	return value
}

func (d recordedDecoder) raw(response *http.Response, err error) json.RawMessage {
	d.t.Helper()
	require.NotNil(d.t, response)
	var value json.RawMessage
	decodeErr := json.NewDecoder(response.Body).Decode(&value)
	closeErr := response.Body.Close()
	checkRecorded(d.t, response, err)
	require.NoError(d.t, decodeErr)
	require.NoError(d.t, closeErr)
	return value
}

func recordedBody(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return bytes.NewReader(body)
}

// These are only JSON assertion targets, not endpoint or resource clients.
type recordedObject struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	ProjectID   string          `json:"project_id"`
	OrgID       string          `json:"org_id"`
	Description string          `json:"description"`
	Slug        string          `json:"slug"`
	XactID      string          `json:"_xact_id"`
	PromptData  json.RawMessage `json:"prompt_data"`
	Metadata    json.RawMessage `json:"metadata"`
	Tags        []string        `json:"tags"`
}

type recordedObjects struct {
	Objects []recordedObject `json:"objects"`
}

func TestGeneratedProjectLifecycle(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()

	created := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-lifecycle-test"}))
	require.NotEmpty(t, created.ID)
	require.NotEmpty(t, created.OrgID)
	retrieved := (recordedDecoder{t}).object(client.GetProjectId(ctx, created.ID))
	require.Equal(t, created, retrieved)
	// Filter by name so the cassette records only this test's project.
	listed := (recordedDecoder{t}).objects(client.GetProject(ctx, &openapi.GetProjectParams{ProjectName: https.String("go-sdk-lifecycle-test"), Limit: https.Positive(1)}))
	require.Contains(t, listed.Objects, created)
	idempotent := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-lifecycle-test"}))
	require.Equal(t, created.ID, idempotent.ID)
	(recordedDecoder{t}).raw(client.DeleteProjectId(ctx, created.ID))
}

func TestGeneratedProjectOrganizationFilter(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()
	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	// Filter by name so the cassette records only this test's project.
	listed := (recordedDecoder{t}).objects(client.GetProject(ctx, &openapi.GetProjectParams{ProjectName: https.String(project.Name), Limit: https.Positive(1)}, https.Query("org_id", project.OrgID)))
	require.Equal(t, []recordedObject{project}, listed.Objects)
}

func TestGeneratedExperimentLifecycle(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()

	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	created := (recordedDecoder{t}).object(client.PostExperimentWithBody(ctx, "application/json", recordedBody(t, map[string]any{
		"project_id": project.ID, "name": "lifecycle-exp", "description": "Test experiment for lifecycle testing",
		"tags": []string{"lifecycle", "test"}, "metadata": map[string]string{"stage": "created"},
	})))
	require.NotEmpty(t, created.ID)
	require.Equal(t, project.ID, created.ProjectID)
	require.Equal(t, "Test experiment for lifecycle testing", created.Description)
	require.Equal(t, []string{"lifecycle", "test"}, created.Tags)
	require.JSONEq(t, `{"stage":"created"}`, string(created.Metadata))
	require.Equal(t, created, (recordedDecoder{t}).object(client.GetExperimentId(ctx, created.ID)))
	listed := (recordedDecoder{t}).objects(client.GetExperiment(ctx, &openapi.GetExperimentParams{ProjectId: https.String(project.ID), Limit: https.Positive(100)}))
	require.Contains(t, listed.Objects, created)
	idempotent := (recordedDecoder{t}).object(client.PostExperimentWithBody(ctx, "application/json", recordedBody(t, map[string]any{"project_id": project.ID, "name": "lifecycle-exp"})))
	require.Equal(t, created.ID, idempotent.ID)
	(recordedDecoder{t}).raw(client.DeleteExperimentId(ctx, created.ID))
	response, err := client.GetExperimentId(ctx, created.ID)
	if response != nil {
		_ = response.Body.Close()
	}
	var httpError *https.HTTPError
	require.ErrorAs(t, err, &httpError)
}

func TestGeneratedExperimentEnsureNew(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()

	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	first := (recordedDecoder{t}).object(client.PostExperimentWithBody(ctx, "application/json", recordedBody(t, map[string]any{"project_id": project.ID, "name": "ensure-new-test"})))
	second := (recordedDecoder{t}).object(client.PostExperimentWithBody(ctx, "application/json", recordedBody(t, map[string]any{"project_id": project.ID, "name": "ensure-new-test", "ensure_new": true})))
	require.NotEqual(t, first.ID, second.ID)
	require.Contains(t, second.Name, "ensure-new-test")
}

func TestGeneratedFunctionLifecycle(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()

	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	created := (recordedDecoder{t}).object(client.PostFunctionWithBody(ctx, "application/json", recordedBody(t, map[string]any{
		"project_id": project.ID, "name": "lifecycle-function", "slug": "lifecycle-func",
		"description": "Test function for lifecycle testing", "function_data": map[string]string{"type": "prompt"},
	})))
	require.NotEmpty(t, created.ID)
	require.Equal(t, project.ID, created.ProjectID)
	require.Equal(t, "lifecycle-func", created.Slug)
	params := &openapi.GetFunctionParams{ProjectName: https.String("go-sdk-tests"), Slug: https.String("lifecycle-func"), Limit: https.Positive(1)}
	listed := (recordedDecoder{t}).objects(client.GetFunction(ctx, params))
	require.Len(t, listed.Objects, 1)
	require.Equal(t, created.ID, listed.Objects[0].ID)
	require.Equal(t, created.Name, listed.Objects[0].Name)
	byProject := (recordedDecoder{t}).objects(client.GetFunction(ctx, &openapi.GetFunctionParams{ProjectId: https.String(project.ID), Limit: https.Positive(100)}))
	require.Contains(t, byProject.Objects, listed.Objects[0])
	(recordedDecoder{t}).raw(client.DeleteFunctionId(ctx, created.ID))
	afterDelete := (recordedDecoder{t}).objects(client.GetFunction(ctx, params))
	require.Empty(t, afterDelete.Objects)
}

func TestGeneratedPromptRead(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()
	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	// PUT replaces any prompt left by an earlier recording.
	(recordedDecoder{t}).object(client.PutFunctionWithBody(ctx, "application/json", recordedBody(t, map[string]any{
		"project_id": project.ID, "name": "sdk-go-load-prompt", "slug": "sdk-go-load-prompt",
		"function_type": "prompt", "function_data": map[string]string{"type": "prompt"},
		"prompt_data": map[string]any{
			"prompt": map[string]any{"type": "chat", "messages": []map[string]string{
				{"role": "system", "content": "You greet people warmly."},
				{"role": "user", "content": "Say hello to {{name}}."},
			}},
			"options": map[string]any{"model": "gpt-4o-mini", "params": map[string]any{"temperature": 0}},
		},
	})))
	rows := (recordedDecoder{t}).objects(client.GetPrompt(ctx, &openapi.GetPromptParams{
		ProjectName: https.String("go-sdk-tests"), Slug: https.String("sdk-go-load-prompt"), Limit: https.Positive(1),
	}))
	require.Len(t, rows.Objects, 1)
	row := rows.Objects[0]
	require.Equal(t, "sdk-go-load-prompt", row.Slug)
	require.NotEmpty(t, row.ProjectID)
	require.NotEmpty(t, row.XactID)
	require.Contains(t, string(row.PromptData), "Say hello to {{name}}.")
	require.Contains(t, string(row.PromptData), "gpt-4o-mini")
	byID := (recordedDecoder{t}).object(client.GetPromptId(ctx, row.ID, nil))
	require.Equal(t, row.ID, byID.ID)
	require.JSONEq(t, string(row.PromptData), string(byID.PromptData))
}

func TestGeneratedPromptUnknownSlug(t *testing.T) {
	client := recordedClient(t)
	rows := (recordedDecoder{t}).objects(client.GetPrompt(context.Background(), &openapi.GetPromptParams{
		ProjectName: https.String("go-sdk-tests"), Slug: https.String("sdk-go-no-such-prompt"),
	}))
	require.Empty(t, rows.Objects)
}

type recordedEvents struct {
	Events []json.RawMessage `json:"events"`
	Cursor string            `json:"cursor"`
}

func (d recordedDecoder) events(response *http.Response, err error) recordedEvents {
	d.t.Helper()
	require.NotNil(d.t, response)
	var value recordedEvents
	decodeErr := json.NewDecoder(response.Body).Decode(&value)
	closeErr := response.Body.Close()
	checkRecorded(d.t, response, err)
	require.NoError(d.t, decodeErr)
	require.NoError(d.t, closeErr)
	return value
}

func TestGeneratedDatasetLifecycle(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()

	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	dataset := (recordedDecoder{t}).object(client.PostDatasetWithBody(ctx, "application/json", recordedBody(t, map[string]any{
		"project_id": project.ID, "name": "lifecycle-test", "description": "Full lifecycle test dataset",
	})))
	require.NotEmpty(t, dataset.ID)
	require.Equal(t, project.ID, dataset.ProjectID)
	require.Equal(t, "lifecycle-test", dataset.Name)
	query := (recordedDecoder{t}).objects(client.GetDataset(ctx, &openapi.GetDatasetParams{Limit: https.Positive(1)}, https.Query("id", dataset.ID)))
	require.Equal(t, []recordedObject{dataset}, query.Objects)
	var inserted struct {
		RowIDs []string `json:"row_ids"`
	}
	raw := (recordedDecoder{t}).raw(client.PostDatasetIdInsertWithBody(ctx, dataset.ID, "application/json", strings.NewReader(`{"events":[{"input":{"question":"What is 2+2?"},"expected":{"answer":"4"},"tags":["math","easy"]},{"input":{"question":"What is the capital of France?"},"expected":{"answer":"Paris"},"tags":["geography","easy"]}]}`)))
	require.NoError(t, json.Unmarshal(raw, &inserted))
	require.Len(t, inserted.RowIDs, 2)
	fetched := (recordedDecoder{t}).events(client.PostDatasetIdFetchWithBody(ctx, dataset.ID, "application/json", strings.NewReader(`{"limit":10}`)))
	answers := map[string]string{}
	for _, raw := range fetched.Events {
		var event struct {
			Input struct {
				Question string `json:"question"`
			} `json:"input"`
			Expected struct {
				Answer string `json:"answer"`
			} `json:"expected"`
		}
		require.NoError(t, json.Unmarshal(raw, &event))
		answers[event.Input.Question] = event.Expected.Answer
	}
	require.Equal(t, "4", answers["What is 2+2?"])
	require.Equal(t, "Paris", answers["What is the capital of France?"])
	(recordedDecoder{t}).raw(client.DeleteDatasetId(ctx, dataset.ID))
	afterDelete := (recordedDecoder{t}).objects(client.GetDataset(ctx, &openapi.GetDatasetParams{Limit: https.Positive(1)}, https.Query("id", dataset.ID)))
	require.Empty(t, afterDelete.Objects)
}

func TestGeneratedDatasetEventFields(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()
	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	dataset := (recordedDecoder{t}).object(client.PostDatasetWithBody(ctx, "application/json", recordedBody(t, map[string]string{"project_id": project.ID, "name": "test-dataset-fields"})))
	(recordedDecoder{t}).raw(client.PostDatasetIdInsertWithBody(ctx, dataset.ID, "application/json", strings.NewReader(`{"events":[{"input":{"question":"What is the meaning of life?","context":"philosophical"},"expected":{"answer":"42","confidence":0.95},"metadata":{"source":"test","model":"test-model"},"tags":["philosophy","test","integration"]}]}`)))
	fetched := (recordedDecoder{t}).events(client.PostDatasetIdFetchWithBody(ctx, dataset.ID, "application/json", strings.NewReader(`{"limit":10}`)))
	require.NotEmpty(t, fetched.Events)
	var event map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(fetched.Events[0], &event))
	for _, field := range []string{"id", "input", "expected", "metadata", "_xact_id", "created", "span_id", "root_span_id"} {
		require.NotEmpty(t, event[field], field)
		require.NotEqual(t, "null", string(event[field]), field)
	}
	require.JSONEq(t, `{"question":"What is the meaning of life?","context":"philosophical"}`, string(event["input"]))
	require.JSONEq(t, `{"answer":"42","confidence":0.95}`, string(event["expected"]))
	require.JSONEq(t, `["philosophy","test","integration"]`, string(event["tags"]))
	require.JSONEq(t, `"`+project.ID+`"`, string(event["project_id"]))
	require.JSONEq(t, `"`+dataset.ID+`"`, string(event["dataset_id"]))
}

func TestGeneratedDatasetFetchPagination(t *testing.T) {
	client := recordedClient(t)
	ctx := context.Background()
	project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
	dataset := (recordedDecoder{t}).object(client.PostDatasetWithBody(ctx, "application/json", recordedBody(t, map[string]string{"project_id": project.ID, "name": "test-dataset-pagination"})))
	(recordedDecoder{t}).raw(client.PostDatasetIdInsertWithBody(ctx, dataset.ID, "application/json", strings.NewReader(`{"events":[{"input":{"n":1}},{"input":{"n":2}},{"input":{"n":3}},{"input":{"n":4}}]}`)))
	first := (recordedDecoder{t}).events(client.PostDatasetIdFetchWithBody(ctx, dataset.ID, "application/json", strings.NewReader(`{"limit":2}`)))
	require.Len(t, first.Events, 2)
	require.NotEmpty(t, first.Cursor)
	second := (recordedDecoder{t}).events(client.PostDatasetIdFetchWithBody(ctx, dataset.ID, "application/json", recordedBody(t, map[string]any{"limit": 2, "cursor": first.Cursor})))
	require.NotEmpty(t, second.Events)
}

func TestGeneratedDatasetQueryFilters(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		datasetName string
		params      openapi.GetDatasetParams
	}{
		{"by_name", "test-dataset-modify", openapi.GetDatasetParams{DatasetName: https.String("test-dataset-modify"), Limit: https.Positive(10)}},
		{"by_project_name", "test-dataset-query", openapi.GetDatasetParams{ProjectName: https.String("go-sdk-tests"), Limit: https.Positive(10)}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client := recordedClient(t)
			ctx := context.Background()
			project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
			dataset := (recordedDecoder{t}).object(client.PostDatasetWithBody(ctx, "application/json", recordedBody(t, map[string]string{"project_id": project.ID, "name": scenario.datasetName})))
			rows := (recordedDecoder{t}).objects(client.GetDataset(ctx, &scenario.params))
			require.NotEmpty(t, rows.Objects)
			require.Contains(t, rows.Objects, dataset)
			for _, row := range rows.Objects {
				require.NotEmpty(t, row.ID)
				if scenario.params.DatasetName != nil {
					require.Contains(t, row.Name, *scenario.params.DatasetName)
				}
			}
		})
	}
	t.Run("project and pagination", func(t *testing.T) {
		client := recordedClient(t)
		ctx := context.Background()
		project := (recordedDecoder{t}).object(client.PostProject(ctx, openapi.CreateProject{Name: "go-sdk-tests"}))
		dataset := (recordedDecoder{t}).object(client.PostDatasetWithBody(ctx, "application/json", recordedBody(t, map[string]string{"project_id": project.ID, "name": "query-params-test"})))
		params := &openapi.GetDatasetParams{ProjectId: https.String(project.ID), Limit: https.Positive(5)}
		first := (recordedDecoder{t}).objects(client.GetDataset(ctx, params))
		require.NotEmpty(t, first.Objects)
		params.StartingAfter = https.String(first.Objects[0].ID)
		second := (recordedDecoder{t}).objects(client.GetDataset(ctx, params))
		require.NotEmpty(t, second.Objects)
		require.NotEqual(t, first.Objects[0].ID, second.Objects[0].ID)
		filtered := (recordedDecoder{t}).objects(client.GetDataset(ctx, &openapi.GetDatasetParams{ProjectId: https.String(project.ID), DatasetName: https.String(dataset.Name), Limit: https.Positive(1)}))
		require.Equal(t, []recordedObject{dataset}, filtered.Objects)
	})
}
