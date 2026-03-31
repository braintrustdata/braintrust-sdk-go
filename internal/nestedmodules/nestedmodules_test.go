package nestedmodules

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestReadManifestSkipsCommentsAndBlankLines(t *testing.T) {
	t.Parallel()

	modules, err := ReadManifest(filepath.Join("testdata", "nested_modules.txt"))
	if err != nil {
		t.Fatalf("ReadManifest returned error: %v", err)
	}

	want := []string{
		"trace/contrib/openai",
		"trace/contrib/github.com/sashabaranov/go-openai",
		"trace/contrib/all",
	}
	if !slices.Equal(modules, want) {
		t.Fatalf("ReadManifest() = %v, want %v", modules, want)
	}
}

func TestDependencyOrderSortsDependenciesBeforeDependents(t *testing.T) {
	t.Parallel()

	repoRoot := filepath.Join("testdata", "repo")
	modules := []string{
		"trace/contrib/all",
		"trace/contrib/github.com/sashabaranov/go-openai",
		"trace/contrib/openai",
		"trace/contrib/langchaingo",
	}

	got, err := DependencyOrder(repoRoot, modules)
	if err != nil {
		t.Fatalf("DependencyOrder returned error: %v", err)
	}

	want := []string{
		"trace/contrib/openai",
		"trace/contrib/github.com/sashabaranov/go-openai",
		"trace/contrib/all",
		"trace/contrib/langchaingo",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DependencyOrder() = %v, want %v", got, want)
	}
}
