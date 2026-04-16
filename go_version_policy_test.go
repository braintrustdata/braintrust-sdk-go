package braintrust

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestOldestGoVersionTestedInCIMatchesGoFiles(t *testing.T) {
	t.Parallel()

	ciVersion, err := oldestGoVersionTestedInCI(".github/workflows/ci.yml")
	require.NoError(t, err)

	for _, path := range []string{"go.mod", "go.work"} {
		path := path
		t.Run(path, func(t *testing.T) {
			version, err := goDirectiveVersion(path)
			require.NoError(t, err)
			assert.Equal(t, ciVersion, version, "%s should pin the oldest Go version tested in CI", path)
		})
	}
}

func TestOldestGoVersionTestedInCIMatchesNestedModuleGoMods(t *testing.T) {
	t.Parallel()

	ciVersion, err := oldestGoVersionTestedInCI(".github/workflows/ci.yml")
	require.NoError(t, err)

	modulePaths, err := nestedModulePaths("scripts/nested_modules.txt")
	require.NoError(t, err)

	for _, modulePath := range modulePaths {
		modulePath := modulePath
		t.Run(modulePath, func(t *testing.T) {
			goModPath := filepath.Join(modulePath, "go.mod")
			version, err := goDirectiveVersion(goModPath)
			require.NoError(t, err)
			assert.Equal(t, ciVersion, version, "%s should pin the oldest Go version tested in CI", goModPath)
		})
	}
}

func oldestGoVersionTestedInCI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					GoVersion []string `yaml:"go-version"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return "", err
	}

	var oldest goVersion
	var oldestText string
	found := false
	for _, job := range workflow.Jobs {
		for _, versionText := range job.Strategy.Matrix.GoVersion {
			version, err := parseGoVersion(versionText)
			if err != nil {
				return "", fmt.Errorf("parse CI go version %q: %w", versionText, err)
			}
			if !found || version.less(oldest) {
				oldest = version
				oldestText = version.String()
				found = true
			}
		}
	}
	if !found {
		return "", fmt.Errorf("no Go versions found in %s", path)
	}

	return oldestText, nil
}

func nestedModulePaths(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		paths = append(paths, line)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no nested module paths found in %s", path)
	}
	return paths, nil
}

func goDirectiveVersion(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		version, err := parseGoVersion(strings.TrimSpace(strings.TrimPrefix(line, "go ")))
		if err != nil {
			return "", fmt.Errorf("parse go directive in %s: %w", path, err)
		}
		return version.String(), nil
	}

	return "", fmt.Errorf("no go directive found in %s", path)
}

type goVersion struct {
	major int
	minor int
	patch int
}

func parseGoVersion(s string) (goVersion, error) {
	s = strings.TrimSpace(strings.TrimSuffix(s, ".x"))
	parts := strings.Split(s, ".")
	if len(parts) != 2 && len(parts) != 3 {
		return goVersion{}, fmt.Errorf("expected version like 1.24 or 1.24.1, got %q", s)
	}

	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return goVersion{}, err
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return goVersion{}, err
	}

	version := goVersion{major: major, minor: minor}
	if len(parts) == 3 {
		patch, err := strconv.Atoi(parts[2])
		if err != nil {
			return goVersion{}, err
		}
		version.patch = patch
	}

	return version, nil
}

func (v goVersion) less(other goVersion) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

func (v goVersion) String() string {
	if v.patch == 0 {
		return fmt.Sprintf("%d.%d", v.major, v.minor)
	}
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}
