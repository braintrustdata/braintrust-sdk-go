// This example builds a generated request offline, or reads through the public generated client with -live.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/braintrustdata/braintrust-sdk-go/openapi"
)

func main() {
	live := flag.Bool("live", false, "also list and fetch a project using the public generated client")
	automationID := flag.String("automation-id", "", "with -live, also fetch an existing project automation by ID")
	flag.Parse()
	if err := run(*live, *automationID); err != nil {
		log.Fatal(err)
	}
}

func run(live bool, automationID string) error {
	if automationID != "" && !live {
		return fmt.Errorf("-automation-id requires -live")
	}
	req, err := openapi.NewPostProjectRequest("https://api.braintrust.dev", openapi.CreateProject{
		Name: "go-sdk-tests",
	})
	if err != nil {
		return fmt.Errorf("build project request: %w", err)
	}
	defer func() { _ = req.Body.Close() }()

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return fmt.Errorf("read project request body: %w", err)
	}
	fmt.Printf("%s %s\n%s\n", req.Method, req.URL, body)
	if live {
		return readProject(automationID)
	}
	return nil
}

func readProject(automationID string) error {
	key := os.Getenv("BRAINTRUST_API_KEY")
	if key == "" {
		return fmt.Errorf("BRAINTRUST_API_KEY is required with -live")
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
	if automationID != "" {
		automation, err := client.GetProjectAutomationIdWithResponse(ctx, automationID)
		if err != nil {
			return fmt.Errorf("get project automation: %w", err)
		}
		if err := checkResponse("get project automation", automation.StatusCode(), automation.JSON200 != nil, automation.Body); err != nil {
			return err
		}
		fmt.Printf("Fetched project automation %s (%s)\n", automation.JSON200.Id, automation.JSON200.Name)
	}
	limit := 1
	result, err := client.GetProjectWithResponse(ctx, &openapi.GetProjectParams{Limit: &limit})
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	if err := checkResponse("list projects", result.StatusCode(), result.JSON200 != nil, result.Body); err != nil {
		return err
	}
	fmt.Printf("Listed %d project(s) through the public generated client\n", len(result.JSON200.Objects))
	if len(result.JSON200.Objects) == 0 {
		return nil
	}
	projectID := result.JSON200.Objects[0].Id
	project, err := client.GetProjectIdWithResponse(ctx, projectID.String())
	if err != nil {
		return fmt.Errorf("get project: %w", err)
	}
	if err := checkResponse("get project", project.StatusCode(), project.JSON200 != nil, project.Body); err != nil {
		return err
	}
	if project.JSON200.Id != projectID {
		return fmt.Errorf("get returned a different project ID than list")
	}
	fmt.Printf("Fetched project %s through the public generated client\n", project.JSON200.Id)
	return nil
}

// Generated methods return transport/decoding errors, not HTTP status errors.
func checkResponse(operation string, status int, hasJSON bool, body []byte) error {
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("%s: HTTP %d: %s", operation, status, body)
	}
	if !hasJSON {
		return fmt.Errorf("%s: HTTP %d without the expected JSON response", operation, status)
	}
	return nil
}
