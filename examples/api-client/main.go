// This example only reads existing resources unless you uncomment a lifecycle call.
// See docs/api-client.md for configuration and write permissions.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

const pageSize = 5

func main() {
	projectID := flag.String("project-id", "", "existing project ID (defaults to the first accessible project)")
	flag.Parse()
	if err := run(*projectID); err != nil {
		log.Fatal(err)
	}
}

func run(projectID string) error {
	key := os.Getenv("BRAINTRUST_API_KEY")
	if key == "" {
		return fmt.Errorf("BRAINTRUST_API_KEY is required")
	}
	baseURL := os.Getenv("BRAINTRUST_API_URL")
	if baseURL == "" {
		baseURL = "https://api.braintrust.dev"
	}
	client, err := openapi.NewClientWithResponses(baseURL,
		openapi.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		openapi.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+key)
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("configure generated client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if projectID == "" {
		limit := 1
		result, err := client.GetProjectWithResponse(ctx, &openapi.GetProjectParams{Limit: &limit})
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		if err := checkResponse("list projects", result.StatusCode(), result.JSON200 != nil, result.Body); err != nil {
			return err
		}
		if len(result.JSON200.Objects) == 0 {
			fmt.Println("No accessible projects. Nothing was created.")
			return nil
		}
		projectID = result.JSON200.Objects[0].Id.String()
	}
	result, err := client.GetProjectIdWithResponse(ctx, projectID)
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if err := checkResponse("get project", result.StatusCode(), result.JSON200 != nil, result.Body); err != nil {
		return err
	}
	project := result.JSON200
	fmt.Printf("Project: %s (%s)\n", project.Name, project.Id)
	if err := listResources(ctx, client, project.Id.String()); err != nil {
		return err
	}

	// Writes are deliberately disabled. To try full dataset-row CRUD, uncomment:
	// if err := DatasetLifecycle(ctx, client, project.Id.String()); err != nil {
	// 	return err
	// }
	// To try full automation CRUD (paused, so no webhook is delivered), uncomment:
	// if err := AutomationLifecycle(ctx, client, project.Id.String()); err != nil {
	// 	return err
	// }
	return nil
}

// Generated response methods return transport/decoding errors separately from
// HTTP errors. Every operation below also requires its decoded success body.
func checkResponse(operation string, status int, hasJSON bool, body []byte) error {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("%s: HTTP %d: %s", operation, status, body)
	}
	if !hasJSON {
		return fmt.Errorf("%s: HTTP %d without the expected JSON response", operation, status)
	}
	return nil
}

func listResources(ctx context.Context, client *openapi.ClientWithResponses, projectID string) error {
	limit := pageSize
	experiments, err := client.GetExperimentWithResponse(ctx, &openapi.GetExperimentParams{
		ProjectId: &projectID, Limit: &limit,
	})
	if err != nil {
		return fmt.Errorf("list experiments: %w", err)
	}
	if err := checkResponse("list experiments", experiments.StatusCode(), experiments.JSON200 != nil, experiments.Body); err != nil {
		return err
	}
	fmt.Printf("Experiments (up to %d):\n", pageSize)
	for _, experiment := range experiments.JSON200.Objects {
		fmt.Printf("  %s\t%s\n", experiment.Id, experiment.Name)
	}

	prompts, err := client.GetPromptWithResponse(ctx, &openapi.GetPromptParams{
		ProjectId: &projectID, Limit: &limit,
	})
	if err != nil {
		return fmt.Errorf("list prompts: %w", err)
	}
	if err := checkResponse("list prompts", prompts.StatusCode(), prompts.JSON200 != nil, prompts.Body); err != nil {
		return err
	}
	fmt.Printf("Prompts (up to %d):\n", pageSize)
	for _, prompt := range prompts.JSON200.Objects {
		fmt.Printf("  %s\t%s\n", prompt.Id, prompt.Name)
	}

	datasets, err := client.GetDatasetWithResponse(ctx, &openapi.GetDatasetParams{
		ProjectId: &projectID, Limit: &limit,
	})
	if err != nil {
		return fmt.Errorf("list datasets: %w", err)
	}
	if err := checkResponse("list datasets", datasets.StatusCode(), datasets.JSON200 != nil, datasets.Body); err != nil {
		return err
	}
	fmt.Printf("Datasets (up to %d):\n", pageSize)
	for _, dataset := range datasets.JSON200.Objects {
		fmt.Printf("  %s\t%s\n", dataset.Id, dataset.Name)
	}
	return nil
}

