// Package apikey provides Braintrust API key discovery helpers.
package apikey

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	braintrustConfigFilename = ".braintrust.json"
	maxParentDirs            = 64
)

// Resolver lazily discovers BRAINTRUST_API_KEY from .braintrust.json.
type Resolver struct {
	once  sync.Once
	done  chan struct{}
	key   string
	found bool
}

// NewResolver creates a lazy .braintrust.json API key resolver.
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
			r.key, r.found = lookupBraintrustConfigAPIKey()
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

func lookupBraintrustConfigAPIKey() (string, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	return lookupBraintrustConfigAPIKeyFromDir(cwd)
}

func lookupBraintrustConfigAPIKeyFromDir(dir string) (string, bool) {
	paths := candidateBraintrustConfigFiles(dir)
	results := make([]braintrustConfigReadResult, len(paths))
	ready := make([]bool, len(paths))
	resultCh := make(chan braintrustConfigReadResult, len(paths))

	for i, path := range paths {
		go func(i int, path string) {
			data, err := os.ReadFile(path)
			resultCh <- braintrustConfigReadResult{index: i, data: data, err: err}
		}(i, path)
	}

	next := 0
	for range paths {
		result := <-resultCh
		results[result.index] = result
		ready[result.index] = true

		for next < len(paths) && ready[next] {
			result := results[next]
			if errors.Is(result.err, os.ErrNotExist) {
				next++
				continue
			}
			if result.err != nil {
				return "", false
			}
			return parseBraintrustAPIKey(result.data)
		}
	}

	return "", false
}

type braintrustConfigReadResult struct {
	index int
	data  []byte
	err   error
}

func candidateBraintrustConfigFiles(dir string) []string {
	if dir == "" {
		return nil
	}

	dir = filepath.Clean(dir)
	paths := make([]string, 0, maxParentDirs+1)
	for depth := 0; depth <= maxParentDirs; depth++ {
		paths = append(paths, filepath.Join(dir, braintrustConfigFilename))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return paths
}

func parseBraintrustAPIKey(data []byte) (string, bool) {
	var cfg struct {
		APIKey string `json:"BRAINTRUST_API_KEY"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	value := strings.TrimSpace(cfg.APIKey)
	return value, value != ""
}
