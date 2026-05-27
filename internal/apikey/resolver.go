// Package apikey provides Braintrust API key discovery helpers.
package apikey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/joho/godotenv"
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

	// Start candidate reads together, but consume results nearest-first below so
	// a slower local .env.braintrust still beats a faster parent directory.
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
	env, err := godotenv.UnmarshalBytes(data)
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(env[braintrustAPIKeyEnv])
	return value, value != ""
}
