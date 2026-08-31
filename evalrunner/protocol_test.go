package evalrunner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/braintrustdata/braintrust-sdk-go/api"
	"github.com/braintrustdata/braintrust-sdk-go/eval"
	"github.com/braintrustdata/braintrust-sdk-go/internal/auth"
	"github.com/braintrustdata/braintrust-sdk-go/internal/vcr"
	"github.com/braintrustdata/braintrust-sdk-go/logger"
	"github.com/braintrustdata/braintrust-sdk-go/prompt"
)

// frame is one Server-Sent Event read back off the socket.
type frame struct {
	event string
	data  string
}

// fakeBT stands in for `bt eval --dev`: it binds the unix socket, hands the
// path to the runner through the environment, and collects the frames the
// runner writes.
//
// This is the one place the tests substitute something for a real dependency.
// bt is an external Rust binary speaking a unix-socket protocol, which VCR
// cannot record, and building it is not viable in this module's test run. Only
// bt's socket is faked: the SDK path under test -- dispatch, the eval run, the
// frames -- is entirely real, and the Braintrust API calls behind it go through
// VCR cassettes like every other integration test in the repo.
type fakeBT struct {
	sockPath string

	mu     sync.Mutex
	frames []frame
	done   chan struct{}
}

// socketDir returns a directory short enough to hold a unix socket.
//
// A unix socket path is capped by sun_path: 104 bytes on macOS, 108 on Linux.
// t.TempDir() embeds the test's name and a random suffix underneath an already
// long TMPDIR (macOS uses /var/folders/<random>/T), which overflows that budget
// and fails with "bind: invalid argument". Sockets therefore get their own
// short home rather than the usual per-test directory.
func socketDir(t *testing.T) string {
	t.Helper()

	parent := ""
	if _, err := os.Stat("/tmp"); err == nil {
		parent = "/tmp"
	}

	dir, err := os.MkdirTemp(parent, "bt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	return dir
}

func startFakeBT(t *testing.T) *fakeBT {
	t.Helper()

	b := &fakeBT{
		sockPath: filepath.Join(socketDir(t), "s.sock"),
		done:     make(chan struct{}),
	}

	ln, err := net.Listen("unix", b.sockPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		defer close(b.done)

		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		b.readFrames(conn)
	}()

	return b
}

// readFrames parses the SSE stream the same way bt does: "event:" and "data:"
// lines accumulate until a blank line dispatches the event.
func (b *fakeBT) readFrames(conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	var current frame
	var data []string

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.event != "" {
				current.data = strings.Join(data, "\n")
				b.mu.Lock()
				b.frames = append(b.frames, current)
				b.mu.Unlock()
			}
			current, data = frame{}, nil
		case strings.HasPrefix(line, "event:"):
			current.event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

// collected waits for the runner to close the socket, then returns the frames.
// bt likewise waits for the child's end to close before finishing the request.
func (b *fakeBT) collected(t *testing.T) []frame {
	t.Helper()

	select {
	case <-b.done:
	case <-time.After(30 * time.Second):
		t.Fatal("runner never closed the SSE socket; bt would hang here forever")
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]frame(nil), b.frames...)
}

func eventNames(frames []frame) []string {
	names := make([]string, len(frames))
	for i, f := range frames {
		names[i] = f.event
	}
	return names
}

func firstFrame(t *testing.T, frames []frame, event string) frame {
	t.Helper()

	for _, f := range frames {
		if f.event == event {
			return f
		}
	}
	t.Fatalf("no %q frame in %v", event, eventNames(frames))
	return frame{}
}

// vcrSession builds a session whose HTTP traffic replays from a cassette.
func vcrSession(t *testing.T) func(context.Context) (*session, error) {
	t.Helper()

	httpsClient := vcr.GetHTTPSClient(t)

	return func(ctx context.Context) (*session, error) {
		authSession, err := auth.NewSession(ctx, auth.Options{
			APIKey: httpsClient.APIKey(),
			AppURL: defaultAppURL,
			Logger: logger.Discard(),
			Client: httpsClient,
		})
		if err != nil {
			return nil, err
		}
		return &session{session: authSession, api: api.NewWithHTTPSClient(httpsClient)}, nil
	}
}

func TestEvalMode_StreamsTheFullProtocol(t *testing.T) {
	bt := startFakeBT(t)

	request := map[string]any{
		"name":       "food-classifier",
		"parameters": map[string]any{"model": "strict"},
		"data": map[string]any{
			"data": []map[string]any{
				{"input": "apple", "expected": "fruit"},
				{"input": "A crisp red apple", "expected": "fruit"},
			},
		},
	}
	requestJSON, err := json.Marshal(request)
	require.NoError(t, err)

	r, stdout := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": string(requestJSON),
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})
	r.newSession = vcrSession(t)
	registerFoodClassifier(r)

	require.NoError(t, Run(context.Background(), r))

	frames := bt.collected(t)
	names := eventNames(frames)

	// start first, summary last, progress in between.
	require.NotEmpty(t, names)
	assert.Equal(t, "start", names[0], "the playground needs the experiment link before results")
	assert.Equal(t, "summary", names[len(names)-1])
	assert.Contains(t, names, "progress")

	// bt suppresses a runner's `done` and emits its own after we exit, so
	// sending one is pointless noise.
	assert.NotContains(t, names, "done", "bt emits the terminal done itself")

	// Nothing may reach stdout in eval mode: bt parses the /list manifest from
	// it, and a stray JSON line would be misread as a manifest.
	assert.Empty(t, stdout.String(), "eval mode must keep stdout clean")
}

