package attachmentprocessor

import (
	"encoding/json"
	"regexp"

	"github.com/braintrustdata/braintrust-sdk-go/logger"
)

// Processor scans JSON strings for base64 attachments across multiple LLM
// provider formats, uploads them, and returns modified JSON with attachment
// references.
type Processor struct {
	uploader  Uploader
	heuristic *regexp.Regexp
	formats   []Format
	log       logger.Logger
}

// NewProcessor creates a processor with the given uploader, logger, and the
// default set of vendor formats.
func NewProcessor(uploader Uploader, log logger.Logger) *Processor {
	return NewProcessorWithFormats(uploader, log, Formats)
}

// NewProcessorWithFormats creates a processor with an explicit set of formats.
func NewProcessorWithFormats(uploader Uploader, log logger.Logger, formats []Format) *Processor {
	if log == nil {
		log = logger.Discard()
	}
	return &Processor{
		uploader:  uploader,
		heuristic: BuildHeuristic(formats),
		formats:   formats,
		log:       log,
	}
}

// ProcessAndUpload scans json for base64 attachments, uploads them, and
// returns the modified JSON. Returns the original string unchanged when no
// attachments are found or if the uploader has shut down.
//
// JSON parse errors on individual spans are silently skipped — they don't
// affect processing of subsequent spans. The uploader shuts itself down on
// upload failures (network errors, auth errors, etc.), which causes all
// future calls to bail out via IsShutdown().
func (p *Processor) ProcessAndUpload(jsonStr string) string {
	if jsonStr == "" || p.uploader.IsShutdown() {
		return jsonStr
	}

	if !p.heuristic.MatchString(jsonStr) {
		return jsonStr
	}

	result, err := p.processJSON(jsonStr)
	if err != nil {
		// Per-span errors (malformed JSON, etc.) — skip this span, don't
		// kill the processor. Upload failures are handled by the uploader
		// itself (it sets rejectNewJobs, so IsShutdown() returns true).
		p.log.Debug("attachment processing skipped for span", "error", err)
		return jsonStr
	}
	return result
}

// maxWalkDepth is the maximum JSON nesting depth we'll recurse into.
// Go's goroutine stack overflow is fatal (not recoverable), so we cap
// depth to avoid crashing the process on pathological input.
const maxWalkDepth = 128

func (p *Processor) processJSON(jsonStr string) (string, error) {
	var root any
	if err := json.Unmarshal([]byte(jsonStr), &root); err != nil {
		return "", err
	}

	modified := false
	failed := false
	result, _ := p.walkAndReplace(root, "", &modified, &failed, 0)
	if failed || !modified {
		return jsonStr, nil
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// walkAndReplace traverses the JSON tree. For each node it checks all
// registered formats. The first format whose matcher returns true handles the
// replacement — no further recursion into that subtree.
//
// Returns the (possibly replaced) node and whether anything in this subtree
// was modified. Containers are only copied when a descendant was replaced.
//
// If an enqueue fails mid-walk, *failed is set to true. The caller should
// discard the partially-rewritten tree and return the original JSON to avoid
// a mix of replaced references and inline base64 data.
func (p *Processor) walkAndReplace(node any, parentKey string, modified *bool, failed *bool, depth int) (any, bool) {
	if depth >= maxWalkDepth || *failed {
		return node, false
	}

	uploadFn := func(ref Reference, data []byte) bool {
		ok := p.uploader.Enqueue(ref, data)
		if !ok {
			*failed = true
		}
		return ok
	}

	// Check each registered format.
	for _, fmt := range p.formats {
		if fmt.Match(parentKey, node) {
			replacement, ok := fmt.Replace(node, uploadFn)
			if ok {
				*modified = true
				return replacement, true
			}
		}
	}

	// No format matched — recurse into children. Only allocate a new
	// container when a child was actually replaced.
	switch v := node.(type) {
	case map[string]any:
		var result map[string]any
		for k, child := range v {
			newChild, changed := p.walkAndReplace(child, k, modified, failed, depth+1)
			if *failed {
				return node, false
			}
			if changed {
				if result == nil {
					result = make(map[string]any, len(v))
					for k2, v2 := range v {
						result[k2] = v2
					}
				}
				result[k] = newChild
			}
		}
		if result != nil {
			return result, true
		}
		return node, false
	case []any:
		var result []any
		for i, child := range v {
			newChild, changed := p.walkAndReplace(child, "", modified, failed, depth+1)
			if *failed {
				return node, false
			}
			if changed {
				if result == nil {
					result = make([]any, len(v))
					copy(result, v)
				}
				result[i] = newChild
			}
		}
		if result != nil {
			return result, true
		}
		return node, false
	default:
		return node, false
	}
}
