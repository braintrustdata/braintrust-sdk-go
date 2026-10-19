// Workflow evaluation with OpenAI batches, polling or externally verified
// webhooks, and optional Redis persistence across process restarts.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"go.opentelemetry.io/otel/sdk/trace"

	braintrust "github.com/braintrustdata/braintrust-sdk-go"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
)

func main() {
	project := flag.String("project", "go-sdk-examples", "Braintrust project for retained experiments")
	local := flag.Bool("local", false, "run an ordinary task/scorer locally without API credentials")
	runID := flag.String("run", "", "resume a persisted run")
	external := flag.String("external", "", "process a verified provider completion ID")
	webhook := flag.Bool("webhook", false, "use webhook completion instead of polling")
	socket := flag.String("redis-socket", "", "Redis Unix socket (requires redis-cli); use for restart")
	single := flag.Bool("single", false, "use one submission per case instead of batch stages")
	flag.Parse()
	if err := run(context.Background(), *local, *runID, *external, *webhook, *socket, *single, *project); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, local bool, runID, external string, webhook bool, socket string, single bool, project string) error {
	var store eval.WorkflowEvalStore = eval.NewWorkflowEvalMemoryStore()
	if socket != "" {
		var err error
		store, err = eval.NewWorkflowEvalRedisStore(redisCommand(socket), "workflow-example:", 7*24*time.Hour)
		if err != nil {
			return err
		}
	} else if runID != "" || external != "" || webhook {
		return errors.New("use -redis-socket to preserve state between invocations")
	}
	definition := &eval.WorkflowEval[string, string]{Name: "OpenAI workflow example", Store: store,
		Task: eval.T(func(ctx context.Context, input string) (string, error) { return input, nil }),
		Scorers: []eval.Scorer[string, string]{eval.NewScorer("nonempty", func(ctx context.Context, result eval.TaskResult[string, string]) (eval.Scores, error) {
			if result.Output != "" {
				return eval.S(1), nil
			}
			return eval.S(0), nil
		})},
		Classifiers: []eval.Classifier[string, string]{eval.NewClassifier("length", func(ctx context.Context, result eval.TaskResult[string, string]) (eval.Classifications, error) {
			label := "short"
			if len(result.Output) > 100 {
				label = "long"
			}
			return eval.Classifications{{ID: label}}, nil
		})},
	}
	if !local {
		provider := openai.NewClient()
		task, scorer := batchStages(provider, webhook)
		definition.Task = task
		definition.WorkflowScorers = []eval.WorkflowScorerDefinition[string, string]{scorer}
		if single {
			definition.Task = singleTask(task)
			definition.WorkflowScorers = []eval.WorkflowScorerDefinition[string, string]{singleScorer(scorer)}
		}
		tp := trace.NewTracerProvider()
		defer func() { _ = tp.Shutdown(ctx) }()
		bt, err := braintrust.New(tp, braintrust.WithProject(project), braintrust.WithBlockingLogin(true))
		if err != nil {
			return err
		}
		definition = braintrust.NewWorkflowEval(bt, definition)
	}
	var result *eval.WorkflowResult
	var err error
	switch {
	case external != "":
		result, err = definition.ProcessSubmissionResult(ctx, eval.WorkflowSubmissionResult{RunID: runID, ExternalID: external})
	case runID != "":
		result, err = definition.Poll(ctx, runID)
	default:
		result, err = definition.Start(ctx, eval.WorkflowStartOpts[string, string]{NoSendLogs: local, Dataset: eval.NewDataset([]eval.Case[string, string]{{Input: "What is the capital of France?", Expected: "Paris"}, {Input: "What is 2 + 2?", Expected: "4"}})})
	}
	if result != nil {
		data, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(data))
	}
	if err != nil {
		return err
	}
	if socket != "" || webhook {
		return nil
	}
	for result.Status != eval.WorkflowRunCompleted {
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		result, err = definition.Poll(ctx, result.RunID)
		if err != nil {
			return err
		}
		fmt.Printf("run=%s status=%s completed=%d failed=%d\n", result.RunID, result.Status, result.Completed, result.Failed)
	}
	return nil
}