func TestEvalMode_SummaryCarriesAveragedScores(t *testing.T) {
	bt := startFakeBT(t)

	// "strict" classifies only exact single words, so "A crisp red apple"
	// becomes "unknown": one exact_match hit out of two.
	requestJSON, err := json.Marshal(map[string]any{
		"name":       "food-classifier",
		"parameters": map[string]any{"model": "strict"},
		"data": map[string]any{
			"data": []map[string]any{
				{"input": "apple", "expected": "fruit"},
				{"input": "A crisp red apple", "expected": "fruit"},
			},
		},
	})
	require.NoError(t, err)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": string(requestJSON),
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})
	r.newSession = vcrSession(t)
	registerFoodClassifier(r)

	require.NoError(t, Run(context.Background(), r))

	var summary summaryEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, bt.collected(t), "summary").data), &summary))

	assert.Equal(t, "food-classifier", summary.ExperimentName)
	assert.NotEmpty(t, summary.ProjectName)
	assert.InDelta(t, 0.5, summary.Scores["exact_match"].Score, 0.0001)
	assert.Equal(t, "exact_match", summary.Scores["exact_match"].Name)
}

// The parameter the playground selects must actually reach the task.
func TestEvalMode_ParametersReachTheTask(t *testing.T) {
	bt := startFakeBT(t)

	requestJSON, err := json.Marshal(map[string]any{
		"name":       "food-classifier",
		"parameters": map[string]any{"model": "strict"},
		"data": map[string]any{
			"data": []map[string]any{{"input": "A crisp red apple", "expected": "fruit"}},
		},
	})
	require.NoError(t, err)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": string(requestJSON),
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})
	r.newSession = vcrSession(t)
	registerFoodClassifier(r)

	require.NoError(t, Run(context.Background(), r))

	// Under "strict" the descriptive phrase falls through to "unknown"; under
	// the default "rule-based" it would have classified as "fruit".
	var sawStrictOutput bool
	for _, f := range bt.collected(t) {
		if f.event == "progress" && strings.Contains(f.data, `\"unknown\"`) {
			sawStrictOutput = true
		}
	}
	assert.True(t, sawStrictOutput, "the strict model parameter did not reach the task")
}