// DatasetLifecycle mutates only a uniquely named temporary dataset in an existing
// project. Updates and deletes apply to a row, not the dataset's metadata.
func DatasetLifecycle(ctx context.Context, client *openapi.ClientWithResponses, projectID string) (err error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return fmt.Errorf("parse project ID: %w", err)
	}
	created, err := client.PostDatasetWithResponse(ctx, openapi.CreateDataset{
		ProjectId:   projectUUID,
		Name:        "go-api-client-example-" + rand.Text(),
		Description: nullable.NewNullableWithValue("Temporary dataset for the Go HTTP API client example"),
	})
	if err != nil {
		return fmt.Errorf("create temporary dataset: %w", err)
	}
	if err := checkResponse("create temporary dataset", created.StatusCode(), created.JSON200 != nil, created.Body); err != nil {
		return err
	}
	dataset := created.JSON200
	datasetID := dataset.Id.String()
	fmt.Printf("Created temporary dataset %s (%s)\n", datasetID, dataset.Name)
	defer func() {
		// Cleanup still gets a chance if the original request context expires.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		deleted, cleanupErr := client.DeleteDatasetIdWithResponse(cleanupCtx, datasetID)
		if cleanupErr == nil {
			cleanupErr = checkResponse("delete temporary dataset", deleted.StatusCode(), deleted.JSON200 != nil, deleted.Body)
		}
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("delete temporary dataset %s manually: %w", datasetID, cleanupErr))
			return
		}
		fmt.Printf("Deleted temporary dataset %s\n", datasetID)
	}()

	rowID := nullable.NewNullableWithValue(rand.Text())
	inserted, err := client.PostDatasetIdInsertWithResponse(ctx, datasetID, openapi.InsertDatasetEventRequest{
		Events: []openapi.InsertDatasetEvent{{
			Id: rowID, Input: nullable.NewNullableWithValue[any]("What is 2 + 2?"),
			Expected: nullable.NewNullableWithValue[any]("four"),
		}},
	})
	if err != nil {
		return fmt.Errorf("create dataset row: %w", err)
	}
	if err := checkResponse("create dataset row", inserted.StatusCode(), inserted.JSON200 != nil, inserted.Body); err != nil {
		return err
	}
	if err := printRows(ctx, client, datasetID, "After create"); err != nil {
		return err
	}

	// Omit input entirely and merge only expected, preserving the existing input.
	updated, err := client.PostDatasetIdInsertWithResponse(ctx, datasetID, openapi.InsertDatasetEventRequest{
		Events: []openapi.InsertDatasetEvent{{
			Id: rowID, Expected: nullable.NewNullableWithValue[any]("4"),
			UnderscoreIsMerge: nullable.NewNullableWithValue(true),
		}},
	})
	if err != nil {
		return fmt.Errorf("update dataset row: %w", err)
	}
	if err := checkResponse("update dataset row", updated.StatusCode(), updated.JSON200 != nil, updated.Body); err != nil {
		return err
	}
	if err := printRows(ctx, client, datasetID, "After update (input preserved)"); err != nil {
		return err
	}

	deleted, err := client.PostDatasetIdInsertWithResponse(ctx, datasetID, openapi.InsertDatasetEventRequest{
		Events: []openapi.InsertDatasetEvent{{
			Id: rowID, UnderscoreObjectDelete: nullable.NewNullableWithValue(true),
		}},
	})
	if err != nil {
		return fmt.Errorf("delete dataset row: %w", err)
	}
	if err := checkResponse("delete dataset row", deleted.StatusCode(), deleted.JSON200 != nil, deleted.Body); err != nil {
		return err
	}
	return printRows(ctx, client, datasetID, "After delete")
}

func printRows(ctx context.Context, client *openapi.ClientWithResponses, datasetID, label string) error {
	limit := pageSize
	page, err := client.GetDatasetIdFetchWithResponse(ctx, datasetID, &openapi.GetDatasetIdFetchParams{Limit: &limit})
	if err != nil {
		return fmt.Errorf("fetch dataset rows: %w", err)
	}
	if err := checkResponse("fetch dataset rows", page.StatusCode(), page.JSON200 != nil, page.Body); err != nil {
		return err
	}
	fmt.Printf("%s: %d row(s)\n", label, len(page.JSON200.Events))
	for _, row := range page.JSON200.Events {
		body, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("encode dataset row: %w", err)
		}
		fmt.Printf("  %s\n", body)
	}
	return nil
}

