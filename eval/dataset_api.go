package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/braintrustdata/braintrust-sdk-go/api"
)

// DatasetAPI provides methods for loading datasets for evaluation.
// It wraps api.DatasetsClient to provide typed Case[I, R] conversion and pagination handling.
type DatasetAPI[I, R any] struct {
	apiClient *api.API
}

// DatasetQueryOpts contains options for querying datasets.
type DatasetQueryOpts struct {
	// Name is the dataset name (requires project context)
	Name string

	// ID is the dataset ID (direct lookup)
	ID string

	// Version specifies a specific dataset version
	Version string

	// Limit specifies the maximum number of records to return (0 = unlimited)
	Limit int
}

// Get loads a dataset by ID and returns a Cases iterator.
func (d *DatasetAPI[I, R]) Get(ctx context.Context, id string) (Cases[I, R], error) {
	if id == "" {
		return nil, fmt.Errorf("dataset ID is required")
	}

	return &datasetIterator[I, R]{
		dataset: newDataset(id, 0, d.apiClient.Datasets()), // 0 = no limit
	}, nil
}

// Query loads a dataset with advanced query options.
func (d *DatasetAPI[I, R]) Query(ctx context.Context, opts DatasetQueryOpts) (Cases[I, R], error) {
	// If ID is provided directly, use it
	if opts.ID != "" {
		return &datasetIterator[I, R]{
			dataset: newDataset(opts.ID, opts.Limit, d.apiClient.Datasets()),
		}, nil
	}

	// Otherwise query for datasets using api.Client
	params := map[string]string{
		"limit": "1", // Only get the most recent
	}
	if opts.Name != "" {
		params["dataset_name"] = opts.Name
	}
	if opts.Version != "" {
		params["version"] = opts.Version
	}

	response, err := d.apiClient.Datasets().Query(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("failed to query datasets: %w", err)
	}

	if len(response.Objects) == 0 {
		return nil, fmt.Errorf("no datasets found matching the criteria")
	}

	// Return the first (most recent) dataset
	return &datasetIterator[I, R]{
		dataset: newDataset(response.Objects[0].ID, opts.Limit, d.apiClient.Datasets()),
	}, nil
}

// dataset handles fetching events from Braintrust with pagination.
// It maintains pagination state and calls api.DatasetsClient for each page.
type dataset struct {
	datasetID      string
	events         []json.RawMessage
	index          int
	cursor         string
	exhausted      bool
	maxRecords     int
	recordCount    int
	datasetsClient *api.DatasetsClient
}

// newDataset creates a new dataset iterator
func newDataset(datasetID string, maxRecords int, datasetsClient *api.DatasetsClient) *dataset {
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

// fetchNextBatch retrieves the next batch of events using api.DatasetsClient
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

	// Use api.DatasetsClient.Fetch() to get the next page
	result, err := d.datasetsClient.Fetch(context.Background(), d.datasetID, d.cursor, batchSize)
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

// datasetIterator implements Cases[I, R] for dataset events
type datasetIterator[I, R any] struct {
	dataset *dataset
}

// Next returns the next case from the dataset
func (di *datasetIterator[I, R]) Next() (Case[I, R], error) {
	var fullEvent struct {
		Input    I        `json:"input"`
		Expected R        `json:"expected"`
		Tags     []string `json:"tags"`
		Metadata Metadata `json:"metadata"`
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
	}, nil
}