// A prompt parameter has to survive the whole round trip: the playground sends
// prompt data as JSON, and the task gets a prompt it can render.
func TestEvalMode_PromptParameterReachesTheTask(t *testing.T) {
	bt := startFakeBT(t)

	requestJSON, err := json.Marshal(map[string]any{
		"name": "prompt-echo",
		"parameters": map[string]any{
			"greeting": map[string]any{
				"prompt": map[string]any{
					"type": "chat",
					"messages": []map[string]any{
						{"role": "user", "content": "Howdy, {{input}}!"},
					},
				},
				"options": map[string]any{"model": "gpt-4o"},
			},
		},
		"data": map[string]any{
			"data": []map[string]any{{"input": "Joe", "expected": "Howdy, Joe!"}},
		},
	})
	require.NoError(t, err)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": string(requestJSON),
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})
	r.newSession = vcrSession(t)
	registerPromptEcho(r)

	require.NoError(t, Run(context.Background(), r))

	frames := bt.collected(t)
	var sawRendered bool
	for _, f := range frames {
		if f.event == "progress" && strings.Contains(f.data, `Howdy, Joe!`) {
			sawRendered = true
		}
	}
	assert.True(t, sawRendered, "the playground's prompt did not reach the task")

	var summary summaryEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, frames, "summary").data), &summary))
	assert.InDelta(t, 1.0, summary.Scores["exact_match"].Score, 0.0001)
}

// A prompt parameter the SDK cannot turn into a prompt is reported as an error
// rather than handed to the task as a raw map.
func TestEvalMode_InvalidPromptParameterReportsAnError(t *testing.T) {
	bt := startFakeBT(t)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE": "eval",
		"BT_EVAL_DEV_REQUEST_JSON": `{"name":"prompt-echo","parameters":{"greeting":"gpt-4o"},` +
			`"data":{"data":[{"input":"Joe","expected":"Howdy, Joe!"}]}}`,
		"BT_EVAL_SSE_SOCK": bt.sockPath,
	})
	registerPromptEcho(r)

	require.NoError(t, Run(context.Background(), r))

	// Reported as a bad request, before the runner even authenticates: a prompt
	// value the SDK cannot use is the caller's problem, not a run failure.
	var payload errorEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, bt.collected(t), "error").data), &payload))
	assert.Equal(t, 400, payload.Status)
	assert.Contains(t, payload.Message, "greeting")
}

func TestEvalMode_UnknownEvalReportsNotFound(t *testing.T) {
	bt := startFakeBT(t)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": `{"name":"does-not-exist","data":{"data":[]}}`,
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})
	registerFoodClassifier(r)

	// Reported over the wire, not returned: returning would exit non-zero and
	// make bt append a second, duplicate error frame.
	require.NoError(t, Run(context.Background(), r))

	var payload errorEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, bt.collected(t), "error").data), &payload))
	assert.Equal(t, 404, payload.Status)
	assert.Contains(t, payload.Message, "does-not-exist")
}

func TestEvalMode_MalformedRequestReportsBadRequest(t *testing.T) {
	bt := startFakeBT(t)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": `{not json`,
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})

	require.NoError(t, Run(context.Background(), r))

	var payload errorEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, bt.collected(t), "error").data), &payload))
	assert.Equal(t, 400, payload.Status)
}

// bt waits for our end of the socket to close before it finishes the request,
// and it has no timeout, so failing to close hangs bt indefinitely.
func TestEvalMode_ClosesTheSocketEvenOnFailure(t *testing.T) {
	bt := startFakeBT(t)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": `{"name":"nope","data":{"data":[]}}`,
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})

	require.NoError(t, Run(context.Background(), r))

	// collected() fails the test if the socket never closed.
	assert.NotEmpty(t, bt.collected(t))
}