// AutomationLifecycle creates a uniquely named, paused automation in an existing
// project. It requires manage permission; no webhook is delivered while paused.
func AutomationLifecycle(ctx context.Context, client *openapi.ClientWithResponses, projectID string) (err error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return fmt.Errorf("parse project ID: %w", err)
	}
	var action openapi.CreateProjectAutomation_Config_0_Action
	if err := action.FromCreateProjectAutomationConfig0Action0(openapi.CreateProjectAutomationConfig0Action0{
		Type: openapi.CreateProjectAutomationConfig0Action0TypeWebhook,
		Url:  "https://example.com/braintrust-hook",
	}); err != nil {
		return fmt.Errorf("encode webhook action: %w", err)
	}
	paused := openapi.AutomationStatusPaused
	var config openapi.CreateProjectAutomation_Config
	if err := config.FromCreateProjectAutomationConfig0(openapi.CreateProjectAutomationConfig0{
		Action: action, EventType: openapi.CreateProjectAutomationConfig0EventTypeLogs,
		BtqlFilter: "scores.Factuality < 0.5", IntervalSeconds: 300, Status: &paused,
	}); err != nil {
		return fmt.Errorf("encode paused logs configuration: %w", err)
	}
	created, err := client.PostProjectAutomationWithResponse(ctx, openapi.CreateProjectAutomation{
		ProjectId:   projectUUID,
		Name:        "go-api-client-automation-" + rand.Text(),
		Description: nullable.NewNullableWithValue("Temporary paused automation for the Go HTTP API client example"),
		Config:      config,
	})
	if err != nil {
		return fmt.Errorf("create temporary automation: %w", err)
	}
	if err := checkResponse("create temporary automation", created.StatusCode(), created.JSON200 != nil, created.Body); err != nil {
		return err
	}
	automationID := created.JSON200.Id.String()
	fmt.Printf("Created temporary automation %s\n", automationID)
	defer func() {
		// Cleanup still gets a chance if the original request context expires.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		deleted, cleanupErr := client.DeleteProjectAutomationIdWithResponse(cleanupCtx, automationID)
		if cleanupErr == nil {
			cleanupErr = checkResponse("delete temporary automation", deleted.StatusCode(), deleted.JSON200 != nil, deleted.Body)
		}
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("delete temporary automation %s manually: %w", automationID, cleanupErr))
			return
		}
		fmt.Printf("Deleted temporary automation %s\n", automationID)
	}()

	if err := printAutomation(ctx, client, automationID, "After create"); err != nil {
		return err
	}

	// Config replaces the whole configuration, so keep the automation paused and
	// include its action and interval along with the updated filter.
	var patchAction openapi.PatchProjectAutomation_Config_0_Action
	if err := patchAction.FromPatchProjectAutomationConfig0Action0(openapi.PatchProjectAutomationConfig0Action0{
		Type: openapi.PatchProjectAutomationConfig0Action0TypeWebhook,
		Url:  "https://example.com/braintrust-hook",
	}); err != nil {
		return fmt.Errorf("encode updated webhook action: %w", err)
	}
	var patchConfig openapi.PatchProjectAutomation_Config
	if err := patchConfig.FromPatchProjectAutomationConfig0(openapi.PatchProjectAutomationConfig0{
		Action: patchAction, EventType: openapi.PatchProjectAutomationConfig0EventTypeLogs,
		BtqlFilter: "scores.Factuality < 0.25", IntervalSeconds: 300, Status: &paused,
	}); err != nil {
		return fmt.Errorf("encode updated paused logs configuration: %w", err)
	}
	updated, err := client.PatchProjectAutomationIdWithResponse(ctx, automationID, openapi.PatchProjectAutomation{
		Description: nullable.NewNullableWithValue("Updated temporary paused automation with a stricter factuality filter"),
		Config:      &patchConfig,
	})
	if err != nil {
		return fmt.Errorf("update temporary automation: %w", err)
	}
	if err := checkResponse("update temporary automation", updated.StatusCode(), updated.JSON200 != nil, updated.Body); err != nil {
		return err
	}
	return printAutomation(ctx, client, automationID, "After update")
}

func printAutomation(ctx context.Context, client *openapi.ClientWithResponses, automationID, label string) error {
	result, err := client.GetProjectAutomationIdWithResponse(ctx, automationID)
	if err != nil {
		return fmt.Errorf("get temporary automation: %w", err)
	}
	if err := checkResponse("get temporary automation", result.StatusCode(), result.JSON200 != nil, result.Body); err != nil {
		return err
	}
	automation := result.JSON200
	config, err := automation.Config.AsProjectAutomationConfig0()
	if err != nil {
		return fmt.Errorf("decode temporary automation config: %w", err)
	}
	if config.EventType != openapi.ProjectAutomationConfig0EventTypeLogs || config.Status == nil || *config.Status != openapi.AutomationStatusPaused {
		return fmt.Errorf("temporary automation %s is not a paused logs automation", automationID)
	}
	description, err := automation.Description.Get()
	if err != nil {
		return fmt.Errorf("decode temporary automation description: %w", err)
	}
	fmt.Printf("%s: %s\t%s\t%s\n", label, automation.Id, description, config.BtqlFilter)
	return nil
}
