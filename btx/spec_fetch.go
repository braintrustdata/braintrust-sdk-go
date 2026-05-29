// Package btx implements the BTX (Braintrust Cross-language) test runner for the Go SDK.
// It validates SDK instrumentation against YAML spec files from braintrustdata/braintrust-spec.
package btx

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const specCacheDir = ".spec-cache"

// fetchSpec downloads and extracts the spec tarball from GitHub if not already cached.
// It returns the path to the spec root directory (containing test/llm_span/).
// If BTX_SPEC_ROOT is set, it is returned directly without any download.
func fetchSpec() (string, error) {
	if root := os.Getenv("BTX_SPEC_ROOT"); root != "" {
		return root, nil
	}

	ref, err := readSpecRef()
	if err != nil {
		return "", fmt.Errorf("reading spec ref: %w", err)
	}

	cacheDir := filepath.Join(specCacheDir, ref)
	marker := filepath.Join(cacheDir, "test", "llm_span")

	// Idempotent: skip if already cached.
	if info, err := os.Stat(marker); err == nil && info.IsDir() {
		return cacheDir, nil
	}

	url := fmt.Sprintf("https://github.com/braintrustdata/braintrust-spec/archive/%s.tar.gz", ref)
	fmt.Printf("btx: fetching spec from %s\n", url)

	resp, err := http.Get(url) //nolint:gosec // URL is constructed from a pinned ref.
	if err != nil {
		return "", fmt.Errorf("downloading spec: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading spec: HTTP %d", resp.StatusCode)
	}

	if err := extractTarGz(resp.Body, cacheDir); err != nil {
		return "", fmt.Errorf("extracting spec: %w", err)
	}

	return cacheDir, nil
}

// readSpecRef reads the pinned ref from spec_ref.txt.
func readSpecRef() (string, error) {
	data, err := os.ReadFile("spec_ref.txt")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// extractTarGz extracts a tar.gz stream into destDir, stripping the top-level
// directory from the archive (e.g. "braintrust-spec-v0.0.7/" is removed).
func extractTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Strip the top-level directory.
		parts := strings.SplitN(header.Name, "/", 2)
		if len(parts) < 2 || parts[1] == "" {
			continue
		}
		relPath := parts[1]
		target := filepath.Join(destDir, filepath.FromSlash(relPath))

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := extractFile(target, tr); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractFile writes a single file from the tar reader.
func extractFile(target string, r io.Reader) (retErr error) {
	f, err := os.Create(target)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); retErr == nil {
			retErr = closeErr
		}
	}()
	_, err = io.Copy(f, r) //nolint:gosec // Trusted archive from GitHub.
	return err
}