// A case where one scorer fails but another succeeds must still contribute the
// successful score to the summary; the failure must not discard it.
func TestEvalMode_PartialScorerFailureKeepsHealthyScores(t *testing.T) {
	bt := startFakeBT(t)

	requestJSON, err := json.Marshal(map[string]any{
		"name": "partial-failure",
		"data": map[string]any{
			"data": []map[string]any{
				{"input": "a", "expected": "a"},
				{"input": "b", "expected": "b"},
			},
		},
	})
	require.NoError(t, err)

	r, _ := newTestRunner(t, map[string]string{
		"BT_EVAL_DEV_MODE":         "eval",
		"BT_EVAL_DEV_REQUEST_JSON": string(requestJSON),
		"BT_EVAL_SSE_SOCK":         bt.sockPath,
	})
	r.newSession = vcrSession(t)
	registerPartialFailure(r)

	require.NoError(t, Run(context.Background(), r))

	frames := bt.collected(t)

	var summary summaryEvent
	require.NoError(t, json.Unmarshal([]byte(firstFrame(t, frames, "summary").data), &summary))

	// The healthy scorer ran on both cases and must be averaged over both,
	// even though the other scorer errored on each case.
	healthy, ok := summary.Scores["healthy"]
	require.True(t, ok, "the healthy score was dropped: %v", summary.Scores)
	assert.InDelta(t, 1.0, healthy.Score, 0.0001)

	// Both cases reached scoring, so both cells are marked done.
	doneCount := 0
	for _, f := range frames {
		if f.event == "progress" && strings.Contains(f.data, `"event":"done"`) {
			doneCount++
		}
	}
	assert.Equal(t, 2, doneCount, "each scored case should emit a done frame")
}

func registerPartialFailure(r *Runner) {
	RegisterEval(r, &eval.Eval[string, string]{
		Name:        "partial-failure",
		Task:        eval.T(func(_ context.Context, input string) (string, error) { return input, nil }),
		ProjectName: "go-sdk-tests",
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("healthy", func(_ context.Context, res eval.TaskResult[string, string]) (eval.Scores, error) {
				return eval.S(1.0), nil
			}),
			eval.NewScorer("broken", func(_ context.Context, _ eval.TaskResult[string, string]) (eval.Scores, error) {
				return nil, errors.New("scorer boom")
			}),
		},
	})
}

func registerFoodClassifier(r *Runner) {
	classify := eval.TaskWithHooks(func(_ context.Context, input string, hooks *eval.TaskHooks) (string, error) {
		normalized := strings.ToLower(strings.TrimSpace(input))
		if hooks.Parameters.String("model") == "strict" {
			switch normalized {
			case "apple", "banana":
				return "fruit", nil
			default:
				return "unknown", nil
			}
		}
		if strings.Contains(normalized, "apple") {
			return "fruit", nil
		}
		return "unknown", nil
	})

	RegisterEval(r, &eval.Eval[string, string]{
		Name:        "food-classifier",
		Task:        classify,
		ProjectName: "go-sdk-tests",
		ParameterSchema: eval.ParameterSchema{
			"model": {Type: "model", Default: "rule-based"},
		},
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("exact_match", func(_ context.Context, res eval.TaskResult[string, string]) (eval.Scores, error) {
				if res.Output == res.Expected {
					return eval.S(1.0), nil
				}
				return eval.S(0.0), nil
			}),
		},
	})
}

// registerPromptEcho registers an eval whose task renders a prompt parameter
// and returns the rendered text, so a test can see exactly what the task got.
func registerPromptEcho(r *Runner) {
	echo := eval.TaskWithHooks(func(_ context.Context, input string, hooks *eval.TaskHooks) (string, error) {
		p, ok := hooks.Parameters.Prompt("greeting")
		if !ok {
			return "", errors.New("greeting parameter is not a prompt")
		}

		built, err := p.Build(map[string]any{"input": input})
		if err != nil {
			return "", err
		}
		built.AnnotateSpan(hooks.TaskSpan)

		return built.Messages[len(built.Messages)-1].Content.String(), nil
	})

	RegisterEval(r, &eval.Eval[string, string]{
		Name:        "prompt-echo",
		Task:        echo,
		ProjectName: "go-sdk-tests",
		Scorers: []eval.Scorer[string, string]{
			eval.NewScorer("exact_match", func(_ context.Context, res eval.TaskResult[string, string]) (eval.Scores, error) {
				if res.Output == res.Expected {
					return eval.S(1.0), nil
				}
				return eval.S(0.0), nil
			}),
		},
		ParameterSchema: eval.ParameterSchema{
			"greeting": {
				Type: eval.ParameterTypePrompt,
				Default: prompt.Definition{
					Model:    "gpt-4o",
					Messages: []prompt.Message{prompt.User("Hello, {{input}}!")},
				},
			},
		},
	})
}
