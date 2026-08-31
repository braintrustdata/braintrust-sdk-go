package mustache

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The template text this renderer parses comes from Braintrust: a saved prompt,
// or whatever a playground user selected. It must therefore be incapable of
// reaching anything outside the template and the variables it is given.
//
// These tests are the guard rails. If one fails, a capability has come back --
// most likely by re-syncing with upstream (see VENDOR.md) -- and it needs
// removing again, not the test relaxing.

// forbiddenImports are packages that would give a template a way out: the
// filesystem, the network, subprocesses, or the environment.
var forbiddenImports = []string{
	"os", "os/exec", "io/fs", "io/ioutil", "path/filepath",
	"net", "net/http", "net/url", "os/user", "syscall", "plugin", "unsafe",
}

func TestNoForbiddenImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("parsing import in %s: %v", name, err)
			}
			for _, forbidden := range forbiddenImports {
				if path == forbidden {
					t.Errorf("%s imports %q: a template must not be able to reach outside itself", name, path)
				}
			}
		}
	}
}

func TestNoPartialResolution(t *testing.T) {
	// A partial pulls in content from outside the template.
	if _, err := ParseString("{{>anything}}"); err == nil {
		t.Error("a partial tag must not parse")
	}
}

func TestContextMethodsAreNeverCalled(t *testing.T) {
	// Upstream resolves a name against zero-argument methods, which would let
	// template text pick Go code to run on caller data.
	out, err := Render("[{{Secret}}]", &probe{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "[]" {
		t.Errorf("got %q; a method must not be invoked by name", out)
	}
	if probeCalled {
		t.Error("template text invoked a method on a context value")
	}
}

func TestContextFuncsAreNeverCalled(t *testing.T) {
	// Upstream treats a func in the context as a lambda, calls it with the
	// section body, and re-parses what it returns as a template.
	called := false
	ctx := map[string]any{
		"fn": func(text string, render RenderFunc) (string, error) {
			called = true
			return "INVOKED", nil
		},
	}

	out, err := Render("[{{#fn}}body{{/fn}}]", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("template text invoked a func from the context")
	}
	if strings.Contains(out, "INVOKED") {
		t.Errorf("got %q; lambda output must not appear", out)
	}
}

func TestRenderedValuesAreNotReparsed(t *testing.T) {
	// A value must never be treated as template source, or a variable could
	// smuggle in template syntax.
	out, err := Render("{{v}}", map[string]any{"v": "{{secret}}", "secret": "LEAKED"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "{{secret}}" {
		t.Errorf("got %q; an interpolated value must not be re-parsed", out)
	}
}

// probe records whether a template managed to call one of its methods.
type probe struct{}

var probeCalled bool

func (p *probe) Secret() string {
	probeCalled = true
	return "SECRET"
}

// nodeGuard keeps the ast import used even if the checks above change shape.
var _ = ast.Print
