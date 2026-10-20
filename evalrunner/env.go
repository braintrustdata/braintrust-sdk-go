package evalrunner

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Mode is what bt is asking this process to do. It is derived entirely from the
// environment, never from os.Args -- bt splices its own arguments in when a
// runner binary is used, so argv is meaningless to us.
type Mode string

const (
	// ModeList means bt wants the manifest of registered evals, printed as JSON
	// on stdout. No authentication and no API calls are needed.
	ModeList Mode = "list"

	// ModeEval means bt wants one specific eval run, described by
	// BT_EVAL_DEV_REQUEST_JSON, with results streamed back over the socket.
	ModeEval Mode = "eval"

	// ModeBatch is `bt eval <target>` without the dev server: run every
	// registered eval, honouring the CLI's filter and output flags.
	ModeBatch Mode = "batch"

	// ModeInspect means nothing spawned us -- somebody ran the binary directly.
	// Print what is registered and exit without touching Braintrust.
	ModeInspect Mode = "inspect"

	// ModeUnknown means BT_EVAL_DEV_MODE held a value we do not recognise.
	ModeUnknown Mode = "unknown"
)

// env is the parsed BT_EVAL_*/BRAINTRUST_* environment bt hands us. Reading the
// environment is confined to this file so the eval-running core stays testable
// and transport-agnostic.
type env struct {
	// DevMode is BT_EVAL_DEV_MODE: "list", "eval", or empty.
	DevMode string
	// RequestJSON is BT_EVAL_DEV_REQUEST_JSON: the browser's entire POST body.
	RequestJSON string

	// SSESock is BT_EVAL_SSE_SOCK, a unix socket path. bt always sets this when
	// it spawns us, which makes it our "was I run by bt?" marker.
	SSESock string
	// SSEAddr is BT_EVAL_SSE_ADDR, a host:port. bt never sets this today, but
	// the other SDK runners honour it and it makes an easy test transport.
	SSEAddr string

	List               bool // BT_EVAL_LIST: print eval names instead of running them
	JSONL              bool // BT_EVAL_JSONL: one JSON summary per eval on stdout
	NoSendLogs         bool // BT_EVAL_LOCAL or BT_EVAL_NO_SEND_LOGS
	TerminateOnFailure bool // BT_EVAL_TERMINATE_ON_FAILURE: stop at the first failing eval
	MaxConcurrency     int  // BT_EVAL_MAX_CONCURRENCY: registered evals to run at once

	// Filters comes from BT_EVAL_FILTER_PARSED (bt's --filter flags).
	Filters []evalFilter
}

// readEnv parses the environment through the given lookup, which tests replace
// with a map so they never mutate the process environment.
func readEnv(lookup func(string) string) env {
	return env{
		DevMode:            lookup("BT_EVAL_DEV_MODE"),
		RequestJSON:        lookup("BT_EVAL_DEV_REQUEST_JSON"),
		SSESock:            lookup("BT_EVAL_SSE_SOCK"),
		SSEAddr:            lookup("BT_EVAL_SSE_ADDR"),
		List:               envFlag(lookup, "BT_EVAL_LIST"),
		JSONL:              envFlag(lookup, "BT_EVAL_JSONL"),
		NoSendLogs:         envFlag(lookup, "BT_EVAL_LOCAL") || envFlag(lookup, "BT_EVAL_NO_SEND_LOGS"),
		TerminateOnFailure: envFlag(lookup, "BT_EVAL_TERMINATE_ON_FAILURE"),
		MaxConcurrency:     envPositiveInt(lookup, "BT_EVAL_MAX_CONCURRENCY", 1),
		Filters:            parseFilters(lookup("BT_EVAL_FILTER_PARSED"), nil),
	}
}

func envPositiveInt(lookup func(string) string, name string, fallback int) int {
	value, err := strconv.Atoi(lookup(name))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// Mode reports what this process should do.
func (e env) Mode() Mode {
	switch e.DevMode {
	case "list":
		return ModeList
	case "eval":
		return ModeEval
	case "":
		if e.spawnedByBT() {
			return ModeBatch
		}
		return ModeInspect
	default:
		return ModeUnknown
	}
}

// spawnedByBT reports whether bt started this process. bt binds an SSE socket
// before every spawn and passes its path, including for --list and for plain
// `bt eval` runs, so the socket path is a reliable marker.
func (e env) spawnedByBT() bool {
	return e.SSESock != "" || e.SSEAddr != ""
}

// envFlag treats a variable as true unless it is unset or holds one of the
// values the other SDK runners consider falsy. bt itself only ever writes "1"
// or omits the variable.
func envFlag(lookup func(string) string, name string) bool {
	switch lookup(name) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// evalFilter is one entry of BT_EVAL_FILTER_PARSED: a path into the eval's
// metadata and a regex to match the value at that path against.
type evalFilter struct {
	path    []string
	pattern *regexp.Regexp
}

// matches reports whether an eval passes this filter. Only the eval name is
// filterable today; a filter aimed at anything else includes by default rather
// than silently dropping evals.
func (f evalFilter) matches(evalName string) bool {
	if len(f.path) == 0 || (len(f.path) == 1 && f.path[0] == "name") {
		return f.pattern.MatchString(evalName)
	}
	return true
}

// matchesFilters reports whether an eval survives the filter set. No filters
// means everything runs; otherwise any single match is enough.
func matchesFilters(filters []evalFilter, evalName string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if f.matches(evalName) {
			return true
		}
	}
	return false
}

// parseFilters decodes BT_EVAL_FILTER_PARSED. Every failure mode degrades to
// "no filter" and warns: bt owns this value, and a malformed one should never
// take down a run.
func parseFilters(serialized string, log logger.Logger) []evalFilter {
	if serialized == "" {
		return nil
	}

	var raw []struct {
		Path    []string `json:"path"`
		Pattern string   `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(serialized), &raw); err != nil {
		logWarn(log, "ignoring malformed BT_EVAL_FILTER_PARSED", "error", err)
		return nil
	}

	filters := make([]evalFilter, 0, len(raw))
	for _, entry := range raw {
		pattern, err := regexp.Compile(entry.Pattern)
		if err != nil {
			logWarn(log, "ignoring eval filter with an invalid pattern", "pattern", entry.Pattern, "error", err)
			continue
		}
		filters = append(filters, evalFilter{path: entry.Path, pattern: pattern})
	}
	return filters
}

func logWarn(log logger.Logger, msg string, args ...any) {
	if log == nil {
		return
	}
	log.Warn(msg, args...)
}