func redisCommand(socket string) eval.WorkflowRedisCommand {
	// For this standalone example use redis-cli; applications can supply
	// client.Do(ctx,args...).Result() from their existing Redis client instead.
	return func(ctx context.Context, args ...any) (any, error) {
		argv := []string{"--json", "-s", socket}
		for _, arg := range args {
			argv = append(argv, fmt.Sprint(arg))
		}
		data, err := exec.CommandContext(ctx, "redis-cli", argv...).Output()
		if err != nil {
			return nil, err
		}
		var result any
		err = json.Unmarshal(data, &result)
		return result, err
	}
}

type batchRequest struct {
	CustomID string `json:"custom_id"`
	Method   string `json:"method"`
	URL      string `json:"url"`
	Body     any    `json:"body"`
}
type batchResponse struct {
	CustomID string          `json:"custom_id"`
	Error    json.RawMessage `json:"error"`
	Response struct {
		StatusCode int `json:"status_code"`
		Body       struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		} `json:"body"`
	} `json:"response"`
}

func submitBatch(ctx context.Context, client openai.Client, requests []batchRequest, info eval.WorkflowSubmissionContext) (json.RawMessage, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			return nil, err
		}
	}
	file, err := client.Files.New(ctx, openai.FileNewParams{File: openai.File(&data, "workflow.jsonl", "application/jsonl"), Purpose: openai.FilePurposeBatch})
	if err != nil {
		return nil, err
	}
	batch, err := client.Batches.New(ctx, openai.BatchNewParams{CompletionWindow: "24h", Endpoint: "/v1/chat/completions", InputFileID: file.ID, Metadata: map[string]string{"braintrust_run_id": info.RunID, "braintrust_submission_id": info.SubmissionID}}, option.WithHeader("Idempotency-Key", info.SubmissionID))
	if err != nil {
		return nil, err
	}
	return json.Marshal(batch.ID)
}
func request(id, prompt string) batchRequest {
	return batchRequest{CustomID: id, Method: "POST", URL: "/v1/chat/completions", Body: map[string]any{"model": "gpt-4o-mini", "messages": []map[string]string{{"role": "user", "content": prompt}}}}
}
func batchID(data json.RawMessage) (string, error) {
	var id string
	err := json.Unmarshal(data, &id)
	return id, err
}
func completion(client openai.Client, webhook bool) eval.WorkflowCompletion {
	if webhook {
		return eval.WorkflowCompletion{GetExternalID: func(_ context.Context, data json.RawMessage, _ eval.WorkflowSubmissionContext) (string, error) {
			return batchID(data)
		}}
	}
	return eval.WorkflowCompletion{Poll: func(ctx context.Context, data json.RawMessage, info eval.WorkflowSubmissionContext) (eval.WorkflowPollResult, error) {
		id, err := batchID(data)
		if err != nil {
			return eval.WorkflowPollResult{}, err
		}
		batch, err := client.Batches.Get(ctx, id)
		if err != nil {
			return eval.WorkflowPollResult{}, err
		}
		switch batch.Status {
		case "completed":
			return eval.WorkflowPollResult{Status: eval.WorkflowComplete}, nil
		case "failed", "expired", "cancelled":
			return eval.WorkflowPollResult{Status: eval.WorkflowFailed, Error: fmt.Errorf("batch %s: %s", id, batch.Status)}, nil
		default:
			return eval.WorkflowPollResult{Status: eval.WorkflowPending}, nil
		}
	}}
}
func collectBatch(ctx context.Context, client openai.Client, data json.RawMessage, yield func(string, string, error) error) error {
	id, err := batchID(data)
	if err != nil {
		return err
	}
	batch, err := client.Batches.Get(ctx, id)
	if err != nil {
		return err
	}
	for _, fileID := range []string{batch.OutputFileID, batch.ErrorFileID} {
		if fileID == "" {
			continue
		}
		response, err := client.Files.Content(ctx, fileID)
		if err != nil {
			return err
		}
		err = readBatch(response.Body, yield)
		closeErr := response.Body.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
func readBatch(reader io.Reader, yield func(string, string, error) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var row batchResponse
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return err
		}
		var output string
		var itemErr error
		if row.Response.StatusCode != 200 || len(row.Response.Body.Choices) == 0 {
			itemErr = fmt.Errorf("provider item failed (HTTP %d): %s", row.Response.StatusCode, row.Error)
		} else {
			output = row.Response.Body.Choices[0].Message.Content
		}
		if err := yield(row.CustomID, output, itemErr); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func batchStages(client openai.Client, webhook bool) (eval.WorkflowBatchTask[string, string], eval.WorkflowBatchScorer[string, string]) {
	task := eval.WorkflowBatchTask[string, string]{Batching: eval.WorkflowBatching{MaxSize: 100}, Completion: completion(client, webhook),
		Submit: func(ctx context.Context, items []eval.WorkflowBatchItem[eval.WorkflowTaskItem[string, string]], info eval.WorkflowSubmissionContext) (json.RawMessage, error) {
			requests := make([]batchRequest, len(items))
			for i, item := range items {
				requests[i] = request(item.CustomID, item.Item.Input)
			}
			return submitBatch(ctx, client, requests, info)
		}, Collect: func(ctx context.Context, data json.RawMessage, info eval.WorkflowSubmissionContext, yield func(eval.WorkflowBatchResult[eval.WorkflowTaskResult[string]]) error) error {
			return collectBatch(ctx, client, data, func(id, output string, err error) error {
				return yield(eval.WorkflowBatchResult[eval.WorkflowTaskResult[string]]{CustomID: id, Result: eval.WorkflowTaskResult[string]{Output: output}, Error: err})
			})
		},
	}
	scorer := eval.WorkflowBatchScorer[string, string]{Name: "correctness", Batching: eval.WorkflowBatching{MaxSize: 100, MaxWait: 15 * time.Minute}, Completion: completion(client, webhook),
		Submit: func(ctx context.Context, items []eval.WorkflowBatchItem[eval.WorkflowScorerItem[string, string]], info eval.WorkflowSubmissionContext) (json.RawMessage, error) {
			requests := make([]batchRequest, len(items))
			for i, item := range items {
				requests[i] = request(item.CustomID, fmt.Sprintf("Expected: %s\nAnswer: %s\nReply PASS if correct, otherwise FAIL.", item.Item.Expected, item.Item.Output))
			}
			return submitBatch(ctx, client, requests, info)
		}, Collect: func(ctx context.Context, data json.RawMessage, info eval.WorkflowSubmissionContext, yield func(eval.WorkflowBatchResult[eval.Scores]) error) error {
			return collectBatch(ctx, client, data, func(id, output string, err error) error {
				score := 0.0
				if strings.TrimSpace(output) == "PASS" {
					score = 1
				}
				return yield(eval.WorkflowBatchResult[eval.Scores]{CustomID: id, Result: eval.S(score), Error: err})
			})
		},
	}
	return task, scorer
}

func singleTask(batch eval.WorkflowBatchTask[string, string]) eval.WorkflowTask[string, string] {
	return eval.WorkflowTask[string, string]{Completion: batch.Completion,
		Submit: func(ctx context.Context, item eval.WorkflowTaskItem[string, string], info eval.WorkflowSubmissionContext) (json.RawMessage, error) {
			return batch.Submit(ctx, []eval.WorkflowBatchItem[eval.WorkflowTaskItem[string, string]]{{CustomID: info.SubmissionID + "_0", Item: item}}, info)
		},
		Collect: func(ctx context.Context, data json.RawMessage, info eval.WorkflowSubmissionContext) (eval.WorkflowTaskResult[string], error) {
			var result eval.WorkflowTaskResult[string]
			found := false
			err := batch.Collect(ctx, data, info, func(item eval.WorkflowBatchResult[eval.WorkflowTaskResult[string]]) error {
				found = true
				result = item.Result
				return item.Error
			})
			if err == nil && !found {
				err = errors.New("provider returned no result")
			}
			return result, err
		},
	}
}
func singleScorer(batch eval.WorkflowBatchScorer[string, string]) eval.WorkflowScorer[string, string] {
	return eval.WorkflowScorer[string, string]{Name: batch.Name, Completion: batch.Completion,
		Submit: func(ctx context.Context, item eval.WorkflowScorerItem[string, string], info eval.WorkflowSubmissionContext) (json.RawMessage, error) {
			return batch.Submit(ctx, []eval.WorkflowBatchItem[eval.WorkflowScorerItem[string, string]]{{CustomID: info.SubmissionID + "_0", Item: item}}, info)
		},
		Collect: func(ctx context.Context, data json.RawMessage, info eval.WorkflowSubmissionContext) (eval.Scores, error) {
			var result eval.Scores
			found := false
			err := batch.Collect(ctx, data, info, func(item eval.WorkflowBatchResult[eval.Scores]) error {
				found = true
				result = item.Result
				return item.Error
			})
			if err == nil && !found {
				err = errors.New("provider returned no result")
			}
			return result, err
		},
	}
}
