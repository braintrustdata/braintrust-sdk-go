// Package apikey provides Braintrust API key discovery helpers.
package apikey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	envBraintrustFilename = ".env.braintrust"
	braintrustAPIKeyEnv   = "BRAINTRUST_API_KEY"
	maxParentDirs         = 64
)

// Resolver lazily discovers BRAINTRUST_API_KEY from .env.braintrust.
type Resolver struct {
	once  sync.Once
	done  chan struct{}
	key   string
	found bool
}

// NewResolver creates a lazy .env.braintrust API key resolver.
func NewResolver() *Resolver {
	return &Resolver{done: make(chan struct{})}
}

// APIKey returns the discovered API key, if one exists.
func (r *Resolver) APIKey(ctx context.Context) (string, bool) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.once.Do(func() {
		go func() {
			r.key, r.found = lookupEnvBraintrustAPIKey()
			close(r.done)
		}()
	})

	select {
	case <-r.done:
		return r.key, r.found
	case <-ctx.Done():
		return "", false
	}
}

func lookupEnvBraintrustAPIKey() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return lookupEnvBraintrustAPIKeyFromDir(cwd)
}

func lookupEnvBraintrustAPIKeyFromDir(dir string) (string, bool) {
	paths := candidateEnvBraintrustFiles(dir)
	results := make([]envBraintrustReadResult, len(paths))

	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			data, err := os.ReadFile(path)
			results[i] = envBraintrustReadResult{data: data, err: err}
		}(i, path)
	}
	wg.Wait()

	for _, result := range results {
		if errors.Is(result.err, os.ErrNotExist) {
			continue
		}
		if result.err != nil {
			return "", false
		}

		key, found := parseBraintrustAPIKey(result.data)
		if found {
			return key, true
		}
		return "", false
	}

	return "", false
}

type envBraintrustReadResult struct {
	data []byte
	err  error
}

func candidateEnvBraintrustFiles(dir string) []string {
	if dir == "" {
		return nil
	}

	dir = filepath.Clean(dir)
	paths := make([]string, 0, maxParentDirs+1)
	for depth := 0; depth <= maxParentDirs; depth++ {
		paths = append(paths, filepath.Join(dir, envBraintrustFilename))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return paths
}

func parseBraintrustAPIKey(data []byte) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := parseDotenvLine(line)
		if !ok || key != braintrustAPIKeyEnv {
			continue
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value, true
		}
	}
	return "", false
}

func parseDotenvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	if strings.HasPrefix(line, "export") && len(line) > len("export") && isSpace(line[len("export")]) {
		line = strings.TrimSpace(line[len("export"):])
	}

	equals := strings.IndexByte(line, '=')
	if equals < 0 {
		return "", "", false
	}

	key = strings.TrimSpace(line[:equals])
	value = parseDotenvValue(line[equals+1:])
	return key, value, true
}

func parseDotenvValue(value string) string {
	value = strings.TrimLeft(value, " \t")
	if value == "" {
		return ""
	}

	switch value[0] {
	case '\'':
		if parsed, ok := parseQuotedDotenvValue(value, '\'', false); ok {
			return parsed
		}
	case '"':
		if parsed, ok := parseQuotedDotenvValue(value, '"', true); ok {
			return parsed
		}
	}

	for i := 0; i < len(value); i++ {
		if value[i] == '#' && (i == 0 || isSpace(value[i-1])) {
			value = value[:i]
			break
		}
	}
	return strings.TrimSpace(value)
}

func parseQuotedDotenvValue(value string, quote byte, expandEscapes bool) (string, bool) {
	var builder strings.Builder
	escaped := false
	for i := 1; i < len(value); i++ {
		ch := value[i]
		if escaped {
			if expandEscapes {
				switch ch {
				case 'n':
					builder.WriteByte('\n')
				case 'r':
					builder.WriteByte('\r')
				case 't':
					builder.WriteByte('\t')
				default:
					builder.WriteByte(ch)
				}
			} else {
				builder.WriteByte(ch)
			}
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == quote {
			return builder.String(), true
		}
		builder.WriteByte(ch)
	}
	return "", false
}

func isSpace(ch byte) bool {
	return ch == ' ' || ch == '\t'
}
