package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/oapi-codegen/nullable"

	"github.com/braintrustdata/braintrust-sdk-go/internal/https"
	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

// datasetList decodes only the dataset IDs used to resolve a dataset query.
type datasetList struct {
	Objects []struct {
		ID string `json:"id"`
	} `json:"objects"`
}

// datasetEvents keeps rows raw so callers decode their own types.
type datasetEvents struct {
	Events []json.RawMessage `json:"events"`
	Cursor string            `json:"cursor"`
}

// DatasetAPI provides methods for loading datasets with automatic type conversion
// so they can be easily used in evals.
type DatasetAPI[I, R any] struct {
	api *openapi.Client
}

// DatasetQueryOpts contains options for querying datasets.
type DatasetQueryOpts struct {
	// Name is the dataset name (requires project context)
	Name string

	// ID is the dataset ID
	ID string

	// ProjectID scopes a Name lookup to one project. Dataset names are only
	// unique within a project, so a Name query without ProjectID or
	// ProjectName can match a dataset in an unrelated project.
	ProjectID string

	// ProjectName scopes a Name lookup to one project, as an alternative to
	// ProjectID.
	ProjectName string

	// Version specifies a specific dataset version
	Version string

	// Limit specifies the maximum number of records to return (0 = unlimited)
	Limit int
}

// Get loads a dataset by ID and returns a Dataset iterator.
func (d *DatasetAPI[I, R]) Get(ctx context.Context, id string) (Dataset[I, R], error) {
	if id == "" {
		return nil, fmt.Errorf("dataset ID is required")
	}

	return &datasetIterator[I, R]{
		dataset: newDataset(id, 0, d.api), // 0 = no limit
		id:      id,
		version: "", // Unknown when loading by ID directly
	}, nil
}

// Query loads a dataset with advanced query options.
func (d *DatasetAPI[I, R]) Query(ctx context.Context, opts DatasetQueryOpts) (Dataset[I, R], error) {
	// If ID is provided directly, use Get
	if opts.ID != "" {
		return &datasetIterator[I, R]{
			dataset: newDataset(opts.ID, opts.Limit, d.api),
			id:      opts.ID,
			version: opts.Version,
		}, nil
	}

	queryParams := &openapi.GetDatasetParams{
		Limit:       https.Positive(1), // Only get the most recent
		DatasetName: https.String(opts.Name),
		ProjectId:   https.String(opts.ProjectID),
		ProjectName: https.String(opts.ProjectName),
	}
	response, err := https.DecodeJSON[datasetList](d.api.GetDataset(ctx, queryParams, https.Query("version", opts.Version))) //nolint:bodyclose // false positive: DecodeJSON closes the body
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	if len(response.Objects) == 0 {
		return nil, fmt.Errorf("no datasets found matching the criteria")
	}

	// Return the first (most recent) dataset with full metadata
	ds := response.Objects[0]
	return &datasetIterator[I, R]{
		dataset: newDataset(ds.ID, opts.Limit, d.api),
		id:      ds.ID,
		version: opts.Version,
	}, nil
}

// dataset handles fetching events from Braintrust with pagination.
// It retains raw events until each event is decoded into the caller's types.
type dataset struct {
	datasetID      string
	events         []json.RawMessage
	index          int
	cursor         string
	exhausted      bool
	maxRecords     int
	recordCount    int
	datasetsClient *openapi.Client
}

// newDataset creates a new dataset iterator
func newDataset(datasetID string, maxRecords int, datasetsClient *openapi.Client) *dataset {
	return &dataset{
		datasetID:      datasetID,
		maxRecords:     maxRecords,
		datasetsClient: datasetsClient,
	}
}

// nextAs fetches the next event and unmarshals into target
func (d *dataset) nextAs(target interface{}) error {
	if d.maxRecords > 0 && d.recordCount >= d.maxRecords {
		return io.EOF
	}

	if d.index >= len(d.events) && !d.exhausted {
		if err := d.fetchNextBatch(); err != nil {
			return err
		}
	}

	if d.index >= len(d.events) {
		return io.EOF
	}

	if err := json.Unmarshal(d.events[d.index], target); err != nil {
		return fmt.Errorf("failed to unmarshal event: %w", err)
	}

	d.index++
	d.recordCount++
	return nil
}

// fetchNextBatch retrieves the next page of raw dataset events.
func (d *dataset) fetchNextBatch() error {
	batchSize := 100

	if d.maxRecords > 0 {
		remaining := d.maxRecords - d.recordCount
		if remaining <= 0 {
			d.exhausted = true
			return nil
		}
		if remaining < batchSize {
			batchSize = remaining
		}
	}

	if d.datasetID == "" {
		return fmt.Errorf("failed to fetch dataset events: dataset ID is required")
	}
	request := openapi.FetchEventsRequest{Limit: nullable.NewNullableWithValue(batchSize)}
	if d.cursor != "" {
		request.Cursor = nullable.NewNullableWithValue(d.cursor)
	}
	result, err := https.DecodeJSON[datasetEvents](d.datasetsClient.PostDatasetIdFetch(context.Background(), d.datasetID, request)) //nolint:bodyclose // false positive: DecodeJSON closes the body
	if err != nil {
		return fmt.Errorf("failed to fetch dataset events: %w", err)
	}

	d.events = result.Events
	d.index = 0
	d.cursor = result.Cursor

	if result.Cursor == "" || len(result.Events) == 0 {
		d.exhausted = true
	}

	return nil
}

// datasetIterator implements Dataset[I, R] for dataset events
type datasetIterator[I, R any] struct {
	dataset *dataset
	id      string
	version string
}

// Next returns the next case from the dataset
func (di *datasetIterator[I, R]) Next() (Case[I, R], error) {
	var fullEvent struct {
		Input    I        `json:"input"`
		Expected R        `json:"expected"`
		Tags     []string `json:"tags"`
		Metadata Metadata `json:"metadata"`
		// Dataset-specific fields for linking eval results back to dataset rows
		ID      string `json:"id"`
		XactID  string `json:"_xact_id"`
		Created string `json:"created"`
	}

	err := di.dataset.nextAs(&fullEvent)
	if err != nil {
		var zero Case[I, R]
		return zero, err
	}

	return Case[I, R]{
		Input:    fullEvent.Input,
		Expected: fullEvent.Expected,
		Tags:     fullEvent.Tags,
		Metadata: fullEvent.Metadata,
		ID:       fullEvent.ID,
		XactID:   fullEvent.XactID,
		Created:  fullEvent.Created,
	}, nil
}

// ID returns the dataset ID.
func (di *datasetIterator[I, R]) ID() string {
	return di.id
}

// Version returns the dataset version.
func (di *datasetIterator[I, R]) Version() string {
	return di.version
}
